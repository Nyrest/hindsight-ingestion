package sync_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/observations"
)

type scopeSource struct{ *fakeSource }

func (s scopeSource) Info() connectors.SourceInfo {
	info := s.fakeSource.Info()
	info.Type = "notion"
	return info
}

func TestObservationScopeReimport(t *testing.T) {
	h := newHarness(t)
	connectors.RegisterSource(scopeSource{h.src})
	h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Update("source_type", "notion")
	h.setDocs(map[string]fakeDoc{"page": {rev: "1", content: "text", image: true}})
	cfg, err := h.engine.Settings.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.InlineMultimodalEnabled = true
	cfg.FilePolicy.Images = true
	if err := h.engine.Settings.Put(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	var before models.TaskItem
	h.db.First(&before, "task_id = ?", h.task.ID)
	for _, doc := range h.dest.docs {
		if doc.ObservationScopes != nil {
			t.Fatal("legacy combined changed")
		}
	}
	cfg.ObservationScope = observations.Scope{Rule: "custom", Scopes: [][]observations.TagRef{{{Kind: "dynamic", Value: "task"}, {Kind: "literal", Value: "future-tag"}}}}
	if err := h.engine.Settings.Put(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	var after models.TaskItem
	h.db.First(&after, "task_id = ?", h.task.ID)
	if before.SyncedFingerprint == after.SyncedFingerprint {
		t.Fatal("scope did not change fingerprint")
	}
	if h.dest.retains != 2 {
		t.Fatal("scope failed to re-retain", h.dest.retains)
	}
	for _, doc := range h.dest.docs {
		want, _ := json.Marshal([][]string{{"future-tag", "ingestion_task:" + h.task.ID}})
		if string(doc.ObservationScopes) != string(want) || len(doc.Blocks) == 0 {
			t.Fatal("scope/inline blocks not forwarded", doc)
		}
		if slices.Contains(doc.Tags, "future-tag") {
			t.Fatal("literal scope injected into tags")
		}
	}
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	if h.dest.retains != 2 {
		t.Fatal("unchanged scope retained again")
	}
	cfg.ObservationScope = observations.Scope{Rule: "custom", Scopes: [][]observations.TagRef{{{Kind: "dynamic", Value: "source_group"}}}}
	if err := h.engine.Settings.Put(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	r := h.run(models.SyncFull)
	if r.FailedCount != 1 || h.dest.retains != 2 {
		t.Fatal("unresolved dynamic scope broadened", r)
	}
}

func TestFileScopeKeepsCombined(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"file": {rev: "1", content: "file text", file: true, name: "file.md"}})
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	var before models.TaskItem
	h.db.First(&before, "task_id = ?", h.task.ID)
	opensBefore := len(h.opened())
	cfg, err := h.engine.Settings.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ObservationScope = observations.Scope{Rule: "per_tag"}
	if err := h.engine.Settings.Put(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	var after models.TaskItem
	h.db.First(&after, "task_id = ?", h.task.ID)
	if before.SyncedFingerprint != after.SyncedFingerprint || len(h.opened()) != opensBefore {
		t.Fatalf("fingerprints equal=%t, content opens=%v", before.SyncedFingerprint == after.SyncedFingerprint, h.opened())
	}
	for _, doc := range h.dest.docs {
		if doc.ObservationScopes != nil {
			t.Fatal("file scope is not combined")
		}
	}
}

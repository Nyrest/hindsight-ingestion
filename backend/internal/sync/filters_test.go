package sync_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestNestedFilterPreviewAndReconciliation(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{
		"a": {rev: "1", content: "alpha", name: "Report.md"},
		"b": {rev: "1", content: "beta", name: "Notes.txt"},
		"c": {rev: "1", content: "gamma", name: "Archive.md"},
	})
	expectStatus(t, h.run(""), models.RunSucceeded)
	filter := connectors.Filter{Mode: "advanced", AdvancedQuery: "provider query", Expression: connectors.FilterGroup{
		Rules: []connectors.FilterRule{{Field: "name", Operator: connectors.OpNotContains, Value: "archive"}},
		Groups: []connectors.FilterGroup{{Combinator: "or", Rules: []connectors.FilterRule{
			{Field: "name", Operator: connectors.OpRegex, Value: `^Report\.md$`},
			{Field: "name", Operator: connectors.OpContainsAll, Value: []string{"notes", ".txt"}},
		}}},
	}}
	setFilter := func() {
		data, err := json.Marshal(filter)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Updates(map[string]any{"source_filter_json": string(data), "reconcile_required": true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	setFilter()
	preview, err := h.runs.DryRun(context.Background(), h.task.ID)
	if err != nil || preview.DeletedCount != 1 || preview.UnchangedCount != 2 || len(h.dest.docs) != 3 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	run := h.run("")
	expectStatus(t, run, models.RunSucceeded)
	if run.DeletedCount != 1 || len(h.dest.docs) != 2 || h.dest.docs[h.documentID("c")].Content != "" {
		t.Fatalf("reconcile: %+v docs=%v", run, h.dest.docs)
	}
	filter.Expression.Combinator = "or"
	filter.Expression.Rules = []connectors.FilterRule{{Field: "name", Operator: connectors.OpStartsWith, Value: "archive"}}
	setFilter()
	run = h.run("")
	expectStatus(t, run, models.RunSucceeded)
	if run.CreatedCount != 1 || len(h.dest.docs) != 3 {
		t.Fatalf("expanded OR scope: %+v", run)
	}
}

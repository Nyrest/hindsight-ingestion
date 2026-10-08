package sync_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
)

func TestInlineDocumentsAreSubmittedSeparately(t *testing.T) {
	h := newHarness(t)
	v := settings.Defaults
	v.InlineMultimodalEnabled = true
	v.FilePolicy.Images = true
	if err := h.engine.Settings.Put(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "a"}, "b": {rev: "1", content: "image", image: true}, "c": {rev: "1", content: "image", image: true}, "d": {rev: "1", content: "d"}})
	expectStatus(t, h.run(""), models.RunSucceeded)
	if len(h.dest.batchSizes) != 4 {
		t.Fatalf("multimodal shared a text batch: %v", h.dest.batchSizes)
	}
	for _, size := range h.dest.batchSizes {
		if size != 1 {
			t.Fatalf("batch=%v", h.dest.batchSizes)
		}
	}
	for _, id := range []string{"b", "c"} {
		item := h.dest.docs[id]
		if item.UpdateMode != "replace" || item.DocumentID != id || item.Metadata["_ingestion_task_id"] != h.task.ID || !contains(item.Tags, "team:a") {
			t.Fatal("multimodal metadata changed")
		}
	}
	h.dest.failNext = true
	h.setDocs(map[string]fakeDoc{"b": {rev: "2", content: "new image", image: true}})
	before := h.state().CommittedCursorJSON
	expectStatus(t, h.run(""), models.RunFailed)
	if h.state().CommittedCursorJSON != before {
		t.Fatal("destination rejection committed cursor")
	}
}

func TestInlineIndependentPolicies(t *testing.T) {
	for _, inlineMode := range []string{"global", "override"} {
		for _, fileMode := range []string{"global", "override"} {
			for _, enabled := range []bool{false, true} {
				for _, images := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s-%s-%t-%t", inlineMode, fileMode, enabled, images), func(t *testing.T) {
						h := newHarness(t)
						v := settings.Defaults
						v.InlineMultimodalEnabled = enabled
						v.FilePolicy = connectors.FilePolicy{Documents: true, Images: images}
						if inlineMode == "override" {
							v.InlineMultimodalEnabled = !enabled
						}
						if fileMode == "override" {
							v.FilePolicy.Images = !images
						}
						if err := h.engine.Settings.Put(t.Context(), v); err != nil {
							t.Fatal(err)
						}
						policy, _ := json.Marshal(connectors.FilePolicy{Documents: true, Images: images})
						h.task.InlineMultimodalMode = inlineMode
						h.task.InlineMultimodalEnabled = enabled
						h.task.FilePolicyMode = fileMode
						h.task.FilePolicyJSON = string(policy)
						if err := h.db.Save(&h.task).Error; err != nil {
							t.Fatal(err)
						}
						h.setDocs(map[string]fakeDoc{"note": {rev: "1", content: "markdown", image: true}, "file.pdf": {rev: "1", content: "file", file: true}})
						expectStatus(t, h.run(""), models.RunSucceeded)
						if got := len(h.dest.docs["note"].Blocks) > 0; got != (enabled && images) {
							t.Fatalf("images=%t want=%t", got, enabled && images)
						}
						if h.dest.docs["note"].Content != "markdown" || len(h.dest.files) != 1 {
							t.Fatal("plainText flag or inline switch gated note/files")
						}
					})
				}
			}
		}
	}
}

func TestInlineRetryAndRemoval(t *testing.T) {
	for _, disable := range []string{"inline", "images"} {
		t.Run(disable, func(t *testing.T) {
			h := newHarness(t)
			v := settings.Defaults
			v.InlineMultimodalEnabled = true
			v.FilePolicy.Images = true
			if err := h.engine.Settings.Put(t.Context(), v); err != nil {
				t.Fatal(err)
			}
			h.setDocs(map[string]fakeDoc{"note": {rev: "1", content: "![caption](unavailable)", image: true, warnings: []string{"image unavailable"}}})
			expectStatus(t, h.run(""), models.RunSucceeded)
			var row models.TaskItem
			h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, "note")
			if !row.NeedsImageRetry || row.LastError != "" || h.state().CommittedCursorJSON == "" {
				t.Fatal("partial submission blocked cursor or lost retry marker")
			}
			beforeState := h.state()
			beforeRow := row
			retains := h.dest.retains
			preview, err := h.engine.DryRun(t.Context(), h.task.ID)
			if err != nil || preview.UpdatedCount != 1 || len(preview.Items[0].Warnings) != 1 {
				t.Fatalf("dry-run: %+v %v", preview, err)
			}
			h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, "note")
			a, _ := json.Marshal([]any{beforeState, beforeRow})
			b, _ := json.Marshal([]any{h.state(), row})
			var runs, ops int64
			h.db.Model(&models.TaskRun{}).Where("task_id = ?", h.task.ID).Count(&runs)
			h.db.Model(&models.HindsightOperation{}).Where("run_id IN (?)", h.db.Model(&models.TaskRun{}).Select("id").Where("task_id = ?", h.task.ID)).Count(&ops)
			if string(a) != string(b) || retains != h.dest.retains || runs != 1 || ops != 1 {
				t.Fatal("dry-run wrote state")
			}
			h.setDocs(map[string]fakeDoc{"note": {rev: "1", content: "![caption](available)", image: true}})
			expectStatus(t, h.run(""), models.RunSucceeded)
			if len(h.opened()) != 0 {
				t.Fatal("incremental polled unchanged image")
			}
			// The periodic reconciliation uses this same full-scan path.
			h.db.Model(&models.TaskState{}).Where("task_id = ?", h.task.ID).Update("last_full_reconcile_at", nil)
			expectStatus(t, h.run(""), models.RunSucceeded)
			h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, "note")
			if row.NeedsImageRetry || len(h.dest.docs["note"].Blocks) == 0 {
				t.Fatal("full scan failed to fill missing image")
			}
			if disable == "inline" {
				v.InlineMultimodalEnabled = false
			} else {
				v.FilePolicy.Images = false
			}
			h.engine.Settings.Put(t.Context(), v)
			expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
			h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, "note")
			if row.NeedsImageRetry || len(h.dest.docs["note"].Blocks) != 0 || h.dest.docs["note"].Content != "![caption](available)" {
				t.Fatal("disabling images did not replace multimodal document")
			}
			h.setDocs(map[string]fakeDoc{})
			expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
			if len(h.dest.docs) != 0 {
				t.Fatal("document deletion failed")
			}
		})
	}
}

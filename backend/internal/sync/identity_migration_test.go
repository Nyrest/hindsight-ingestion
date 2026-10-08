package sync_test

import (
	"context"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestIdentityMigration(t *testing.T) {
	for _, mode := range []string{"success", "source failure", "upload failure", "operation failure", "failed then removed", "duplicate deletion", "empty content"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "alpha", file: mode == "upload failure", name: "a.txt"}})
			expectStatus(t, h.run(""), models.RunSucceeded)
			const legacy = "hi_legacy_task_scoped_id"
			delete(h.dest.docs, "a")
			h.dest.docs[legacy] = hindsight.MemoryItem{DocumentID: legacy, Content: "alpha"}
			if err := h.db.Model(&models.TaskItem{}).Where("task_id = ? AND source_item_id = ?", h.task.ID, "a").Update("destination_document_id", legacy).Error; err != nil {
				t.Fatal(err)
			}
			preview, err := h.runs.DryRun(context.Background(), h.task.ID)
			if err != nil || preview.UpdatedCount != 1 || preview.UnchangedCount != 0 {
				t.Fatalf("migration preview: %+v %v", preview, err)
			}
			switch mode {
			case "source failure":
				h.src.failIDs["a"] = true
				expectStatus(t, h.run(""), models.RunFailed)
				if _, ok := h.dest.docs[legacy]; !ok {
					t.Fatal("source read failure deleted the legacy document")
				}
				delete(h.src.failIDs, "a")
			case "upload failure":
				h.dest.failNext = true
				expectStatus(t, h.run(""), models.RunFailed)
				var row models.TaskItem
				h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, "a")
				if row.PreviousDocumentID != legacy || row.DestinationDocumentID != "a" {
					t.Fatalf("upload failure lost an identity: %+v", row)
				}
			case "operation failure", "failed then removed":
				h.dest.failOps = true
				expectStatus(t, h.run(""), models.RunFailed)
				var row models.TaskItem
				h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, "a")
				if row.PreviousDocumentID != legacy || row.DestinationDocumentID != "a" {
					t.Fatalf("failed migration lost an identity: %+v", row)
				}
				h.dest.failOps = false
				if mode == "failed then removed" {
					h.setDocs(map[string]fakeDoc{})
				}
			case "duplicate deletion":
				h.src.deleteAfterScan = "a"
			case "empty content":
				h.setDocs(map[string]fakeDoc{"a": {rev: "1"}})
			}
			result := h.run("")
			expectStatus(t, result, models.RunSucceeded)
			if result.SyncMode != models.SyncFull {
				t.Fatal("identity migration used an incremental scan")
			}
			if _, exists := h.dest.docs[legacy]; exists {
				t.Fatal("legacy document was left behind")
			}
			_, present := h.dest.docs["a"]
			wantPresent := mode != "failed then removed" && mode != "duplicate deletion" && mode != "empty content"
			if present != wantPresent {
				t.Fatalf("canonical document present=%v, want %v", present, wantPresent)
			}
			var row models.TaskItem
			if err := h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, "a").Error; err == nil && row.PreviousDocumentID != "" {
				t.Fatalf("completed migration retained previous identity: %+v", row)
			}
		})
	}
}

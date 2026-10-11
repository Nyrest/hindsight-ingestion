package sync_test

import (
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/google/uuid"
)

func TestTasksRetainSameSourceItemIndependently(t *testing.T) {
	for _, file := range []bool{false, true} {
		name := "text"
		if file {
			name = "file"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.setDocs(map[string]fakeDoc{"same-item": {rev: "1", content: "original", file: file, name: "same.txt"}})
			a := h.task
			idA := h.documentID("same-item")
			expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)

			b := a
			b.ID = uuid.NewString()
			b.CustomTagsJSON = `["team:b"]`
			if err := h.db.Create(&b).Error; err != nil {
				t.Fatal(err)
			}
			h.task = b
			idB := h.documentID("same-item")
			preview, err := h.runs.DryRun(t.Context(), b.ID)
			if err != nil || preview.CreatedCount != 1 || preview.UnchangedCount != 0 {
				t.Fatalf("second task preview: %+v %v", preview, err)
			}
			run := h.run(models.SyncFull)
			expectStatus(t, run, models.RunSucceeded)
			if idA == idB || run.CreatedCount != 1 || len(h.dest.docs) != 2 {
				t.Fatal("tasks did not create distinct documents", run, h.dest.docs)
			}
			if !contains(h.dest.docs[idA].Tags, "team:a") || !contains(h.dest.docs[idB].Tags, "team:b") {
				t.Fatal("task policies overwrote each other")
			}
			if file && (h.dest.files[idA] != "original" || h.dest.files[idB] != "original") {
				t.Fatal("file uploads did not use distinct document IDs")
			}
			h.setDocs(map[string]fakeDoc{"same-item": {rev: "2", content: "updated", file: file, name: "same.txt"}})
			expectStatus(t, h.run(models.SyncReingest), models.RunSucceeded)
			if len(h.dest.docs) != 2 || h.dest.docs[idA].Content != "original" || h.dest.docs[idB].Content != "updated" {
				t.Fatal("reingestion changed another task's document or its own ID")
			}
			h.setDocs(map[string]fakeDoc{})
			run = h.run(models.SyncFull)
			expectStatus(t, run, models.RunSucceeded)
			if run.DeletedCount != 1 || len(h.dest.docs) != 1 || h.dest.docs[idA].Content != "original" || !contains(h.dest.deleted, idB) {
				t.Fatal("deletion affected another task's document", run, h.dest.deleted)
			}
		})
	}
}

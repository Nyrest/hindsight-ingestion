package sync_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/runner"
)

func TestDryRunDoesNotWrite(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"same": {rev: "1", content: "same"}, "changed": {rev: "1", content: "before"}, "gone": {rev: "1", content: "gone"}})
	expectStatus(t, h.run(""), models.RunSucceeded)
	h.setDocs(map[string]fakeDoc{"same": {rev: "1", content: "same"}, "changed": {rev: "2", content: "after"}, "new": {rev: "2", content: "new"}, "empty": {rev: "2"}, "bad": {rev: "2", content: "bad"}})
	h.src.failIDs["bad"] = true
	snapshot := func() string {
		var task models.Task
		var state models.TaskState
		var items []models.TaskItem
		var runs []models.TaskRun
		var ops []models.HindsightOperation
		h.db.First(&task, "id = ?", h.task.ID)
		h.db.First(&state, "task_id = ?", h.task.ID)
		h.db.Order("source_item_id").Find(&items)
		h.db.Order("id").Find(&runs)
		h.db.Order("id").Find(&ops)
		b, err := json.Marshal([]any{task, state, items, runs, ops, h.dest.docs, h.dest.deleted})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	before := snapshot()
	preview, err := h.runs.DryRun(context.Background(), h.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Complete || preview.CreatedCount != 1 || preview.UpdatedCount != 1 || preview.DeletedCount != 1 || preview.UnchangedCount != 1 || preview.SkippedCount != 1 || preview.FailedCount != 1 {
		t.Fatalf("preview: %+v", preview)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("dry-run wrote sync/destination state")
	}
	if contains(h.opened(), "same") {
		t.Fatal("dry-run fetched unchanged content")
	}
	h.src.partial = true
	preview, err = h.runs.DryRun(context.Background(), h.task.ID)
	if err != nil || preview.Complete || preview.DeletedCount != 0 {
		t.Fatalf("partial inventory: %+v %v", preview, err)
	}
	if before != snapshot() {
		t.Fatal("partial preview changed state")
	}
}

func TestDryRunGuardsTask(t *testing.T) {
	h := newHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "alpha"}})
	h.src.scanHook = func() {
		if _, err := h.runs.Start(h.task.ID, models.TriggerManual, "", nil, false); !errors.Is(err, runner.ErrAlreadyRunning) {
			t.Errorf("sync not guarded: %v", err)
		}
		if _, err := h.runs.DryRun(context.Background(), h.task.ID); !errors.Is(err, runner.ErrAlreadyRunning) {
			t.Errorf("preview not guarded: %v", err)
		}
	}
	if _, err := h.runs.DryRun(context.Background(), h.task.ID); err != nil {
		t.Fatal(err)
	}
	if _, active := h.runs.Active(h.task.ID); active {
		t.Fatal("guard not released")
	}
}

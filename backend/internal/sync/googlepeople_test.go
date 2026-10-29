package sync_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/googlepeople"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type transactionalPeople struct {
	*fakeSource
	store     *googlepeople.Connector
	commitErr error
	lastFull  bool
	scans     int
	resource  string
	complete  bool
}

func (s *transactionalPeople) Info() connectors.SourceInfo {
	info := s.fakeSource.Info()
	info.Type = googlepeople.SourceType
	info.Capabilities.IncrementalMode = connectors.IncrementalSyncToken
	info.Capabilities.DeletionMode = connectors.DeletionDelta
	info.Capabilities.SupportsFiles = false
	return info
}

func (s *transactionalPeople) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	s.lastFull, s.scans = req.Full, s.scans+1
	result, err := s.fakeSource.Scan(ctx, req)
	if s.complete {
		result.Complete = true
	}
	if !req.Preview && err == nil {
		err = s.store.DB.WithContext(ctx).Create(&models.GooglePeopleScanAlias{RunID: req.RunID, TaskID: req.TaskID, ResourceName: s.resource, ItemID: "contact:1", Subject: "account"}).Error
	}
	return result, err
}

func (s *transactionalPeople) CommitScan(ctx context.Context, run string, tx *gorm.DB) error {
	if err := s.store.CommitScan(ctx, run, tx); err != nil {
		return err
	}
	return s.commitErr
}

func (s *transactionalPeople) AbortScan(ctx context.Context, run string) error {
	return s.store.AbortScan(ctx, run)
}

func peopleHarness(t *testing.T) (*harness, *transactionalPeople) {
	h := newHarness(t)
	previous, _ := connectors.Source(googlepeople.SourceType)
	source := &transactionalPeople{fakeSource: h.src, store: &googlepeople.Connector{DB: h.db}, resource: "people/1"}
	connectors.RegisterSource(source)
	t.Cleanup(func() { connectors.RegisterSource(previous) })
	h.task.SourceType = googlepeople.SourceType
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	h.setDocs(map[string]fakeDoc{"contact:1": {rev: "1", content: "Alice"}})
	return h, source
}

func TestGooglePeopleMirrorsEmptyInventoryAndUsesDocumentPrefix(t *testing.T) {
	h, _ := peopleHarness(t)
	first := h.run("")
	expectStatus(t, first, models.RunSucceeded)
	if first.CreatedCount != 1 || h.state().CommittedCursorJSON == "" {
		t.Fatal(first)
	}
	for id := range h.dest.docs {
		if !strings.HasPrefix(id, "google_people:") {
			t.Fatal("wrong document prefix", id)
		}
	}
	second := h.run(models.SyncFull)
	expectStatus(t, second, models.RunSucceeded)
	if second.UnchangedCount != 1 || second.UpdatedCount != 0 {
		t.Fatal("unchanged full scan rewrote contact", second)
	}
	h.setDocs(map[string]fakeDoc{})
	empty := h.run(models.SyncFull)
	expectStatus(t, empty, models.RunSucceeded)
	var aliases int64
	h.db.Model(&models.GooglePeopleIdentity{}).Where("task_id = ?", h.task.ID).Count(&aliases)
	if empty.DeletedCount != 1 || len(h.dest.docs) != 0 || aliases != 0 {
		t.Fatal("empty inventory retained contact", empty, aliases)
	}
}

func TestGooglePeopleWriteAndCommitFailuresPreserveCursorAndAliases(t *testing.T) {
	for _, failure := range []string{"content", "destination", "commit", "partial"} {
		t.Run(failure, func(t *testing.T) {
			h, source := peopleHarness(t)
			h.setDocs(map[string]fakeDoc{"contact:1": {rev: "1", content: "Alice"}, "contact:gone": {rev: "1", content: "Removed later"}})
			expectStatus(t, h.run(""), models.RunSucceeded)
			before := h.state().CommittedCursorJSON
			source.resource = "people/new1"
			h.setDocs(map[string]fakeDoc{"contact:1": {rev: "2", content: "Updated"}, "contact:2": {rev: "2", content: "Bob"}})
			switch failure {
			case "content":
				source.failIDs["contact:1"] = true
			case "destination":
				h.dest.failNext = true
			case "commit":
				source.commitErr = errors.New("forced transaction rollback")
			case "partial":
				source.partial = true
			}
			failed := h.run(models.SyncFull)
			expectStatus(t, failed, models.RunFailed)
			var aliases, staged int64
			h.db.Model(&models.GooglePeopleIdentity{}).Where("task_id = ? AND resource_name = ?", h.task.ID, source.resource).Count(&aliases)
			h.db.Model(&models.GooglePeopleScanAlias{}).Where("task_id = ?", h.task.ID).Count(&staged)
			if h.state().CommittedCursorJSON != before || h.state().PendingScanRunID == "" || aliases != 0 || staged != 0 || (failure != "commit" && len(h.dest.deleted) != 0) {
				t.Fatal("failure committed scan state", failed, aliases, staged)
			}
			source.commitErr = nil
			source.failIDs = map[string]bool{}
			expectStatus(t, h.run(""), models.RunSucceeded)
			if !source.lastFull || h.state().PendingScanRunID != "" {
				t.Fatal("interrupted scan was not recovered with full reconciliation")
			}
		})
	}
}

func TestGooglePeopleLostRetainResponseFencesDeletion(t *testing.T) {
	h, source := peopleHarness(t)
	h.dest.loseResponse = true
	failed := h.run("")
	expectStatus(t, failed, models.RunFailed)
	var row models.TaskItem
	if err := h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, "contact:1").Error; err != nil || !row.DestinationPresent || row.LastError == "" {
		t.Fatal("missing durable retain intent", row, err)
	}
	var intent models.HindsightOperation
	if err := h.db.First(&intent, "run_id = ? AND type = ?", failed.ID, "retain_intent").Error; err != nil {
		t.Fatal(err)
	}
	h.dest.loseResponse = false
	h.setDocs(map[string]fakeDoc{})
	scans := source.scans
	run := models.TaskRun{ID: uuid.NewString(), TaskID: h.task.ID, Status: models.RunRunning}
	if err := h.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := h.engine.Execute(ctx, run.ID, sync.RunOptions{}); err == nil || source.scans != scans || len(h.dest.deleted) != 0 {
		t.Fatal("outstanding retain did not fence scan and deletion", err)
	}
	h.dest.mu.Lock()
	h.dest.opStatus[intent.RemoteOperationID] = "completed"
	h.dest.mu.Unlock()
	recovered := h.run("")
	expectStatus(t, recovered, models.RunSucceeded)
	if !source.lastFull || recovered.DeletedCount != 1 || len(h.dest.docs) != 0 {
		t.Fatal("unconfirmed contact was orphaned", recovered)
	}
}

func TestGooglePeopleFullFallbackUpdatesRunAndReconcileTime(t *testing.T) {
	h, source := peopleHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state().LastFullReconcileAt
	source.complete = true
	run := h.run("")
	expectStatus(t, run, models.RunSucceeded)
	if source.lastFull || run.SyncMode != models.SyncFull || !h.state().LastFullReconcileAt.After(*before) {
		t.Fatal("connector full fallback was not recorded", run)
	}
}

func TestGooglePeopleFilterExitAndDryRunPreserveState(t *testing.T) {
	h, _ := peopleHarness(t)
	h.setDocs(map[string]fakeDoc{"contact:1": {rev: "1", name: "Alice", content: "Alice"}, "contact:2": {rev: "1", name: "Bob", content: "Bob"}})
	expectStatus(t, h.run(""), models.RunSucceeded)
	filter := `{"mode":"simple","expression":{"rules":[{"field":"name","operator":"equals","value":"Alice"}]}}`
	if err := h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Updates(map[string]any{"source_filter_json": filter, "reconcile_required": true, "config_revision": 2}).Error; err != nil {
		t.Fatal(err)
	}
	before := h.state()
	var aliasesBefore, runsBefore int64
	h.db.Model(&models.GooglePeopleIdentity{}).Where("task_id = ?", h.task.ID).Count(&aliasesBefore)
	h.db.Model(&models.TaskRun{}).Where("task_id = ?", h.task.ID).Count(&runsBefore)
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || !preview.Complete || preview.DeletedCount != 1 || preview.UnchangedCount != 1 {
		t.Fatal(preview, err)
	}
	after := h.state()
	var aliasesAfter, runsAfter int64
	h.db.Model(&models.GooglePeopleIdentity{}).Where("task_id = ?", h.task.ID).Count(&aliasesAfter)
	h.db.Model(&models.TaskRun{}).Where("task_id = ?", h.task.ID).Count(&runsAfter)
	if before.CommittedCursorJSON != after.CommittedCursorJSON || before.ScanGeneration != after.ScanGeneration || before.PendingScanRunID != after.PendingScanRunID || aliasesBefore != aliasesAfter || runsBefore != runsAfter || len(h.dest.docs) != 2 || len(h.dest.deleted) != 0 {
		t.Fatal("dry-run changed sync state")
	}
	run := h.run("")
	expectStatus(t, run, models.RunSucceeded)
	if run.DeletedCount != 1 || run.UnchangedCount != 1 || len(h.dest.docs) != 1 {
		t.Fatal("filter scope exit did not remove Bob", run)
	}
}

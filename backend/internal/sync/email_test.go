package sync_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	emailconnector "github.com/Nyrest/hindsight-ingestion/internal/connectors/email"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type transactionalEmail struct {
	*fakeSource
	store     *emailconnector.Connector
	commitErr error
	lastFull  bool
	scans     int
}

func (f *transactionalEmail) Info() connectors.SourceInfo {
	info := f.fakeSource.Info()
	info.Type = "email"
	info.Capabilities.SupportsFiles = false
	return info
}
func (f *transactionalEmail) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	f.lastFull = req.Full
	f.scans++
	res, err := f.fakeSource.Scan(ctx, req)
	if !req.Preview && err == nil {
		f.mu.Lock()
		defer f.mu.Unlock()
		for id := range f.docs {
			if err := f.store.DB.WithContext(ctx).Create(&models.EmailScanIdentity{RunID: req.RunID, TaskID: req.TaskID, ItemID: id, AccountKey: "account", SnapshotJSON: "{}"}).Error; err != nil {
				return res, err
			}
			if err := f.store.DB.WithContext(ctx).Create(&models.EmailScanOccurrence{RunID: req.RunID, TaskID: req.TaskID, ItemID: id, AccountKey: "account", FolderKey: id, FolderName: "INBOX", UIDValidity: 1, UID: 1, PartsJSON: "[]"}).Error; err != nil {
				return res, err
			}
		}
	}
	return res, err
}
func (f *transactionalEmail) CommitScan(ctx context.Context, run string, tx *gorm.DB) error {
	if err := f.store.CommitScan(ctx, run, tx); err != nil {
		return err
	}
	return f.commitErr
}
func (f *transactionalEmail) AbortScan(ctx context.Context, run string) error {
	return f.store.AbortScan(ctx, run)
}
func emailHarness(t *testing.T) (*harness, *transactionalEmail) {
	h := newHarness(t)
	source := &transactionalEmail{fakeSource: h.src, store: &emailconnector.Connector{DB: h.db}}
	connectors.RegisterSource(source)
	h.task.SourceType = "email"
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	return h, source
}

func TestEmailFailuresBlockDeletionAndRecoveryForcesFull(t *testing.T) {
	h, source := emailHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "A"}, "b": {rev: "1", content: "B"}})
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state()
	h.setDocs(map[string]fakeDoc{"b": {rev: "2", content: "B2"}})
	source.failIDs["b"] = true
	failed := h.run(models.SyncFull)
	expectStatus(t, failed, models.RunFailed)
	state := h.state()
	if state.CommittedCursorJSON != before.CommittedCursorJSON || state.PendingScanRunID == "" || len(h.dest.deleted) > 0 {
		t.Fatal("failure deleted documents or advanced state")
	}
	var staged int64
	h.db.Model(&models.EmailScanOccurrence{}).Where("task_id = ?", h.task.ID).Count(&staged)
	if staged != 0 {
		t.Fatal("failed staging was not cleared")
	}
	source.failIDs = map[string]bool{}
	expectStatus(t, h.run(""), models.RunSucceeded)
	if !source.lastFull || len(h.dest.deleted) != 1 || h.state().PendingScanRunID != "" {
		t.Fatal("recovery did not perform full reconciliation")
	}
}

func TestEmailCursorAndIdentityCommitAreAtomic(t *testing.T) {
	h, source := emailHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "A"}})
	source.commitErr = errors.New("injected commit failure")
	expectStatus(t, h.run(""), models.RunFailed)
	var identities, occurrences int64
	h.db.Model(&models.EmailIdentity{}).Where("task_id = ?", h.task.ID).Count(&identities)
	h.db.Model(&models.EmailOccurrence{}).Where("task_id = ?", h.task.ID).Count(&occurrences)
	if h.state().CommittedCursorJSON != "" || identities != 0 || occurrences != 0 {
		t.Fatal("connector state escaped failed cursor transaction")
	}
	source.commitErr = nil
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state()
	var count int64
	h.db.Model(&models.TaskRun{}).Count(&count)
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || !preview.Complete || preview.UnchangedCount != 1 {
		t.Fatal(preview, err)
	}
	var after int64
	h.db.Model(&models.TaskRun{}).Count(&after)
	if after != count || h.state().CommittedCursorJSON != before.CommittedCursorJSON || h.state().ScanGeneration != before.ScanGeneration {
		t.Fatal("dry-run wrote run or cursor state")
	}
}

func TestEmailLostResponseIsFencedBeforeNewWrites(t *testing.T) {
	h, source := emailHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "Accepted remotely"}})
	h.dest.loseResponse = true
	failed := h.run("")
	expectStatus(t, failed, models.RunFailed)
	var intent models.HindsightOperation
	h.db.Where("type = ? AND run_id = ?", "retain_intent", failed.ID).First(&intent)
	if intent.RemoteOperationID == "" || intent.Status != "submitted" {
		t.Fatal("lost response has no durable operation identity")
	}
	h.setDocs(map[string]fakeDoc{})
	h.dest.loseResponse = false
	before := source.scans
	run := models.TaskRun{ID: uuid.NewString(), TaskID: h.task.ID, Status: models.RunRunning}
	h.db.Create(&run)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := h.engine.Execute(ctx, run.ID, sync.RunOptions{}); err == nil || source.scans != before || len(h.dest.deleted) > 0 {
		t.Fatal("prior processing retain did not fence scan")
	}
	h.dest.mu.Lock()
	h.dest.opStatus[intent.RemoteOperationID] = "completed"
	h.dest.mu.Unlock()
	expectStatus(t, h.run(""), models.RunSucceeded)
	if len(h.dest.deleted) != 1 {
		t.Fatal("accepted mail was not deleted after fenced full scan")
	}
}

func TestEmailConfigurationChangeFencesStateAndDelete(t *testing.T) {
	h, _ := emailHarness(t)
	h.setDocs(map[string]fakeDoc{"a": {rev: "1", content: "A"}})
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state()
	h.setDocs(map[string]fakeDoc{})
	h.src.scanHook = func() {
		h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Update("config_revision", gorm.Expr("config_revision + 1"))
	}
	expectStatus(t, h.run(models.SyncFull), models.RunFailed)
	if len(h.dest.deleted) > 0 || h.state().CommittedCursorJSON != before.CommittedCursorJSON {
		t.Fatal("changed task authorized deletion or commit")
	}
}

func TestEmailDeletionReconcilesMultiplePages(t *testing.T) {
	h, _ := emailHarness(t)
	rows := make([]models.TaskItem, 601)
	for i := range rows {
		rows[i] = models.TaskItem{TaskID: h.task.ID, SourceItemID: fmt.Sprintf("mail-%04d", i), DestinationDocumentID: fmt.Sprintf("document-%04d", i), DestinationPresent: true}
	}
	if err := h.db.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	expectStatus(t, h.run(models.SyncFull), models.RunSucceeded)
	var remaining int64
	h.db.Model(&models.TaskItem{}).Where("task_id = ?", h.task.ID).Count(&remaining)
	if remaining != 0 || len(h.dest.deleted) != len(rows) {
		t.Fatalf("paged deletion left %d items and deleted %d documents", remaining, len(h.dest.deleted))
	}
}

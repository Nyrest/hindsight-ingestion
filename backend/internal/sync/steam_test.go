package sync_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/steam"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

// Isolate engine inventory behavior from Steam's network and rendering tests.
type pollingSteam struct {
	*fakeSource
	lastFull     bool
	lastCursor   json.RawMessage
	delayedError error
	scans        int
}

func (s *pollingSteam) Info() connectors.SourceInfo {
	info := s.fakeSource.Info()
	info.Type = steam.SourceType
	info.Capabilities.IncrementalMode = connectors.IncrementalPollingInventory
	info.Capabilities.DeletionMode = connectors.DeletionScanGeneration
	return info
}
func (s *pollingSteam) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	s.scans++
	s.lastFull, s.lastCursor = req.Full, append(json.RawMessage(nil), req.Cursor...)
	// Polling inventories emit every item even in incremental mode.
	req.Cursor = nil
	req.Full = true
	res, err := s.fakeSource.Scan(ctx, req)
	res.Error = s.delayedError
	return res, err
}

func TestSteamLostResponseRecovery(t *testing.T) {
	for _, existing := range []bool{true, false} {
		t.Run(map[bool]string{true: "reverted_content", false: "disappeared_new_document"}[existing], func(t *testing.T) {
			h, source := steamHarness(t)
			const id = "steam_game:player:620"
			if existing {
				h.setDocs(map[string]fakeDoc{id: {rev: "A", content: "A"}})
				baseline := h.run("")
				expectStatus(t, baseline, models.RunSucceeded)
				if baseline.CreatedCount != 1 || baseline.UpdatedCount != 0 {
					t.Fatal("new document accounting", baseline)
				}
			}
			before := h.state().CommittedCursorJSON
			h.setDocs(map[string]fakeDoc{id: {rev: "B", content: "B"}})
			h.dest.loseResponse = true
			failed := h.run("")
			expectStatus(t, failed, models.RunFailed)
			var row models.TaskItem
			if err := h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, id).Error; err != nil {
				t.Fatal(err)
			}
			if row.LastError == "" || !row.DestinationPresent || h.state().CommittedCursorJSON != before {
				t.Fatal("unconfirmed remote write was not retained for recovery", row)
			}
			var intent models.HindsightOperation
			if err := h.db.First(&intent, "type = ? AND run_id = ?", "retain_intent", failed.ID).Error; err != nil {
				t.Fatal(err)
			}
			if intent.RemoteOperationID == "" || intent.Status != "submitted" {
				t.Fatal("missing durable operation identity", intent)
			}
			h.dest.loseResponse = false
			if existing {
				h.setDocs(map[string]fakeDoc{id: {rev: "A", content: "A"}})
			} else {
				h.setDocs(map[string]fakeDoc{})
			}
			scans, retains := source.scans, h.dest.retains
			run := models.TaskRun{ID: uuid.NewString(), TaskID: h.task.ID, Status: models.RunRunning}
			if err := h.db.Create(&run).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			if err := h.engine.Execute(ctx, run.ID, sync.RunOptions{}); err == nil || source.scans != scans || h.dest.retains != retains || len(h.dest.deleted) != 0 || h.state().CommittedCursorJSON != before {
				t.Fatal("prior processing retain did not fence scan, writes, deletion and cursor", err)
			}
			h.dest.mu.Lock()
			h.dest.opStatus[intent.RemoteOperationID] = "completed"
			h.dest.mu.Unlock()
			recovered := h.run("")
			expectStatus(t, recovered, models.RunSucceeded)
			if !source.lastFull {
				t.Fatal("unconfirmed write did not force reconciliation")
			}
			if existing {
				if recovered.UpdatedCount != 1 || recovered.CreatedCount != 0 || recovered.UnchangedCount != 0 || len(h.dest.docs) != 1 {
					t.Fatal("reverted content was skipped or miscounted", recovered)
				}
				for _, doc := range h.dest.docs {
					if doc.Content != "A" {
						t.Fatal("remote content remains stale", doc.Content)
					}
				}
				if err := h.db.First(&row, "task_id = ? AND source_item_id = ?", h.task.ID, id).Error; err != nil || row.LastError != "" || row.SyncedFingerprint != row.TargetFingerprint {
					t.Fatal("confirmed write did not clear pending ledger state", row, err)
				}
			} else if recovered.DeletedCount != 1 || len(h.dest.docs) != 0 {
				t.Fatal("unconfirmed new document was orphaned", recovered)
			}
		})
	}
}
func steamHarness(t *testing.T) (*harness, *pollingSteam) {
	h := newHarness(t)
	old, _ := connectors.Source(steam.SourceType)
	source := &pollingSteam{fakeSource: h.src}
	connectors.RegisterSource(source)
	t.Cleanup(func() { connectors.RegisterSource(old) })
	h.task.SourceType = steam.SourceType
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	h.setDocs(map[string]fakeDoc{"steam_profile:player": {content: "Profile", rev: "1"}, "steam_game:player:620": {content: "Portal 2", rev: "1"}, "steam_game:player:400": {content: "Portal", rev: "1"}})
	return h, source
}

func TestSteamPollingLifecycleAndFullTimestamp(t *testing.T) {
	h, source := steamHarness(t)
	baseline := h.run("")
	expectStatus(t, baseline, models.RunSucceeded)
	if baseline.CreatedCount != 3 || !source.lastFull {
		t.Fatal("baseline", baseline)
	}
	before := h.state()
	second := h.run("")
	expectStatus(t, second, models.RunSucceeded)
	after := h.state()
	if second.SyncMode != models.SyncIncremental || source.lastFull || len(source.lastCursor) == 0 || second.UnchangedCount != 3 {
		t.Fatal("polling inventory forced full", second)
	}
	if !before.LastFullReconcileAt.Equal(*after.LastFullReconcileAt) {
		t.Fatal("ordinary inventory reset full reconcile timestamp")
	}
	full := h.run(models.SyncFull)
	expectStatus(t, full, models.RunSucceeded)
	if !source.lastFull || len(source.lastCursor) == 0 || full.UnchangedCount != 3 {
		t.Fatal("full inventory lost committed cursor", full)
	}
	reingest := h.run(models.SyncReingest)
	expectStatus(t, reingest, models.RunSucceeded)
	if reingest.UpdatedCount != 3 {
		t.Fatal("reingest skipped documents", reingest)
	}
	h.task.CustomTagsJSON = `["changed"]`
	h.task.CustomMetadataJSON = `{"project":"new"}`
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	changed := h.run("")
	expectStatus(t, changed, models.RunSucceeded)
	if changed.UpdatedCount != 3 {
		t.Fatal("tags and metadata were not reapplied", changed)
	}
	h.setDocs(map[string]fakeDoc{"steam_profile:player": {content: "Empty Profile", rev: "2"}})
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || !preview.Complete || preview.DeletedCount != 2 {
		t.Fatal("empty inventory preview", preview, err)
	}
	empty := h.run("")
	expectStatus(t, empty, models.RunSucceeded)
	if empty.DeletedCount != 2 || len(h.dest.docs) != 1 {
		t.Fatal("zero inventory protection was added", empty)
	}
}

func TestSteamFailuresBlockDeletionAndCursor(t *testing.T) {
	for _, scenario := range []string{"content", "destination", "partial", "source", "configuration"} {
		t.Run(scenario, func(t *testing.T) {
			h, source := steamHarness(t)
			expectStatus(t, h.run(""), models.RunSucceeded)
			before := h.state().CommittedCursorJSON
			h.setDocs(map[string]fakeDoc{"steam_profile:player": {content: "Changed", rev: "2"}})
			switch scenario {
			case "content":
				h.src.failIDs["steam_profile:player"] = true
			case "destination":
				h.dest.failOps = true
			case "partial":
				h.src.partial = true
			case "source":
				source.delayedError = errors.New("Steam request failed after emission")
			case "configuration":
				h.src.scanHook = func() { h.db.Model(&h.task).Update("config_revision", gorm.Expr("config_revision + 1")) }
			}
			if scenario != "destination" {
				preview, err := h.engine.DryRun(t.Context(), h.task.ID)
				if err != nil || preview.Complete || preview.DeletedCount != 0 || preview.Error == "" {
					t.Fatal("failed preview allowed deletion", preview, err)
				}
				// The fake source consumes its partial flag; restore it for the run.
				if scenario == "partial" {
					h.src.partial = true
				}
			}
			expectStatus(t, h.run(""), models.RunFailed)
			if h.state().CommittedCursorJSON != before || len(h.dest.deleted) != 0 {
				t.Fatal("failure deleted documents or advanced cursor")
			}
			if scenario == "content" || scenario == "destination" {
				h.src.failIDs = map[string]bool{}
				h.dest.failOps = false
				expectStatus(t, h.run(""), models.RunSucceeded)
				if !source.lastFull || len(h.dest.deleted) != 2 {
					t.Fatal("failed writes were not recovered by full reconciliation")
				}
			}
		})
	}
}

func TestSteamCanceledScanDoesNotCommit(t *testing.T) {
	h, source := steamHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state().CommittedCursorJSON
	ctx, cancel := context.WithCancel(t.Context())
	source.scanHook = cancel
	now := time.Now()
	run := models.TaskRun{ID: uuid.NewString(), TaskID: h.task.ID, Status: models.RunRunning, StartedAt: &now}
	if err := h.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	err := h.engine.Execute(ctx, run.ID, sync.RunOptions{})
	if !errors.Is(err, context.Canceled) || h.state().CommittedCursorJSON != before || len(h.dest.deleted) != 0 {
		t.Fatal("canceled Steam scan committed", err)
	}
}

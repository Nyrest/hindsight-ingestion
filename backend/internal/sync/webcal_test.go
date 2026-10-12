package sync_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/webcal"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

type calendarFeed struct {
	mu         stdsync.Mutex
	body, etag string
	status     int
	headers    []http.Header
	hook       func()
}

func calendarSnapshot(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + strings.Join(events, "\r\n") + "\r\nEND:VCALENDAR\r\n"
}

func calendarEvent(uid, description string) string {
	return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:" + description + "\r\nDTSTART:20261012T090000\r\nLAST-MODIFIED:20261001T000000Z\r\nEND:VEVENT"
}

func (f *calendarFeed) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headers = append(f.headers, r.Header.Clone())
	if f.hook != nil {
		f.hook()
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	w.Header().Set("ETag", f.etag)
	if f.etag != "" && r.Header.Get("If-None-Match") == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	fmt.Fprint(w, f.body)
}

func (f *calendarFeed) replace(body, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body, f.etag = body, etag
}

func webcalHarness(t *testing.T) (*harness, *calendarFeed) {
	t.Helper()
	h := newHarness(t)
	feed := &calendarFeed{body: calendarSnapshot(calendarEvent("a", "Alpha"), calendarEvent("b", "Beta")), etag: `"initial"`}
	server := httptest.NewServer(http.HandlerFunc(feed.serve))
	t.Cleanup(server.Close)
	cred := models.Credential{ID: uuid.NewString()}
	name := "calendar"
	if err := h.engine.Creds.Apply(&cred, credentials.Input{Name: &name, Type: "webcal", Config: map[string]any{"feedUrl": server.URL}}, true); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&cred).Error; err != nil {
		t.Fatal(err)
	}
	h.task.SourceType, h.task.SourceCredentialID = "webcal", cred.ID
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	return h, feed
}

func TestWebcalLifecycleAndDryRun(t *testing.T) {
	h, feed := webcalHarness(t)
	first := h.run("")
	expectStatus(t, first, models.RunSucceeded)
	if first.CreatedCount != 2 {
		t.Fatalf("initial: %+v", first)
	}
	retains := h.dest.retains
	second := h.run("")
	expectStatus(t, second, models.RunSucceeded)
	if second.SyncMode != models.SyncIncremental || second.DiscoveredCount != 0 || h.dest.retains != retains || len(h.dest.deleted) != 0 {
		t.Fatalf("304: %+v", second)
	}
	feed.replace(calendarSnapshot(calendarEvent("a", "Alpha edited"), calendarEvent("c", "Gamma")), `"changed"`)
	state := h.state()
	var runCountBefore int64
	h.db.Model(&models.TaskRun{}).Count(&runCountBefore)
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || !preview.Complete || preview.UpdatedCount != 1 || preview.CreatedCount != 1 || preview.DeletedCount != 1 {
		t.Fatalf("dry run: %+v %v", preview, err)
	}
	var runCountAfter int64
	h.db.Model(&models.TaskRun{}).Count(&runCountAfter)
	if h.state().CommittedCursorJSON != state.CommittedCursorJSON || h.state().ScanGeneration != state.ScanGeneration || h.dest.retains != retains || runCountBefore != runCountAfter {
		t.Fatal("dry run persisted state or wrote destination")
	}
	third := h.run("")
	expectStatus(t, third, models.RunSucceeded)
	if third.CreatedCount != 1 || third.UpdatedCount != 1 || third.DeletedCount != 1 {
		t.Fatalf("changed snapshot: %+v", third)
	}
	full := h.run(models.SyncFull)
	expectStatus(t, full, models.RunSucceeded)
	if full.DiscoveredCount != 2 || full.UnchangedCount != 2 {
		t.Fatalf("full used shortcut: %+v", full)
	}
	reingest := h.run(models.SyncReingest)
	expectStatus(t, reingest, models.RunSucceeded)
	if reingest.UpdatedCount != 2 {
		t.Fatalf("reingest: %+v", reingest)
	}
	feed.replace(calendarSnapshot(), `"empty"`)
	empty := h.run("")
	expectStatus(t, empty, models.RunSucceeded)
	if empty.DeletedCount != 2 || len(h.dest.docs) != 0 {
		t.Fatalf("empty snapshot did not delete all: %+v", empty)
	}
}

func TestWebcalInvalidSnapshotsDoNotDeleteOrCommit(t *testing.T) {
	for _, scenario := range []string{"missing UID", "conflict", "truncated", "HTTP 404", "component limit", "size limit", "invalid date"} {
		t.Run(scenario, func(t *testing.T) {
			h, feed := webcalHarness(t)
			expectStatus(t, h.run(""), models.RunSucceeded)
			before := h.state().CommittedCursorJSON
			good := calendarEvent("a", "Alpha edited")
			switch scenario {
			case "missing UID":
				feed.replace(calendarSnapshot(good, strings.Replace(calendarEvent("bad", "Invalid"), "UID:bad\r\n", "", 1)), `"broken"`)
			case "conflict":
				feed.replace(calendarSnapshot(good, calendarEvent("a", "Conflict")), `"broken"`)
			case "truncated":
				feed.replace(strings.TrimSuffix(calendarSnapshot(good), "END:VCALENDAR\r\n"), `"broken"`)
			case "HTTP 404":
				feed.status = 404
			case "component limit":
				h.db.Model(&h.task).Update("source_config_json", `{"maxComponents":1}`)
				feed.replace(calendarSnapshot(good), `"broken"`)
			case "size limit":
				h.db.Model(&h.task).Update("source_config_json", `{"maxFeedSizeMB":1}`)
				feed.replace(calendarSnapshot(strings.Replace(good, "END:VEVENT", "DESCRIPTION:"+strings.Repeat("x", 1<<20)+"\r\nEND:VEVENT", 1)), `"oversized"`)
			case "invalid date":
				feed.replace(calendarSnapshot(good, "BEGIN:VTODO\r\nUID:bad\r\nDUE:invalid\r\nEND:VTODO"), `"broken"`)
			}
			preview, err := h.engine.DryRun(t.Context(), h.task.ID)
			if err != nil || preview.Complete || preview.DeletedCount != 0 || preview.Error == "" {
				t.Fatalf("invalid preview: %+v %v", preview, err)
			}
			expectStatus(t, h.run(""), models.RunFailed)
			if h.state().CommittedCursorJSON != before || len(h.dest.deleted) != 0 || len(h.dest.docs) != 2 {
				t.Fatal("failed scan deleted documents or advanced cursor")
			}
		})
	}
}

func TestWebcalRetainFailureAndConfigurationRace(t *testing.T) {
	for _, scenario := range []string{"retain failure", "operation failure", "configuration race"} {
		t.Run(scenario, func(t *testing.T) {
			h, feed := webcalHarness(t)
			expectStatus(t, h.run(""), models.RunSucceeded)
			before := h.state().CommittedCursorJSON
			feed.replace(calendarSnapshot(calendarEvent("a", "Alpha edited")), `"changed"`)
			if scenario == "retain failure" {
				h.dest.failNext = true
			} else if scenario == "operation failure" {
				h.dest.failOps = true
			} else {
				feed.hook = func() { h.db.Model(&h.task).Update("config_revision", 2) }
			}
			expectStatus(t, h.run(""), models.RunFailed)
			if len(h.dest.deleted) != 0 || before != h.state().CommittedCursorJSON {
				t.Fatal("failed writes or old configuration deleted or committed")
			}
		})
	}
}

func TestWebcalTimezoneChangeAndTaskIsolation(t *testing.T) {
	h, _ := webcalHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	configuration, err := h.engine.Settings.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	configuration.Timezone = "Asia/Singapore"
	if err := h.engine.Settings.Put(t.Context(), configuration); err != nil {
		t.Fatal(err)
	}
	changed := h.run("")
	expectStatus(t, changed, models.RunSucceeded)
	if changed.UpdatedCount != 2 {
		t.Fatalf("timezone change hidden by 304: %+v", changed)
	}
	for _, retained := range h.dest.docs {
		if retained.Timestamp != "2026-10-12T01:00:00Z" {
			t.Fatalf("timestamp fell back to ModifiedAt: %s", retained.Timestamp)
		}
	}
	firstTask := h.task.ID
	h.task.ID = uuid.NewString()
	if err := h.db.Create(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	expectStatus(t, h.run(""), models.RunSucceeded)
	if len(h.dest.docs) != 4 {
		t.Fatal("same feed overwritten across tasks")
	}
	var ledger []models.TaskItem
	h.db.Where("task_id = ?", firstTask).Find(&ledger)
	for _, row := range ledger {
		if row.DestinationDocumentID == sync.DocumentID("webcal", h.task.ID, row.SourceItemID) {
			t.Fatal("document IDs not task isolated")
		}
	}
}

func TestWebcalUnknownTimestampIsExplicit(t *testing.T) {
	h, feed := webcalHarness(t)
	feed.replace(calendarSnapshot("BEGIN:VEVENT\r\nUID:unknown\r\nDTSTART;TZID=Custom/Zone:20261012T090000\r\nLAST-MODIFIED:20261001T000000Z\r\nEND:VEVENT"), `"unknown"`)
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || !preview.Complete || len(preview.Items) != 1 || len(preview.Items[0].Warnings) == 0 {
		t.Fatalf("timezone warning missing from dry run: %+v %v", preview, err)
	}
	expectStatus(t, h.run(""), models.RunSucceeded)
	for _, retained := range h.dest.docs {
		encoded, err := json.Marshal(retained)
		if err != nil || !strings.Contains(string(encoded), `"timestamp":"unset"`) {
			t.Fatalf("actual retain JSON does not contain unset: %s %v", encoded, err)
		}
	}
	state := h.state()
	if state.LastFullReconcileAt == nil || time.Since(*state.LastFullReconcileAt) > time.Minute {
		t.Fatal("custom timezone should still be a complete snapshot")
	}
}

func TestWebcalDryRunFencesConfigurationChanges(t *testing.T) {
	h, feed := webcalHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	feed.replace(calendarSnapshot(), `"empty"`)
	feed.hook = func() { h.db.Model(&h.task).Update("config_revision", gorm.Expr("config_revision + 1")) }
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || preview.Complete || preview.DeletedCount != 0 || !strings.Contains(preview.Error, "configuration changed") {
		t.Fatalf("stale configuration previewed deletion: %+v %v", preview, err)
	}
}

func TestWebcalFailedReingestRetriesDespiteUnchangedETag(t *testing.T) {
	h, feed := webcalHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state().CommittedCursorJSON
	h.dest.failNext = true
	expectStatus(t, h.run(models.SyncReingest), models.RunFailed)
	if h.state().CommittedCursorJSON != before {
		t.Fatal("failed reingest advanced cursor")
	}
	retry := h.run("")
	expectStatus(t, retry, models.RunSucceeded)
	if retry.SyncMode != models.SyncFull || retry.UpdatedCount != 2 || feed.headers[len(feed.headers)-1].Get("If-None-Match") != "" {
		t.Fatalf("failed reingest hidden by 304: %+v", retry)
	}
}

func TestWebcalMassDisappearanceDeletesWithoutThreshold(t *testing.T) {
	h, feed := webcalHarness(t)
	var events []string
	for index := range 100 {
		events = append(events, calendarEvent(fmt.Sprintf("mass-%d", index), "Meeting"))
	}
	feed.replace(calendarSnapshot(events...), `"large"`)
	expectStatus(t, h.run(""), models.RunSucceeded)
	feed.replace(calendarSnapshot(events[0]), `"one-left"`)
	run := h.run("")
	expectStatus(t, run, models.RunSucceeded)
	if run.DeletedCount != 99 || len(h.dest.docs) != 1 {
		t.Fatalf("deletion threshold was applied: %+v", run)
	}
}

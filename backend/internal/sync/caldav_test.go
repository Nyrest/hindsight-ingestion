package sync_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/caldav"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
	"github.com/google/uuid"
)

func caldavReport(calendars ...string) string {
	var body strings.Builder
	body.WriteString(`<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">`)
	for index, calendar := range calendars {
		fmt.Fprintf(&body, `<d:response><d:href>/cal/%d.ics</d:href><d:propstat><d:prop><c:calendar-data>`, index)
		if err := xml.EscapeText(&body, []byte(calendar)); err != nil {
			panic(err)
		}
		body.WriteString(`</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	}
	body.WriteString(`</d:multistatus>`)
	return body.String()
}

func caldavHarness(t *testing.T) (*harness, *calendarFeed) {
	t.Helper()
	h := newHarness(t)
	feed := &calendarFeed{body: caldavReport(calendarSnapshot(calendarEvent("a", "Alpha")), calendarSnapshot(calendarEvent("b", "Beta")))}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			w.WriteHeader(207)
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>/cal/</d:href><d:propstat><d:prop><d:resourcetype><c:calendar/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
			return
		}
		feed.mu.Lock()
		defer feed.mu.Unlock()
		if feed.hook != nil {
			feed.hook()
		}
		w.WriteHeader(207)
		fmt.Fprint(w, feed.body)
	}))
	t.Cleanup(server.Close)
	cred := models.Credential{ID: uuid.NewString()}
	name := "CalDAV"
	if err := h.engine.Creds.Apply(&cred, credentials.Input{Name: &name, Type: "caldav", Config: map[string]any{"baseUrl": server.URL}}, true); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&cred).Error; err != nil {
		t.Fatal(err)
	}
	h.task.SourceType, h.task.SourceCredentialID, h.task.SourceConfigJSON = "caldav", cred.ID, `{"calendarUrl":"/cal/"}`
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	return h, feed
}

func TestCalDAVLifecycleAndDryRun(t *testing.T) {
	h, feed := caldavHarness(t)
	first := h.run("")
	expectStatus(t, first, models.RunSucceeded)
	if first.CreatedCount != 2 {
		t.Fatalf("initial: %+v", first)
	}
	retains := h.dest.retains
	unchanged := h.run("")
	expectStatus(t, unchanged, models.RunSucceeded)
	if unchanged.UnchangedCount != 2 || h.dest.retains != retains {
		t.Fatalf("unchanged: %+v", unchanged)
	}
	feed.replace(caldavReport(calendarSnapshot(calendarEvent("a", "Edited")), calendarSnapshot(calendarEvent("c", "Gamma"))), "")
	state := h.state()
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || !preview.Complete || preview.UpdatedCount != 1 || preview.CreatedCount != 1 || preview.DeletedCount != 1 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if h.state().ScanGeneration != state.ScanGeneration || h.dest.retains != retains || len(h.dest.deleted) != 0 {
		t.Fatal("dry run wrote state or destination")
	}
	changed := h.run("")
	expectStatus(t, changed, models.RunSucceeded)
	if changed.CreatedCount != 1 || changed.UpdatedCount != 1 || changed.DeletedCount != 1 {
		t.Fatalf("changed: %+v", changed)
	}
	feed.replace(caldavReport(), "")
	empty := h.run("")
	expectStatus(t, empty, models.RunSucceeded)
	if empty.DeletedCount != 2 || len(h.dest.docs) != 0 {
		t.Fatalf("empty: %+v", empty)
	}
}

func TestCalDAVFailuresPreventDeletionAndStateCommit(t *testing.T) {
	for _, scenario := range []string{"partial response", "retain failure", "operation failure", "configuration race"} {
		t.Run(scenario, func(t *testing.T) {
			h, feed := caldavHarness(t)
			expectStatus(t, h.run(""), models.RunSucceeded)
			before := h.state()
			feed.replace(caldavReport(calendarSnapshot(calendarEvent("a", "Edited"))), "")
			switch scenario {
			case "partial response":
				feed.replace(strings.TrimSuffix(feed.body, "</d:multistatus>"), "")
			case "retain failure":
				h.dest.failNext = true
			case "operation failure":
				h.dest.failOps = true
			case "configuration race":
				feed.hook = func() { h.db.Model(&h.task).Update("config_revision", 2) }
			}
			expectStatus(t, h.run(""), models.RunFailed)
			if len(h.dest.deleted) != 0 || !h.state().LastSuccessAt.Equal(*before.LastSuccessAt) {
				t.Fatal("failed scan deleted data or committed state")
			}
		})
	}
}

func TestCalDAVFailedReingestRetriesUnchangedItems(t *testing.T) {
	h, _ := caldavHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	h.dest.failNext = true
	expectStatus(t, h.run(models.SyncReingest), models.RunFailed)
	retried := h.run("")
	expectStatus(t, retried, models.RunSucceeded)
	if retried.UpdatedCount != 2 {
		t.Fatalf("unchanged items did not retry: %+v", retried)
	}
}

func TestCalDAVLostResponseRecovery(t *testing.T) {
	for _, scenario := range []string{"reverted content", "disappeared new document"} {
		t.Run(scenario, func(t *testing.T) {
			h, feed := caldavHarness(t)
			original := caldavReport(calendarSnapshot(calendarEvent("a", "Alpha")))
			if scenario == "reverted content" {
				feed.replace(original, "")
				expectStatus(t, h.run(""), models.RunSucceeded)
			}
			feed.replace(caldavReport(calendarSnapshot(calendarEvent("a", "Edited"))), "")
			h.dest.loseResponse = true
			failed := h.run("")
			expectStatus(t, failed, models.RunFailed)
			var intent models.HindsightOperation
			if err := h.db.First(&intent, "type = ? AND run_id = ?", "retain_intent", failed.ID).Error; err != nil {
				t.Fatal("lost response left no durable retain operation to await", err)
			}
			if intent.RemoteOperationID == "" || intent.Status != "submitted" {
				t.Fatal("unconfirmed retain operation was not preserved", intent)
			}
			var row models.TaskItem
			if err := h.db.First(&row, "task_id = ?", h.task.ID).Error; err != nil || !row.DestinationPresent || row.LastError == "" {
				t.Fatal("unconfirmed destination document was not preserved", row, err)
			}
			h.dest.loseResponse = false
			feed.replace(caldavReport(), "")
			if scenario == "reverted content" {
				feed.replace(original, "")
			}
			var scans atomic.Int32
			feed.hook = func() { scans.Add(1) }
			before, retains := h.state(), h.dest.retains
			run := models.TaskRun{ID: uuid.NewString(), TaskID: h.task.ID, Status: models.RunRunning}
			if err := h.db.Create(&run).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if err := h.engine.Execute(ctx, run.ID, sync.RunOptions{}); err == nil || scans.Load() != 0 || h.dest.retains != retains || len(h.dest.deleted) != 0 || h.state().ScanGeneration != before.ScanGeneration {
				t.Fatal("unsettled prior retain did not block scanning, writes and deletion", err)
			}
			h.dest.mu.Lock()
			h.dest.opStatus[intent.RemoteOperationID] = "completed"
			h.dest.mu.Unlock()
			recovered := h.run("")
			expectStatus(t, recovered, models.RunSucceeded)
			if scenario == "reverted content" {
				if recovered.UpdatedCount != 1 || len(h.dest.docs) != 1 {
					t.Fatal("reverted content was not restored", recovered)
				}
				for _, document := range h.dest.docs {
					if !strings.Contains(document.Content, "Alpha") || strings.Contains(document.Content, "Edited") {
						t.Fatal("unconfirmed write left stale destination content", document.Content)
					}
				}
			} else {
				var count int64
				h.db.Model(&models.TaskItem{}).Where("task_id = ?", h.task.ID).Count(&count)
				if recovered.DeletedCount != 1 || len(h.dest.docs) != 0 || count != 0 {
					t.Fatal("settled write was not deleted with the empty calendar", recovered, count)
				}
			}
		})
	}
}

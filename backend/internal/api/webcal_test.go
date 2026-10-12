package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func webcalAPICredential(t *testing.T, e *env, typ string, cfg map[string]any) string {
	t.Helper()
	var out map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{"name": typ, "type": typ, "config": cfg}, &out); code != 201 {
		t.Fatalf("%d %+v", code, out)
	}
	if typ == "webcal" && out["config"].(map[string]any)["feedUrl"] != credentials.Mask {
		t.Fatal("subscription URL was not masked", out)
	}
	return out["id"].(string)
}

func webcalAPITask(t *testing.T, e *env, source string) string {
	t.Helper()
	dest := webcalAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	var out map[string]any
	if code := e.do("POST", "/api/tasks", map[string]any{
		"name": "Calendar", "sourceType": "webcal", "sourceCredentialId": source, "sourceConfig": map[string]any{},
		"destinationCredentialId": dest, "destinationBankId": "dest", "cronExpression": "",
	}, &out); code != 201 {
		t.Fatalf("%d %+v", code, out)
	}
	return out["id"].(string)
}

func TestWebcalCredentialEncryptionAndRotation(t *testing.T) {
	e := newEnv(t)
	address := "https://calendar.example/private?token=secret-calendar-token"
	credID := webcalAPICredential(t, e, "webcal", map[string]any{"feedUrl": address})
	model, err := e.creds.Get(context.Background(), credID)
	if err != nil || strings.Contains(model.ConfigJSON, address) || strings.Contains(model.EncryptedSecret, "secret-calendar-token") || model.EncryptedSecret == "" {
		t.Fatal("URL was not encrypted", err)
	}
	decrypted, err := e.creds.Load(context.Background(), credID)
	if err != nil || decrypted.String("feedUrl") != address {
		t.Fatal("URL did not round-trip", err)
	}
	taskID := webcalAPITask(t, e, credID)
	var initial models.Task
	e.db.First(&initial, "id = ?", taskID)
	now := time.Now().UTC()
	seed := func() {
		t.Helper()
		if err := e.db.Model(&models.TaskState{}).Where("task_id = ?", taskID).Updates(map[string]any{"committed_cursor_json": `{"etag":"old"}`, "last_full_reconcile_at": now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed()
	for index, change := range []struct {
		body  map[string]any
		reset bool
	}{
		{map[string]any{"name": "Renamed", "config": map[string]any{"feedUrl": credentials.Mask}}, false},
		{map[string]any{"config": map[string]any{"feedUrl": address}}, false},
		{map[string]any{"config": map[string]any{"feedUrl": "https://calendar.example/rotated?token=new"}}, true},
		{map[string]any{"customHeaders": map[string]string{"X-Token": "new-token"}}, true},
		{map[string]any{"customHeaders": map[string]string{"X-Token": credentials.Mask}}, false},
		{map[string]any{"customHeaders": map[string]string{"x-token": credentials.Mask}}, false},
	} {
		seed()
		if code := e.do("PATCH", "/api/credentials/"+credID, change.body, nil); code != 200 {
			t.Fatal(index, code)
		}
		var task models.Task
		var state models.TaskState
		e.db.First(&task, "id = ?", taskID)
		e.db.First(&state, "task_id = ?", taskID)
		if change.reset {
			if task.ConfigRevision != initial.ConfigRevision+1 || !task.ReconcileRequired || state.CommittedCursorJSON != "" || state.LastFullReconcileAt != nil {
				t.Fatalf("rotation %d did not reset atomically: %+v %+v", index, task, state)
			}
		} else if task.ConfigRevision != initial.ConfigRevision || state.CommittedCursorJSON == "" || state.LastFullReconcileAt == nil {
			t.Fatalf("unchanged credential %d reset state", index)
		}
		initial = task
	}
}

func TestWebcalRetainTimestampRequestJSON(t *testing.T) {
	e := newEnv(t)
	components := []struct{ kind, fields, expected string }{
		{"VEVENT", "DTSTART:20301012T090000Z", "2030-10-12T09:00:00Z"},
		{"VEVENT", "DTSTART;TZID=Asia/Tokyo:20261012T090000", "2026-10-12T00:00:00Z"},
		{"VEVENT", "DTSTART;VALUE=DATE:20261012\r\nDTEND;VALUE=DATE:20261013", "2026-10-11T16:00:00Z"},
		{"VEVENT", "DTSTART:20261012T090000", "2026-10-12T01:00:00Z"},
		{"VEVENT", "DTSTART;TZID=America/New_York:20260308T023000", "2026-03-08T07:30:00Z"},
		{"VTODO", "COMPLETED:20261012T120000Z\r\nDUE:20261013T120000Z\r\nDTSTART:20261010T120000Z", "2026-10-12T12:00:00Z"},
		{"VTODO", "DUE:20261013T120000Z\r\nDTSTART:20261010T120000Z", "2026-10-13T12:00:00Z"},
		{"VTODO", "DTSTART:20261012T090000", "2026-10-12T01:00:00Z"},
		{"VTODO", "DUE;TZID=Custom/Missing:20261013T120000\r\nDTSTART:20261010T120000Z", "unset"},
		{"VEVENT", "DTSTART:20261012T090000Z\r\nRRULE:FREQ=WEEKLY", "unset"},
		{"VEVENT", "DTSTART;TZID=Custom/Missing:20261012T090000\r\nLAST-MODIFIED:20261012T100000Z", "unset"},
		{"VTODO", "LAST-MODIFIED:20261012T100000Z", "unset"},
	}
	var feed strings.Builder
	feed.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n")
	for index, component := range components {
		fmt.Fprintf(&feed, "BEGIN:%s\r\nUID:json-%d\r\nSUMMARY:Timestamp %d\r\n%s\r\nEND:%s\r\n", component.kind, index, index, component.fields, component.kind)
	}
	feed.WriteString("END:VCALENDAR\r\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, feed.String()) }))
	t.Cleanup(server.Close)
	if code := e.do("PATCH", "/api/settings", map[string]any{"timezone": "Asia/Singapore"}, nil); code != 200 {
		t.Fatal(code)
	}
	credID := webcalAPICredential(t, e, "webcal", map[string]any{"feedUrl": server.URL})
	taskID := webcalAPITask(t, e, credID)
	runID, err := e.runs.Start(taskID, models.TriggerManual, models.SyncIncremental, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var run models.TaskRun
	e.db.First(&run, "id = ?", runID)
	if run.Status != models.RunSucceeded || run.CreatedCount != len(components) {
		t.Fatalf("run failed: %+v", run)
	}
	e.hs.mu.Lock()
	defer e.hs.mu.Unlock()
	if len(e.hs.docs) != len(components) {
		t.Fatal("missing retained documents", len(e.hs.docs))
	}
	for _, item := range e.hs.docs {
		metadata := item["metadata"].(map[string]any)
		var index int
		if _, err := fmt.Sscanf(metadata["ical_uid"].(string), "json-%d", &index); err != nil {
			t.Fatal(err)
		}
		if stamp, exists := item["timestamp"]; !exists || stamp != components[index].expected {
			t.Fatalf("document %d outbound JSON timestamp = %v, want %s", index, stamp, components[index].expected)
		}
	}
}

func TestWebcalTimezoneResetsInventoryAndHourlyDefault(t *testing.T) {
	e := newEnv(t)
	credID := webcalAPICredential(t, e, "webcal", map[string]any{"feedUrl": "https://calendar.example/calendar.ics"})
	destID := webcalAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	var task map[string]any
	if code := e.do("POST", "/api/tasks", map[string]any{
		"name": "Calendar", "sourceType": "webcal", "sourceCredentialId": credID,
		"destinationCredentialId": destID, "destinationBankId": "dest", "enabled": false,
	}, &task); code != 201 || task["cronExpression"] != "0 * * * *" {
		t.Fatalf("hourly default: %d %+v", code, task)
	}
	id := task["id"].(string)
	var before models.Task
	e.db.First(&before, "id = ?", id)
	e.db.Model(&models.TaskState{}).Where("task_id = ?", id).Update("committed_cursor_json", `{"etag":"old"}`)
	if code := e.do("PATCH", "/api/settings", map[string]any{"timezone": "Asia/Singapore"}, nil); code != 200 {
		t.Fatal(code)
	}
	var after models.Task
	var state models.TaskState
	e.db.First(&after, "id = ?", id)
	e.db.First(&state, "task_id = ?", id)
	if after.ConfigRevision != before.ConfigRevision+1 || !after.ReconcileRequired || state.CommittedCursorJSON != "" {
		t.Fatal("timezone did not reset WebCAL inventory and fence old runs")
	}
	if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"cronExpression": ""}, nil); code != 200 {
		t.Fatal(code)
	}
	e.db.First(&after, "id = ?", id)
	if after.CronExpression != "" {
		t.Fatal("explicit manual-only schedule was overwritten")
	}
}

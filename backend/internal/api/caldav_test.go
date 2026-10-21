package api_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestCalDAVCredentialTaskAndSettings(t *testing.T) {
	e := newEnv(t)
	var out map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{"name": "CalDAV", "type": "caldav", "config": map[string]any{"baseUrl": "https://calendar.example/", "username": "alice", "password": "app-secret", "bearerToken": "token-secret"}}, &out); code != 201 {
		t.Fatal(code, out)
	}
	credID := out["id"].(string)
	for _, field := range []string{"password", "bearerToken"} {
		if out["config"].(map[string]any)[field] != credentials.Mask {
			t.Fatal("secret was not masked", field)
		}
	}
	stored, err := e.creds.Get(t.Context(), credID)
	if err != nil || stored.EncryptedSecret == "" || strings.Contains(stored.ConfigJSON, "secret") || strings.Contains(stored.EncryptedSecret, "app-secret") {
		t.Fatal("secrets were not encrypted", err)
	}
	dest := webcalAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	body := map[string]any{"name": "CalDAV", "sourceType": "caldav", "sourceCredentialId": credID, "sourceConfig": map[string]any{"calendarUrl": "/home/work/"}, "destinationCredentialId": dest, "destinationBankId": "dest"}
	if code := e.do("POST", "/api/tasks", body, &out); code != 201 {
		t.Fatal(code, out)
	}
	taskID := out["id"].(string)
	var task models.Task
	e.db.First(&task, "id = ?", taskID)
	if task.CronExpression != "0 * * * *" {
		t.Fatal("missing hourly default")
	}
	seed := func() {
		t.Helper()
		if err := e.db.Model(&models.TaskState{}).Where("task_id = ?", taskID).Updates(map[string]any{"committed_cursor_json": `{"baseline":1}`, "last_full_reconcile_at": time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed()
	if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"name": "Renamed", "config": map[string]any{"password": credentials.Mask, "bearerToken": credentials.Mask}}, nil); code != 200 {
		t.Fatal(code)
	}
	var state models.TaskState
	e.db.First(&state, "task_id = ?", taskID)
	if state.CommittedCursorJSON == "" {
		t.Fatal("masked secret reset state")
	}
	for _, cfg := range []map[string]any{{"password": "rotated"}, {"username": "bob"}, {"baseUrl": "https://new.example/"}} {
		seed()
		if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"config": cfg}, nil); code != 200 {
			t.Fatal(code)
		}
		state = models.TaskState{}
		e.db.First(&state, "task_id = ?", taskID)
		var changed models.Task
		e.db.First(&changed, "id = ?", taskID)
		if state.CommittedCursorJSON != "" || state.LastFullReconcileAt != nil || changed.ConfigRevision <= task.ConfigRevision || !changed.ReconcileRequired {
			t.Fatal("credential change did not fence task", cfg)
		}
	}
	seed()
	if code := e.do("PATCH", "/api/settings", map[string]any{"timezone": "Asia/Tokyo"}, nil); code != 200 {
		t.Fatal(code)
	}
	state = models.TaskState{}
	e.db.First(&state, "task_id = ?", taskID)
	if state.CommittedCursorJSON != "" || state.LastFullReconcileAt != nil {
		t.Fatal("timezone change did not reset calendar baseline")
	}
	body["cronExpression"] = ""
	if code := e.do("POST", "/api/tasks", body, &out); code != 201 || out["cronExpression"] != "" {
		t.Fatal("manual schedule ignored", code, out)
	}
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"sourceConfig": map[string]any{}}, nil); code != 422 {
		t.Fatal("missing calendar URL accepted", code)
	}
}

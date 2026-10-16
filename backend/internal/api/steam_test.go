package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestSteamCredentialTaskAndCacheLifecycle(t *testing.T) {
	e := newEnv(t)
	key := strings.Repeat("a", 32)
	var out map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{"name": "Steam", "type": "steam", "config": map[string]any{"apiKey": key}}, &out); code != 201 {
		t.Fatal(code, out)
	}
	credID := out["id"].(string)
	if out["config"].(map[string]any)["apiKey"] != credentials.Mask {
		t.Fatal("API key was not masked")
	}
	stored, err := e.creds.Get(context.Background(), credID)
	if err != nil || stored.EncryptedSecret == "" || strings.Contains(stored.ConfigJSON, key) || strings.Contains(stored.EncryptedSecret, key) {
		t.Fatal("API key was not encrypted", err)
	}
	dest := webcalAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	if code := e.do("POST", "/api/tasks", map[string]any{"name": "Steam", "sourceType": "steam", "sourceCredentialId": credID, "sourceConfig": map[string]any{"steamId": "76561198188596121"}, "destinationCredentialId": dest, "destinationBankId": "dest"}, &out); code != 201 {
		t.Fatal(code, out)
	}
	taskID := out["id"].(string)
	var task models.Task
	e.db.First(&task, "id = ?", taskID)
	if task.CronExpression != "0 * * * *" {
		t.Fatal("missing default schedule")
	}
	now := time.Now().UTC()
	seed := func() {
		t.Helper()
		if err := e.db.Model(&models.TaskState{}).Where("task_id = ?", taskID).Updates(map[string]any{"committed_cursor_json": `{"version":1}`, "last_full_reconcile_at": now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed()
	if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"name": "Renamed", "config": map[string]any{"apiKey": credentials.Mask}}, nil); code != 200 {
		t.Fatal(code)
	}
	var state models.TaskState
	e.db.First(&state, "task_id = ?", taskID)
	if state.CommittedCursorJSON == "" {
		t.Fatal("masked key reset cursor")
	}
	if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"config": map[string]any{"apiKey": strings.Repeat("b", 32)}}, nil); code != 200 {
		t.Fatal(code)
	}
	state = models.TaskState{}
	e.db.First(&state, "task_id = ?", taskID)
	var rotated models.Task
	e.db.First(&rotated, "id = ?", taskID)
	if state.CommittedCursorJSON != "" || state.LastFullReconcileAt != nil || rotated.ConfigRevision != task.ConfigRevision+1 || !rotated.ReconcileRequired {
		t.Fatal("key rotation did not fence and reset task", state, rotated)
	}
	for _, cfg := range []map[string]any{{"steamId": "76561198188596121", "language": "schinese"}, {"steamId": "76561197960434622", "language": "schinese"}} {
		seed()
		if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"sourceConfig": cfg}, nil); code != 200 {
			t.Fatal(code)
		}
		state = models.TaskState{}
		e.db.First(&state, "task_id = ?", taskID)
		if state.CommittedCursorJSON != "" || state.LastFullReconcileAt != nil {
			t.Fatal("source change did not rebaseline")
		}
	}
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"sourceConfig": map[string]any{"steamId": 76561198188596121.0}}, nil); code != 422 {
		t.Fatal("numeric SteamID accepted", code)
	}
	if err := e.db.Create(&models.SteamPlayerSnapshot{TaskID: taskID, CredentialKey: "key", SteamID: "76561198188596121", Language: "english", AppID: 620, PayloadJSON: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	if code := e.do("DELETE", "/api/tasks/"+taskID, nil, nil); code != 204 {
		t.Fatal("task deletion", code)
	}
	var count int64
	e.db.Model(&models.SteamPlayerSnapshot{}).Where("task_id = ?", taskID).Count(&count)
	if count != 0 {
		t.Fatal("deleted task retained player snapshots")
	}
}

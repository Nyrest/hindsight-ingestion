package api_test

import (
	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/email"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"strings"
	"testing"
	"time"
)

func TestEmailCredentialValidationRotationAndStaleEditor(t *testing.T) {
	e := newEnv(t)
	connectors.RegisterSource(&email.Connector{DB: e.db})
	var out map[string]any
	cfg := map[string]any{"host": "imap.example.com", "username": "user@example.com", "password": "secret-password"}
	if code := e.do("POST", "/api/credentials", map[string]any{"type": "imap", "name": "Mail", "config": cfg}, &out); code != 201 || out["config"].(map[string]any)["password"] != credentials.Mask {
		t.Fatal(code, out)
	}
	credID := out["id"].(string)
	credential, err := e.creds.Get(t.Context(), credID)
	if err != nil || strings.Contains(credential.ConfigJSON, "secret-password") || strings.Contains(credential.EncryptedSecret, "secret-password") {
		t.Fatal("password was not encrypted", err)
	}
	for key, value := range map[string]any{"port": -1, "host": "imap.example.com:993", "security": "none", "commandTimeout": 1.5} {
		if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"config": map[string]any{key: value}}, nil); code != 422 {
			t.Fatalf("invalid %s: %d", key, code)
		}
	}
	if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"customHeaders": map[string]string{"X-Token": "secret"}}, nil); code != 422 {
		t.Fatal("IMAP accepted HTTP headers", code)
	}
	dest := sqlAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	if code := e.do("POST", "/api/tasks", map[string]any{"name": "Email", "enabled": false, "sourceType": "email", "sourceCredentialId": credID, "sourceConfig": map[string]any{"folders": []string{"INBOX", "Sent"}}, "destinationCredentialId": dest, "destinationBankId": "bank"}, &out); code != 201 || out["cronExpression"] != "0 * * * *" {
		t.Fatal(code, out)
	}
	taskID := out["id"].(string)
	var task models.Task
	e.db.First(&task, "id = ?", taskID)
	now := time.Now()
	e.db.Model(&models.TaskState{}).Where("task_id = ?", taskID).Updates(map[string]any{"committed_cursor_json": "old", "last_full_reconcile_at": now})
	if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"config": map[string]any{"password": "rotated"}}, nil); code != 200 {
		t.Fatal(code)
	}
	var state models.TaskState
	e.db.First(&state, "task_id = ?", taskID)
	if state.CommittedCursorJSON != "old" {
		t.Fatal("password rotation reset mailbox identities")
	}
	if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"config": map[string]any{"username": "other@example.com"}}, nil); code != 200 {
		t.Fatal(code)
	}
	e.db.First(&state, "task_id = ?", taskID)
	var changed models.Task
	e.db.First(&changed, "id = ?", taskID)
	if state.CommittedCursorJSON != "" || changed.ConfigRevision != task.ConfigRevision+1 || !changed.ReconcileRequired {
		t.Fatal("account rotation did not reset baseline atomically")
	}
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"expectedConfigRevision": task.ConfigRevision, "name": "Stale form"}, nil); code != 409 {
		t.Fatal("stale editor overwrote rotated account", code)
	}
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"expectedConfigRevision": changed.ConfigRevision, "name": "Current form", "cronExpression": ""}, nil); code != 200 {
		t.Fatal(code)
	}
	e.db.First(&changed, "id = ?", taskID)
	if changed.CronExpression != "" || !changed.ReconcileRequired {
		t.Fatal("save overwrote reconciliation intent or manual schedule")
	}
	if code := e.do("DELETE", "/api/tasks/"+taskID, nil, nil); code != 204 {
		t.Fatal(code)
	}
}

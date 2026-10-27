package api_test

import (
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestNotionModesAndUnlimitedDepth(t *testing.T) {
	e := newEnv(t)
	source := webcalAPICredential(t, e, "notion", map[string]any{"token": "test-token"})
	dest := webcalAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	body := map[string]any{"name": "Notion", "enabled": false, "sourceType": "notion", "sourceCredentialId": source,
		"sourceConfig": map[string]any{"dataSourceId": "ds"}, "destinationCredentialId": dest, "destinationBankId": "dest"}
	var out map[string]any
	if code := e.do("POST", "/api/tasks", body, &out); code != 201 {
		t.Fatal(code, out)
	}
	cfg := out["sourceConfig"].(map[string]any)
	if cfg["mode"] != "database" || cfg["includeSubpages"] != false || cfg["maxDepth"] != float64(3) {
		t.Fatal("legacy defaults", cfg)
	}
	id := out["id"].(string)
	seed := func() {
		t.Helper()
		if err := e.db.Model(&models.TaskState{}).Where("task_id = ?", id).Updates(map[string]any{"committed_cursor_json": `{"highWater":"2026-10-01T00:00:00Z"}`, "last_full_reconcile_at": time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, config := range []map[string]any{
		{"mode": "pages", "pageIds": []string{"a", "b"}, "includeSubpages": true, "maxDepth": nil},
		{"mode": "pages", "pageIds": []string{"b"}, "includeSubpages": true, "maxDepth": 0},
		{"mode": "database", "dataSourceId": "ds", "includeSubpages": true, "maxDepth": 2},
		{"dataSourceId": "ds"},
	} {
		seed()
		if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"sourceConfig": config}, &out); code != 200 {
			t.Fatal(code, out)
		}
		cfg = out["sourceConfig"].(map[string]any)
		if raw, ok := config["maxDepth"]; ok && raw == nil {
			if depth, exists := cfg["maxDepth"]; !exists || depth != nil {
				t.Fatal("unlimited was replaced by the default", cfg)
			}
			var saved map[string]any
			if code := e.do("GET", "/api/tasks/"+id, nil, &saved); code != 200 || saved["sourceConfig"].(map[string]any)["maxDepth"] != nil {
				t.Fatal("unlimited did not survive reload", saved)
			}
		}
		var state models.TaskState
		e.db.First(&state, "task_id = ?", id)
		if state.CommittedCursorJSON != "" || state.LastFullReconcileAt != nil || out["reconcileRequired"] != true {
			t.Fatal("scope change did not rebaseline", state, out)
		}
	}
	for _, config := range []map[string]any{{"mode": "pages", "pageIds": []string{}}, {"mode": "database"}, {"dataSourceId": "ds", "maxDepth": -1}, {"dataSourceId": "ds", "maxDepth": 0.5}} {
		if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"sourceConfig": config}, nil); code != 422 {
			t.Fatal("invalid config accepted", config, code)
		}
	}
}

package api_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManualOnlyTasks(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manual.txt"), []byte("manual content"), 0600); err != nil {
		t.Fatal(err)
	}
	var src, dest map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{
		"name": "files", "type": "filesystem", "config": map[string]any{"rootPath": root},
	}, &src); code != 201 {
		t.Fatal(code, src)
	}
	if code := e.do("POST", "/api/credentials", map[string]any{
		"name": "destination", "type": "hindsight", "config": map[string]any{"baseUrl": e.hsrv.URL},
	}, &dest); code != 201 {
		t.Fatal(code, dest)
	}
	for _, expr := range []any{nil, "", " \t "} {
		body := map[string]any{
			"name": "manual", "sourceType": "filesystem", "sourceCredentialId": src["id"],
			"sourceConfig":            map[string]any{"folder": "."},
			"destinationCredentialId": dest["id"], "destinationBankId": "dest",
		}
		if expr != nil {
			body["cronExpression"] = expr
		}
		var task map[string]any
		if code := e.do("POST", "/api/tasks", body, &task); code != 201 {
			t.Fatalf("create with cron %v: %d %+v", expr, code, task)
		}
		id := task["id"].(string)
		assertManual := func() {
			t.Helper()
			if code := e.do("GET", "/api/tasks/"+id, nil, &task); code != 200 {
				t.Fatal(code, task)
			}
			if task["cronExpression"] != "" || task["enabled"] != true || task["nextRunAt"] != nil {
				t.Fatalf("expected enabled manual-only task: %+v", task)
			}
		}
		assertManual()
		var started map[string]string
		if code := e.do("POST", "/api/tasks/"+id+"/run", nil, &started); code != 202 {
			t.Fatal("manual run", code, started)
		}
		waitIdle(t, e.runs, id)
		var run map[string]any
		if code := e.do("GET", "/api/runs/"+started["runId"], nil, &run); code != 200 || run["status"] != "succeeded" || run["triggerType"] != "manual" {
			t.Fatal("manual run result", code, run)
		}
		if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"cronExpression": "0 3 * * *"}, &task); code != 200 || task["nextRunAt"] == nil {
			t.Fatal("add cron", code, task)
		}
		if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"cronExpression": "bad"}, &task); code != 422 {
			t.Fatal("invalid cron", code, task)
		}
		if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"cronExpression": " \t "}, &task); code != 200 {
			t.Fatal("clear cron", code, task)
		}
		assertManual()
		if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"enabled": false}, &task); code != 200 {
			t.Fatal(code, task)
		}
		if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"enabled": true}, &task); code != 200 {
			t.Fatal(code, task)
		}
		assertManual()
		if err := e.sched.LoadTasks(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertManual()
		if code := e.do("PATCH", "/api/settings", map[string]any{"timezone": "Asia/Tokyo"}, nil); code != 200 {
			t.Fatal("change timezone", code)
		}
		assertManual()
	}
	for _, expr := range []string{"", " \t "} {
		var preview map[string]any
		if code := e.do("POST", "/api/tasks/validate-cron", map[string]any{"cronExpression": expr}, &preview); code != 200 || preview["valid"] != true || len(preview["nextRuns"].([]any)) != 0 {
			t.Fatal("manual-only preview", code, preview)
		}
	}
}

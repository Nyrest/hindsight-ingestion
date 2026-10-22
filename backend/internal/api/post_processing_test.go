package api_test

import (
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestPostProcessingConfiguration(t *testing.T) {
	e := newEnv(t)
	id := makeTask(t, e, e.hsrv.URL, "filesystem")
	var task map[string]any
	if c := e.do("GET", "/api/tasks/"+id, nil, &task); c != 200 {
		t.Fatal(c)
	}
	pp := task["postProcessing"].(map[string]any)
	if pp["enabled"] != false || pp["mode"] != "ui" || len(pp["steps"].([]any)) != 0 {
		t.Fatalf("default post-processing %v", pp)
	}

	var failure map[string]any
	bad := map[string]any{"enabled": true, "mode": "ui", "steps": []any{map[string]any{"type": "http", "enabled": true}}}
	if c := e.do("PATCH", "/api/tasks/"+id, map[string]any{"postProcessing": bad}, &failure); c != 422 {
		t.Fatal(c, failure)
	}
	if _, ok := failure["fields"].(map[string]any)["postProcessing.steps.0.url"]; !ok {
		t.Fatalf("fields %v", failure["fields"])
	}
	syntax := map[string]any{"enabled": true, "mode": "code", "code": "function process( {"}
	if c := e.do("PATCH", "/api/tasks/"+id, map[string]any{"postProcessing": syntax}, &failure); c != 422 {
		t.Fatal(c, failure)
	}

	var before, after models.Task
	e.db.First(&before, "id = ?", id)
	// Disabled configurations, and disabled steps, do not change what runs.
	steps := []any{map[string]any{"type": "trim", "enabled": false}}
	if c := e.do("PATCH", "/api/tasks/"+id, map[string]any{"postProcessing": map[string]any{"enabled": true, "mode": "ui", "steps": steps}}, &task); c != 200 {
		t.Fatal(c, task)
	}
	e.db.First(&after, "id = ?", id)
	if after.PolicyRevision != before.PolicyRevision+1 {
		// Enabling an empty pipeline still changes the executed script.
		t.Fatalf("policy revision %d -> %d", before.PolicyRevision, after.PolicyRevision)
	}
	before = after
	steps = []any{map[string]any{"type": "trim", "enabled": false, "side": "end"}}
	if c := e.do("PATCH", "/api/tasks/"+id, map[string]any{"postProcessing": map[string]any{"enabled": true, "mode": "ui", "steps": steps}}, &task); c != 200 {
		t.Fatal(c, task)
	}
	e.db.First(&after, "id = ?", id)
	if after.PolicyRevision != before.PolicyRevision {
		t.Fatal("editing a disabled step re-retained documents")
	}
	steps = []any{map[string]any{"type": "trim", "enabled": true}}
	if c := e.do("PATCH", "/api/tasks/"+id, map[string]any{"postProcessing": map[string]any{"enabled": true, "mode": "ui", "steps": steps}}, &task); c != 200 {
		t.Fatal(c, task)
	}
	e.db.First(&after, "id = ?", id)
	if after.PolicyRevision != before.PolicyRevision+1 || !after.ReconcileRequired {
		t.Fatal("enabling a step did not invalidate synced documents")
	}
	step := task["postProcessing"].(map[string]any)["steps"].([]any)[0].(map[string]any)
	if step["id"] == "" || step["side"] != "both" {
		t.Fatalf("step defaults %v", step)
	}
}

func TestPostProcessingTestEndpoint(t *testing.T) {
	e := newEnv(t)
	var out map[string]any
	body := map[string]any{
		"input": "  Hello  ", "itemName": "note.md",
		"postProcessing": map[string]any{"mode": "ui", "steps": []any{
			map[string]any{"type": "trim", "enabled": true},
			map[string]any{"type": "append", "enabled": true, "text": " ({{item.name}})"},
		}},
	}
	if c := e.do("POST", "/api/post-processing/test", body, &out); c != 200 {
		t.Fatal(c, out)
	}
	if out["ok"] != true || out["outputType"] != "text" || out["output"] != "Hello (note.md)" || !strings.Contains(out["code"].(string), "steps.append") {
		t.Fatalf("result %v", out)
	}
	body = map[string]any{"input": "aGk=", "base64": true, "postProcessing": map[string]any{"mode": "code",
		"code": `function process(c, input) { console.warn(c.document.contentType); throw new Error("nope " + new TextDecoder().decode(input)); }`}}
	if c := e.do("POST", "/api/post-processing/test", body, &out); c != 200 {
		t.Fatal(c, out)
	}
	logs := out["logs"].([]any)
	if out["ok"] != false || out["error"] != "Error: nope hi" || len(logs) != 1 || logs[0].(map[string]any)["message"] != "binary" {
		t.Fatalf("result %v", out)
	}
}

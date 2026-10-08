package api_test

import (
	"testing"
)

func TestInlineConfigurationAPI(t *testing.T) {
	e := newEnv(t)
	var settings map[string]any
	e.do("GET", "/api/settings", nil, &settings)
	if settings["inlineMultimodalEnabled"] != false {
		t.Fatal("global must default off")
	}
	var src, dst map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{"name": "Notes", "type": "siyuan", "config": map[string]any{"baseUrl": "http://localhost:6806", "token": "secret"}}, &src); code != 201 {
		t.Fatalf("source credential: %d %v", code, src)
	}
	e.do("POST", "/api/credentials", map[string]any{"name": "Destination", "type": "hindsight", "config": map[string]any{"baseUrl": e.hsrv.URL}}, &dst)
	var tasks []map[string]any
	for _, mode := range []string{"global", "override"} {
		body := map[string]any{"name": "Notes", "enabled": false, "sourceType": "siyuan", "sourceCredentialId": src["id"], "destinationCredentialId": dst["id"], "destinationBankId": "dest", "inlineMultimodalMode": mode, "inlineMultimodalEnabled": true, "filePolicyMode": "override"}
		var task map[string]any
		if code := e.do("POST", "/api/tasks", body, &task); code != 201 {
			t.Fatalf("create: %d %v", code, task)
		}
		if task["inlineMultimodalMode"] != mode || task["inlineMultimodalEnabled"] != true {
			t.Fatal("create lost inline fields")
		}
		if code := e.do("PATCH", "/api/tasks/"+task["id"].(string), map[string]any{"name": "Renamed"}, &task); code != 200 || task["inlineMultimodalMode"] != mode || task["inlineMultimodalEnabled"] != true {
			t.Fatal("PATCH omitted fields changed inline configuration")
		}
		tasks = append(tasks, task)
		body["inlineMultimodalMode"] = "invalid"
		if code := e.do("POST", "/api/tasks", body, &task); code != 422 {
			t.Fatalf("invalid mode accepted: %d", code)
		}
	}
	if code := e.do("PATCH", "/api/settings", map[string]any{"inlineMultimodalEnabled": true}, &settings); code != 200 {
		t.Fatalf("settings: %d", code)
	}
	for i, task := range tasks {
		before := task["configRevision"].(float64)
		e.do("GET", "/api/tasks/"+task["id"].(string), nil, &task)
		want := before
		if i == 0 {
			want++
		}
		if task["configRevision"] != want || task["reconcileRequired"] != (i == 0) {
			t.Fatalf("inline inheritance invalidation: %+v", task)
		}
		tasks[i] = task
	}
	// Invert file-policy inheritance: the second task alone follows Images globally.
	e.do("PATCH", "/api/tasks/"+tasks[1]["id"].(string), map[string]any{"filePolicyMode": "global"}, &tasks[1])
	before := []any{tasks[0]["configRevision"], tasks[1]["configRevision"]}
	e.do("PATCH", "/api/settings", map[string]any{"filePolicy": map[string]bool{"plainText": true, "documents": true, "images": true}}, &settings)
	if settings["inlineMultimodalEnabled"] != true {
		t.Fatal("settings PATCH reset omitted inline field")
	}
	for i, task := range tasks {
		e.do("GET", "/api/tasks/"+task["id"].(string), nil, &task)
		want := before[i].(float64)
		if i == 1 {
			want++
		}
		if task["configRevision"] != want {
			t.Fatalf("file inheritance invalidation: %+v", task)
		}
	}
}

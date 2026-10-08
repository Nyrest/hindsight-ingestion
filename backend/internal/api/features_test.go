package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestProxyStorageAndInheritance(t *testing.T) {
	e := newEnv(t)
	var result map[string]any
	cfg := map[string]any{"type": "http", "address": "proxy.invalid:8080", "username": "user", "password": "proxy-secret"}
	if code := e.do("PATCH", "/api/settings", map[string]any{"proxy": cfg}, &result); code != 200 {
		t.Fatalf("settings %d %v", code, result)
	}
	if result["proxy"].(map[string]any)["password"] != credentials.Mask {
		t.Fatal("settings password exposed")
	}
	var row models.Setting
	e.db.First(&row)
	if strings.Contains(row.Value, "proxy-secret") || strings.Contains(row.EncryptedSecret, "proxy-secret") {
		t.Fatal("unencrypted proxy password")
	}
	var cred map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{"name": "HS", "type": "hindsight", "config": map[string]any{"baseUrl": e.hsrv.URL}}, &cred); code != 201 {
		t.Fatal(code, cred)
	}
	id := cred["id"].(string)
	loaded, err := e.creds.Load(t.Context(), id)
	if err != nil || loaded.Proxy.Password != "proxy-secret" {
		t.Fatal(loaded.Proxy.Masked(), err)
	}
	cfg["password"] = "own-secret"
	if code := e.do("PATCH", "/api/credentials/"+id, map[string]any{"proxyMode": "override", "proxy": cfg}, &cred); code != 200 {
		t.Fatal(code, cred)
	}
	for _, password := range []any{credentials.Mask, nil} {
		patch := map[string]any{"type": "http", "address": "other.invalid:8000", "username": "user"}
		if password != nil {
			patch["password"] = password
		}
		if code := e.do("PATCH", "/api/credentials/"+id, map[string]any{"proxy": patch}, &cred); code != 200 {
			t.Fatal(code, cred)
		}
		loaded, err = e.creds.Load(t.Context(), id)
		if err != nil || loaded.Proxy.Password != "own-secret" {
			t.Fatal("lost password", err)
		}
	}
	if err := e.creds.SaveOAuthToken(t.Context(), id, &connectors.OAuthToken{AccessToken: "rotated", RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	loaded, err = e.creds.Load(t.Context(), id)
	if err != nil || loaded.Proxy.Password != "own-secret" || loaded.OAuth.AccessToken != "rotated" {
		t.Fatal("OAuth lost proxy secret", err)
	}
	cfg["password"] = ""
	if code := e.do("PATCH", "/api/credentials/"+id, map[string]any{"proxy": cfg}, &cred); code != 200 {
		t.Fatal(code, cred)
	}
	loaded, _ = e.creds.Load(t.Context(), id)
	if loaded.Proxy.Password != "" {
		t.Fatal("explicit clear did not clear")
	}
	if code := e.do("PATCH", "/api/credentials/"+id, map[string]any{"proxy": map[string]any{"type": "none"}}, &cred); code != 200 {
		t.Fatal(code, cred)
	}
	loaded, _ = e.creds.Load(t.Context(), id)
	if loaded.Proxy.Type != "none" {
		t.Fatal("override ignored")
	}
	if code := e.do("PATCH", "/api/settings", map[string]any{"proxy": map[string]any{"type": "http", "address": "proxy.invalid:8080", "password": credentials.Mask}}, &result); code != 200 {
		t.Fatal(code, result)
	}
	if code := e.do("PATCH", "/api/credentials/"+id, map[string]any{"proxyMode": "global"}, &cred); code != 200 {
		t.Fatal(code, cred)
	}
	loaded, _ = e.creds.Load(t.Context(), id)
	if loaded.Proxy.Password != "proxy-secret" {
		t.Fatal("masked settings update lost secret")
	}
}

func makeTask(t *testing.T, e *env, base string, source string) string {
	t.Helper()
	var dest, src, task map[string]any
	if c := e.do("POST", "/api/credentials", map[string]any{"name": "destination", "type": "hindsight", "config": map[string]any{"baseUrl": base}}, &dest); c != 201 {
		t.Fatal(c, dest)
	}
	config := map[string]any{"rootPath": t.TempDir()}
	sourceConfig := map[string]any{}
	if source == "notion" {
		config = map[string]any{"token": "notion-token"}
		sourceConfig = map[string]any{"dataSourceId": "group"}
	}
	if c := e.do("POST", "/api/credentials", map[string]any{"name": "source", "type": source, "config": config}, &src); c != 201 {
		t.Fatal(c, src)
	}
	if c := e.do("POST", "/api/tasks", map[string]any{"name": "cleanup", "enabled": true, "sourceType": source, "sourceCredentialId": src["id"], "sourceConfig": sourceConfig, "destinationCredentialId": dest["id"], "destinationBankId": "bank", "cronExpression": "0 3 * * *"}, &task); c != 201 {
		t.Fatal(c, task)
	}
	return task["id"].(string)
}

func TestObservationPolicyInvalidation(t *testing.T) {
	e := newEnv(t)
	id := makeTask(t, e, e.hsrv.URL, "notion")
	files := makeTask(t, e, e.hsrv.URL, "filesystem")
	var before, after models.Task
	e.db.First(&before, "id = ?", id)
	if c := e.do("PATCH", "/api/settings", map[string]any{"observationScope": map[string]any{"rule": "per_tag", "scopes": []any{}}}, nil); c != 200 {
		t.Fatal(c)
	}
	e.db.First(&after, "id = ?", id)
	if !after.ReconcileRequired || after.PolicyRevision != before.PolicyRevision+1 {
		t.Fatal("inherited text scope did not invalidate")
	}
	var fileTask models.Task
	e.db.First(&fileTask, "id = ?", files)
	if fileTask.PolicyRevision != before.PolicyRevision {
		t.Fatal("file scope changed revision")
	}
	if c := e.do("PATCH", "/api/tasks/"+id, map[string]any{"observationScopeMode": "override", "observationScope": map[string]any{"rule": "custom", "scopes": [][]map[string]string{{{"kind": "literal", "value": "future-tag"}, {"kind": "dynamic", "value": "task"}}}}}, nil); c != 200 {
		t.Fatal(c)
	}
	e.db.First(&before, "id = ?", id)
	if c := e.do("PATCH", "/api/settings", map[string]any{"observationScope": map[string]any{"rule": "shared", "scopes": []any{}}}, nil); c != 200 {
		t.Fatal(c)
	}
	e.db.First(&after, "id = ?", id)
	if after.PolicyRevision != before.PolicyRevision {
		t.Fatal("overridden task affected by global scope")
	}
	if c := e.do("PATCH", "/api/tasks/"+id, map[string]any{"observationScopeMode": "global"}, nil); c != 200 {
		t.Fatal(c)
	}
	for _, tag := range []string{"first", "second"} {
		e.db.First(&before, "id = ?", id)
		scope := map[string]any{"rule": "custom", "scopes": [][]map[string]string{{{"kind": "literal", "value": tag}}}}
		if c := e.do("PATCH", "/api/settings", map[string]any{"observationScope": scope}, nil); c != 200 {
			t.Fatal(c)
		}
		e.db.First(&after, "id = ?", id)
		if after.PolicyRevision != before.PolicyRevision+1 {
			t.Fatal("custom group edit failed to invalidate", tag)
		}
	}
}

func TestDocumentCleanup(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	docs := map[string][]string{}
	failID, opStatus := "", "processing"
	pages := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(r.URL.Path, "/operations/") {
			json.NewEncoder(w).Encode(map[string]string{"status": opStatus})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tags") {
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"tag": "bank-tag", "count": 2}}, "total": 1, "limit": 100, "offset": 0})
			return
		}
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/documents") {
			if r.URL.Query().Get("tags_match") != "all_strict" {
				t.Error("missing strict filter")
			}
			ids := []string{}
			for id := range docs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			end := min(offset+2, len(ids))
			items := []map[string]any{}
			for _, id := range ids[min(offset, len(ids)):end] {
				items = append(items, map[string]any{"id": id, "tags": docs[id]})
			}
			pages++
			json.NewEncoder(w).Encode(map[string]any{"items": items, "total": len(ids)})
			return
		}
		if r.Method == "DELETE" {
			id := strings.SplitN(r.URL.Path, "/documents/", 2)[1]
			if id == failID {
				w.WriteHeader(400)
				return
			}
			delete(docs, id)
			if id == "d1" {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(404)
	}))
	defer upstream.Close()
	id := makeTask(t, e, upstream.URL, "filesystem")
	tag := "ingestion_task:" + id
	mu.Lock()
	for i := 0; i < 5; i++ {
		docs[fmt.Sprint("d", i)] = []string{tag}
	}
	docs["foreign"] = []string{"ingestion_task:other"}
	docs["untagged"] = nil
	failID = "d3"
	mu.Unlock()
	e.db.Create(&models.TaskItem{TaskID: id, SourceItemID: "item", DestinationDocumentID: "d0", DestinationPresent: true, SyncedFingerprint: "synced"})
	var out map[string]any
	if c := e.do("DELETE", "/api/tasks/"+id+"?deleteDocuments=true", nil, &out); c != 502 {
		t.Fatal(c, out)
	}
	var task models.Task
	e.db.First(&task, "id = ?", id)
	if task.Enabled || !task.ReconcileRequired {
		t.Fatal("cleanup did not pause")
	}
	var item models.TaskItem
	e.db.First(&item, "task_id = ? AND source_item_id = ?", id, "item")
	if item.DestinationPresent || item.SyncedFingerprint != "" {
		t.Fatal("ledger not committed after each delete")
	}
	mu.Lock()
	if pages != 4 || len(docs) != 4 {
		t.Fatal(pages, docs)
	}
	failID = ""
	mu.Unlock()
	if c := e.do("DELETE", "/api/tasks/"+id+"/documents", nil, &out); c != 200 || out["deletedCount"] != float64(2) {
		t.Fatal(c, out)
	}
	mu.Lock()
	if len(docs) != 2 || docs["foreign"] == nil {
		t.Fatal(docs)
	}
	mu.Unlock()
	ctx, release, err := e.runs.BeginMaintenance(t.Context(), id)
	_ = ctx
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct {
		method, path string
		body         any
	}{{"DELETE", "/documents", nil}, {"DELETE", "", nil}, {"PATCH", "", map[string]any{"name": "busy"}}, {"POST", "/run", nil}} {
		if c := e.do(route.method, "/api/tasks/"+id+route.path, route.body, nil); c != 409 {
			t.Fatal("maintenance conflict", route, c)
		}
	}
	release()
	e.db.Create(&models.TaskRun{ID: "run-op", TaskID: id, Status: models.RunInterrupted})
	e.db.Create(&models.HindsightOperation{ID: "op", RunID: "run-op", RemoteOperationID: "remote", Status: "abandoned"})
	e.db.Model(&models.Task{}).Where("id = ?", id).Update("enabled", true)
	if c := e.do("DELETE", "/api/tasks/"+id+"/documents", nil, nil); c != 409 {
		t.Fatal("pending retain", c)
	}
	e.db.First(&task, "id = ?", id)
	if task.Enabled {
		t.Fatal("pending retain conflict left task scheduled")
	}
	if c := e.do("DELETE", "/api/tasks/"+id, nil, nil); c != 409 {
		t.Fatal("task deletion lost pending remote operations", c)
	}
	mu.Lock()
	opStatus = "completed"
	docs["fresh"] = []string{tag}
	mu.Unlock()
	if c := e.do("DELETE", "/api/tasks/"+id+"?deleteDocuments=true", nil, nil); c != 204 {
		t.Fatal(c)
	}
	if c := e.do("GET", "/api/tasks/"+id, nil, nil); c != 404 {
		t.Fatal("task remains", c)
	}
	other := makeTask(t, e, upstream.URL, "filesystem")
	mu.Lock()
	docs["keep"] = []string{"ingestion_task:" + other}
	mu.Unlock()
	if c := e.do("DELETE", "/api/tasks/"+other, nil, nil); c != 204 {
		t.Fatal(c)
	}
	mu.Lock()
	defer mu.Unlock()
	if docs["keep"] == nil {
		t.Fatal("unchecked deletion touched remote")
	}
}

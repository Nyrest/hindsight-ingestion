package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/api"
	"github.com/Nyrest/hindsight-ingestion/internal/config"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/oauth"
	"github.com/Nyrest/hindsight-ingestion/internal/runner"
	"github.com/Nyrest/hindsight-ingestion/internal/scheduler"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"

	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/filesystem"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/googledrive"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/hindsightsrc"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/onedrive"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/s3"
)

// fakeHindsight is an in-memory Hindsight server.
type fakeHindsight struct {
	mu   stdsync.Mutex
	docs map[string]map[string]any
}

func (f *fakeHindsight) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/default/banks" && r.Method == "GET":
			io.WriteString(w, `{"banks":[{"bank_id":"dest"},{"bank_id":"src"}],"total":2}`)
		case r.URL.Path == "/version":
			io.WriteString(w, `{"api_version":"0.10.2"}`)
		case strings.HasSuffix(r.URL.Path, "/config"):
			io.WriteString(w, `{"config":{"retain_default_strategy":"docs","retain_strategies":{"docs":{},"chat":{}}}}`)
		case strings.HasSuffix(r.URL.Path, "/memories") && r.Method == "POST":
			var body struct {
				Items []map[string]any `json:"items"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			for _, it := range body.Items {
				f.docs[it["document_id"].(string)] = it
			}
			io.WriteString(w, `{"success":true,"operation_id":"op"}`)
		case strings.Contains(r.URL.Path, "/operations/"):
			io.WriteString(w, `{"status":"completed"}`)
		case strings.Contains(r.URL.Path, "/documents/") && r.Method == "DELETE":
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			delete(f.docs, id)
			io.WriteString(w, `{"success":true}`)
		case strings.HasPrefix(r.URL.Path, "/v1/default/banks/src/documents/"):
			io.WriteString(w, `{"id":"doc1","original_text":"source text","updated_at":"2026-01-01T00:00:00Z"}`)
		case r.URL.Path == "/v1/default/banks/src/documents":
			io.WriteString(w, `{"items":[{"id":"doc1","content_hash":"h1","updated_at":"2026-01-01T00:00:00Z","tags":["x"]}],"total":1}`)
		default:
			w.WriteHeader(404)
		}
	})
}

type env struct {
	t    *testing.T
	srv  *httptest.Server
	hs   *fakeHindsight
	hsrv *httptest.Server
	runs *runner.Manager
}

func newEnv(t *testing.T) *env {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "api.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s, _ := db.DB(); s.Close() })
	cipher, _ := crypto.New(bytes.Repeat([]byte{1}, 32))
	cfg := &config.Config{ListenAddr: ":8080", DBType: "sqlite", BasicAuthUsername: "admin", BasicAuthPassword: "pw"}
	creds := credentials.NewService(db, cipher)
	store := settings.NewStore(db)
	om := oauth.NewManager(cfg, db, creds, log)
	engine := &sync.Engine{DB: db, Creds: creds, Settings: store, OAuth: om, Log: log,
		NewDestination: func(c connectors.Credential) (sync.Destination, error) { return hindsight.NewClient(c) }}
	sync.OperationPollInitial = time.Millisecond
	runs := runner.NewManager(db, engine, 2, log)
	sched, _ := scheduler.New(db, runs, log)
	om.SetScheduler(sched)
	sched.Start()
	t.Cleanup(func() { sched.Shutdown() })

	s := &api.Server{Cfg: cfg, DB: db, Creds: creds, Settings: store, OAuth: om, Runs: runs, Sched: sched, Log: log}
	mux := http.NewServeMux()
	s.Routes(mux)
	hs := &fakeHindsight{docs: map[string]map[string]any{}}
	e := &env{t: t, srv: httptest.NewServer(mux), hs: hs, hsrv: httptest.NewServer(hs.handler()), runs: runs}
	t.Cleanup(e.srv.Close)
	t.Cleanup(e.hsrv.Close)
	return e
}

func (e *env) do(method, path string, body any, out any) int {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)

	var conns struct {
		CredentialTypes []map[string]any `json:"credentialTypes"`
		Sources         []map[string]any `json:"sources"`
	}
	if e.do("GET", "/api/connectors", nil, &conns) != 200 || len(conns.Sources) < 2 {
		t.Fatalf("connectors: %+v", conns)
	}

	// Credential with secret and custom headers.
	var hcred map[string]any
	code := e.do("POST", "/api/credentials", map[string]any{
		"name": "HS", "type": "hindsight",
		"config":        map[string]any{"baseUrl": e.hsrv.URL, "apiKey": "secret-key"},
		"customHeaders": map[string]string{"X-A": "1"},
	}, &hcred)
	if code != 201 {
		t.Fatalf("create credential: %d %v", code, hcred)
	}
	cfg := hcred["config"].(map[string]any)
	if cfg["apiKey"] != credentials.Mask || hcred["customHeaders"].(map[string]any)["X-A"] != credentials.Mask {
		t.Fatalf("secrets not masked: %v", hcred)
	}
	credID := hcred["id"].(string)

	// Duplicate headers rejected.
	var errBody map[string]any
	if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"customHeaders": map[string]string{"X-A": "1", "x-a": "2"}}, &errBody); code != 422 {
		t.Fatalf("duplicate header: %d %v", code, errBody)
	}

	var test map[string]any
	e.do("POST", "/api/credentials/"+credID+"/test", nil, &test)
	if test["ok"] != true {
		t.Fatalf("test credential: %v", test)
	}
	var strategies map[string]any
	if code := e.do("GET", "/api/credentials/"+credID+"/strategies?bankId=dest", nil, &strategies); code != 200 || strategies["defaultStrategy"] != "docs" {
		t.Fatalf("strategies: %d %v", code, strategies)
	}
	var browse map[string]any
	if code := e.do("POST", "/api/credentials/"+credID+"/browse", map[string]any{}, &browse); code != 200 || len(browse["items"].([]any)) != 2 {
		t.Fatalf("browse: %d %v", code, browse)
	}

	// Validation: no-op Hindsight → same bank.
	if code := e.do("POST", "/api/tasks", map[string]any{
		"name": "noop", "sourceType": "hindsight", "sourceCredentialId": credID, "sourceConfig": map[string]any{"bankId": "dest"},
		"destinationCredentialId": credID, "destinationBankId": "dest", "cronExpression": "0 3 * * *",
	}, &errBody); code != 422 {
		t.Fatalf("no-op task should be rejected: %d", code)
	}
	// Reserved metadata rejected.
	if code := e.do("POST", "/api/tasks", map[string]any{
		"name": "bad", "sourceType": "hindsight", "sourceCredentialId": credID, "sourceConfig": map[string]any{"bankId": "src"},
		"destinationCredentialId": credID, "destinationBankId": "dest", "cronExpression": "0 3 * * *",
		"customMetadata": map[string]string{"_ingestion_x": "1"},
	}, &errBody); code != 422 {
		t.Fatalf("reserved metadata should be rejected: %d", code)
	}

	// Hindsight → Hindsight task.
	var task map[string]any
	code = e.do("POST", "/api/tasks", map[string]any{
		"name": "copy", "sourceType": "hindsight", "sourceCredentialId": credID,
		"sourceConfig": map[string]any{"bankId": "src"}, "destinationCredentialId": credID, "destinationBankId": "dest",
		"cronExpression": "*/15 * * * *", "cronTimezone": "Europe/Berlin", "customTags": []string{"copied"},
	}, &task)
	if code != 201 {
		t.Fatalf("create task: %d %v", code, task)
	}
	taskID := task["id"].(string)
	if task["nextRunAt"] == nil {
		t.Fatal("enabled task has no next run")
	}

	var started map[string]string
	if code := e.do("POST", "/api/tasks/"+taskID+"/run", nil, &started); code != 202 {
		t.Fatalf("run: %d", code)
	}
	waitIdle(t, e.runs, taskID)
	var run map[string]any
	e.do("GET", "/api/runs/"+started["runId"], nil, &run)
	if run["status"] != "succeeded" || run["createdCount"].(float64) != 1 {
		t.Fatalf("run: %v", run)
	}
	docID := sync.DocumentID(taskID, "doc1")
	e.hs.mu.Lock()
	doc := e.hs.docs[docID]
	e.hs.mu.Unlock()
	if doc == nil || doc["content"] != "source text" {
		t.Fatalf("destination doc: %v", e.hs.docs)
	}

	// Destination now locked.
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"destinationBankId": "other"}, &errBody); code != 422 {
		t.Fatalf("locked destination change: %d", code)
	}
	// Changing the global size limit requires reconciliation.
	var changedSettings map[string]any
	if code := e.do("PATCH", "/api/settings", map[string]any{"maxFileSizeMB": 1}, &changedSettings); code != 200 {
		t.Fatalf("change size limit: %d %v", code, changedSettings)
	}
	e.do("GET", "/api/tasks/"+taskID, nil, &task)
	if task["reconcileRequired"] != true {
		t.Fatal("size limit change did not require reconciliation")
	}
	if code := e.do("POST", "/api/tasks/"+taskID+"/full-reconcile", nil, &started); code != 202 {
		t.Fatalf("reconcile size limit change: %d", code)
	}
	waitIdle(t, e.runs, taskID)
	e.do("GET", "/api/tasks/"+taskID, nil, &task)
	if task["reconcileRequired"] != false {
		t.Fatal("successful reconciliation did not clear the flag")
	}
	// Tags change → policy revision + reconcile.
	e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"customTags": []string{"copied", "v2"}}, &task)
	if task["policyRevision"].(float64) != 2 || task["reconcileRequired"] != true {
		t.Fatalf("policy change: %v", task)
	}
	// Name change → no sync effect.
	e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"name": "renamed"}, &task)
	if task["configRevision"].(float64) != 2 {
		t.Fatalf("rename bumped config revision: %v", task["configRevision"])
	}
	// Disable removes job; re-enable restores it.
	e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"enabled": false}, &task)
	if task["nextRunAt"] != nil {
		t.Fatal("disabled task still scheduled")
	}
	e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"enabled": true}, &task)
	if task["nextRunAt"] == nil {
		t.Fatal("re-enabled task not scheduled")
	}

	var cron map[string]any
	e.do("POST", "/api/tasks/validate-cron", map[string]any{"cronExpression": "bad", "cronTimezone": "UTC"}, &cron)
	if cron["valid"] != false {
		t.Fatal("invalid cron accepted")
	}

	// Credential in use cannot be deleted.
	if code := e.do("DELETE", "/api/credentials/"+credID, nil, nil); code != 409 {
		t.Fatalf("delete used credential: %d", code)
	}
	var dash map[string]any
	if code := e.do("GET", "/api/dashboard", nil, &dash); code != 200 || dash["enabledTasks"].(float64) != 1 {
		t.Fatalf("dashboard: %v", dash)
	}
	if code := e.do("DELETE", "/api/tasks/"+taskID, nil, nil); code != 204 {
		t.Fatalf("delete task: %d", code)
	}
	if code := e.do("DELETE", "/api/credentials/"+credID, nil, nil); code != 204 {
		t.Fatalf("delete credential: %d", code)
	}

	var s map[string]any
	if code := e.do("PATCH", "/api/settings", map[string]any{"maxFileSizeMB": 0}, &s); code != 422 {
		t.Fatalf("invalid settings accepted: %d", code)
	}
	e.do("PATCH", "/api/settings", map[string]any{"fullReconcileIntervalHours": 6}, &s)
	if s["fullReconcileIntervalHours"].(float64) != 6 || s["incrementalSyncEnabled"] != true {
		t.Fatalf("settings: %v", s)
	}
}

func waitIdle(t *testing.T, runs *runner.Manager, taskID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if _, busy := runs.Active(taskID); !busy {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("run did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestOAuthCredentialApplicationSettings(t *testing.T) {
	e := newEnv(t)
	for _, typ := range []string{"google_drive", "onedrive"} {
		t.Run(typ, func(t *testing.T) {
			for _, missing := range []string{"clientId", "clientSecret"} {
				cfg := map[string]any{"clientId": "app-id", "clientSecret": "app-secret"}
				delete(cfg, missing)
				var invalid struct {
					Fields map[string]string `json:"fields"`
				}
				code := e.do("POST", "/api/credentials", map[string]any{"name": typ, "type": typ, "config": cfg}, &invalid)
				if code != 422 || invalid.Fields[missing] == "" {
					t.Fatalf("missing %s: status=%d fields=%v", missing, code, invalid.Fields)
				}
			}
			var cred struct {
				ID     string         `json:"id"`
				Config map[string]any `json:"config"`
			}
			code := e.do("POST", "/api/credentials", map[string]any{
				"name": typ, "type": typ, "config": map[string]any{"clientId": "app-id", "clientSecret": "app-secret"},
			}, &cred)
			if code != 201 || cred.Config["clientId"] != "app-id" || cred.Config["clientSecret"] != credentials.Mask {
				t.Fatalf("OAuth credential: status=%d config=%v", code, cred.Config)
			}
			if typ == "onedrive" && cred.Config["tenant"] != "common" {
				t.Fatal("OneDrive credential did not store the default tenant")
			}
			code = e.do("PATCH", "/api/credentials/"+cred.ID, map[string]any{"config": map[string]any{"clientId": "updated-id", "clientSecret": credentials.Mask}}, &cred)
			if code != 200 || cred.Config["clientId"] != "updated-id" || cred.Config["clientSecret"] != credentials.Mask {
				t.Fatalf("OAuth credential edit: status=%d config=%v", code, cred.Config)
			}
		})
	}
}

func TestFilesystemDryRunAndHourlyDefault(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	var source, dest map[string]any
	if status := e.do("POST", "/api/credentials", map[string]any{"name": "Local", "type": "filesystem", "config": map[string]any{"rootPath": root}}, &source); status != 201 {
		t.Fatalf("source: %d %+v", status, source)
	}
	if status := e.do("POST", "/api/credentials", map[string]any{"name": "Destination", "type": "hindsight", "config": map[string]any{"baseUrl": e.hsrv.URL}}, &dest); status != 201 {
		t.Fatalf("destination: %d %+v", status, dest)
	}
	sourceID := source["id"].(string)
	var test map[string]any
	if status := e.do("POST", "/api/credentials/"+sourceID+"/test", nil, &test); status != 200 || test["ok"] != true {
		t.Fatalf("credential test: %d %+v", status, test)
	}
	var task map[string]any
	if status := e.do("POST", "/api/tasks", map[string]any{"name": "Local task", "enabled": false, "sourceType": "filesystem", "sourceCredentialId": sourceID, "sourceConfig": map[string]any{"folder": "."}, "destinationCredentialId": dest["id"], "destinationBankId": "dest"}, &task); status != 201 {
		t.Fatalf("task: %d %+v", status, task)
	}
	if task["cronExpression"] != "0 * * * *" || task["enabled"] != false || task["nextRunAt"] != nil {
		t.Fatalf("schedule: %+v", task)
	}
	id := task["id"].(string)
	var result sync.DryRunResult
	if status := e.do("POST", "/api/tasks/"+id+"/dry-run", nil, &result); status != 200 || !result.Complete || result.CreatedCount != 1 {
		t.Fatalf("dry run: %d %+v", status, result)
	}
	var after map[string]any
	e.do("GET", "/api/tasks/"+id, nil, &after)
	if after["destinationLocked"] != false || after["lastRun"] != nil || after["itemCount"] != float64(0) || after["state"].(map[string]any)["hasCursor"] != false {
		t.Fatalf("preview changed task: %+v", after)
	}
	if len(e.hs.docs) != 0 {
		t.Fatal("dry-run wrote destination")
	}
	var spec map[string]any
	if status := e.do("GET", "/api/openapi.json", nil, &spec); status != 200 || spec["openapi"] != "3.1.0" {
		t.Fatalf("openapi: %d", status)
	}
	var missing map[string]any
	if status := e.do("POST", "/api/tasks/missing/dry-run", nil, &missing); status != 404 {
		t.Fatalf("missing task: %d %+v", status, missing)
	}
}

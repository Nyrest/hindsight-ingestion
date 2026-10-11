package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/cloudflare"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/shared"
	ingestion "github.com/Nyrest/hindsight-ingestion/internal/sync"
)

func cloudAPITasks(t *testing.T, e *env) (string, string) {
	t.Helper()
	var source, destination map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{"name": "Cloudflare", "type": "cloudflare_crawl", "config": map[string]any{"accountId": strings.Repeat("a", 32), "apiToken": "cloud-secret"}}, &source); code != 201 {
		t.Fatal(code, source)
	}
	if source["config"].(map[string]any)["apiToken"] != credentials.Mask {
		t.Fatal("unmasked Cloudflare token")
	}
	var encrypted models.Credential
	e.db.First(&encrypted, "id = ?", source["id"])
	if strings.Contains(encrypted.ConfigJSON, "cloud-secret") || strings.Contains(encrypted.EncryptedSecret, "cloud-secret") {
		t.Fatal("plaintext Cloudflare token stored")
	}
	if code := e.do("POST", "/api/credentials", map[string]any{"name": "Hindsight", "type": "hindsight", "config": map[string]any{"baseUrl": e.hsrv.URL}}, &destination); code != 201 {
		t.Fatal(code, destination)
	}
	ids := []string{}
	for _, name := range []string{"A", "B"} {
		var task map[string]any
		if code := e.do("POST", "/api/tasks", map[string]any{"name": name, "enabled": false, "sourceType": "cloudflare_crawl", "sourceCredentialId": source["id"], "sourceConfig": map[string]any{"url": "https://example.com/", "includePatterns": []string{"https://example.com/**"}, "rejectResourceTypes": []string{"image", "media", "font"}}, "destinationCredentialId": destination["id"], "destinationBankId": "dest"}, &task); code != 201 {
			t.Fatal(code, task)
		}
		ids = append(ids, task["id"].(string))
	}
	return ids[0], ids[1]
}

func seedCloudReferences(t *testing.T, e *env, a, b string) string {
	t.Helper()
	var task models.Task
	e.db.First(&task, "id = ?", a)
	cred, err := e.creds.Load(t.Context(), task.DestinationCredentialID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := shared.TargetKey(cred, task.DestinationBankID)
	if err != nil {
		t.Fatal(err)
	}
	itemID := strings.Repeat("f", 64)
	for _, taskID := range []string{a, b} {
		id := ingestion.DocumentID(connectors.SourceCloudflareCrawl, taskID, itemID)
		if err := e.db.Create(&models.TaskItem{TaskID: taskID, SourceItemID: itemID, SharedTargetKey: key, DestinationDocumentID: id, DestinationPresent: true, SyncedFingerprint: "confirmed"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := e.db.Create(&models.SharedDocument{TargetKey: key, DocumentID: id, SyncedFingerprint: "confirmed"}).Error; err != nil {
			t.Fatal(err)
		}
		e.hs.docs[id] = map[string]any{"document_id": id, "tags": []string{"ingestion_task:" + taskID}}
	}
	return ingestion.DocumentID(connectors.SourceCloudflareCrawl, a, itemID)
}

func TestCloudflareCleanupAndTaskDeletionAreIsolated(t *testing.T) {
	e := newEnv(t)
	a, b := cloudAPITasks(t, e)
	id := seedCloudReferences(t, e, a, b)
	var response map[string]any
	if code := e.do("DELETE", "/api/tasks/"+a+"/documents", nil, &response); code != 200 || response["deletedCount"] != float64(1) {
		t.Fatal(code, response)
	}
	if len(e.hs.docs) != 1 || e.hs.docs[id] != nil {
		t.Fatal("cleanup did not delete only the current task's document")
	}
	var row models.TaskItem
	e.db.Where("task_id = ?", a).First(&row)
	if row.DestinationPresent {
		t.Fatal("cleanup retained current task reference")
	}
	if code := e.do("DELETE", "/api/tasks/"+a+"?deleteDocuments=true", nil, &response); code != 204 {
		t.Fatal(code, response)
	}
	if code := e.do("DELETE", "/api/tasks/"+b+"?deleteDocuments=true", nil, &response); code != 204 {
		t.Fatal(code, response)
	}
	e.hs.mu.Lock()
	_, exists := e.hs.docs[id]
	e.hs.mu.Unlock()
	if exists {
		t.Fatal("last reference left remote document behind")
	}
	var count int64
	e.db.Model(&models.SharedDocument{}).Count(&count)
	if count != 0 {
		t.Fatal("shared state left behind")
	}
}

func TestCloudflareCleanupProtectsAnotherTasksPendingOperation(t *testing.T) {
	e := newEnv(t)
	a, b := cloudAPITasks(t, e)
	seedCloudReferences(t, e, a, b)
	e.db.Create(&models.TaskRun{ID: "pending-cloud-run", TaskID: b, Status: models.RunInterrupted})
	e.db.Create(&models.HindsightOperation{ID: "pending-cloud-op", RunID: "pending-cloud-run", RemoteOperationID: "op", Status: "pending"})
	original := e.hs.handler()
	e.hsrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/operations/") {
			fmt.Fprint(w, `{"status":"processing"}`)
			return
		}
		original.ServeHTTP(w, r)
	})
	var response map[string]any
	if code := e.do("DELETE", "/api/tasks/"+a+"/documents", nil, &response); code != 409 {
		t.Fatal(code, response)
	}
	var row models.TaskItem
	e.db.Where("task_id = ?", a).First(&row)
	if !row.DestinationPresent || len(e.hs.docs) != 2 {
		t.Fatal("pending write lost references/documents")
	}
}

func TestCloudflareActiveTaskChangesConflictAndRunShowsJob(t *testing.T) {
	e := newEnv(t)
	var ready atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			fmt.Fprint(w, `{"success":true,"result":"job"}`)
		case "DELETE":
			fmt.Fprint(w, `{"success":true,"result":{}}`)
		default:
			status := "running"
			if ready.Load() {
				status = "completed"
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "job", "status": status, "total": 0, "finished": 0}})
		}
	}))
	defer server.Close()
	original, _ := connectors.Source("cloudflare_crawl")
	connectors.RegisterSource(&cloudflare.Connector{APIBase: server.URL})
	defer connectors.RegisterSource(original)
	a, _ := cloudAPITasks(t, e)
	var started map[string]string
	if code := e.do("POST", "/api/tasks/"+a+"/run", nil, &started); code != 202 {
		t.Fatal(code, started)
	}
	defer func() { e.runs.Cancel(a); waitIdle(t, e.runs, a) }()
	var run map[string]any
	for attempt := 0; attempt < 100; attempt++ {
		e.do("GET", "/api/runs/"+started["runId"], nil, &run)
		if run["status"] == "waiting_source" {
			break
		}
	}
	if run["status"] != "waiting_source" || run["sourceJob"].(map[string]any)["id"] != "job" {
		t.Fatal("missing job progress", run)
	}
	var response map[string]any
	for _, request := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/api/tasks/" + a, map[string]any{"name": "changed"}},
		{"DELETE", "/api/tasks/" + a + "/documents", nil},
		{"DELETE", "/api/tasks/" + a + "?deleteDocuments=true", nil},
	} {
		if code := e.do(request.method, request.path, request.body, &response); code != 409 {
			t.Fatal("missing conflict", request, code, response)
		}
	}
}

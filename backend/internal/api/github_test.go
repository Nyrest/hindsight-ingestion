package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

func TestGitHubTaskLifecycleAndDryRun(t *testing.T) {
	e := newEnv(t)
	phase := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/api/v3/repos/owner/repo")
		write := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
		switch {
		case p == "":
			write(map[string]any{"id": 42, "full_name": "owner/repo", "default_branch": "main", "html_url": "https://github.com/owner/repo"})
		case p == "/commits/main":
			tree := "old"
			if phase > 0 {
				tree = "new"
			}
			write(map[string]any{"commit": map[string]any{"tree": map[string]string{"sha": tree}}})
		case strings.HasPrefix(p, "/git/trees/"):
			name := "before.md"
			if strings.HasSuffix(p, "/new") {
				name = "after.md"
			}
			write(map[string]any{"tree": []map[string]any{{"path": name, "sha": "blob", "mode": "100644", "type": "blob", "size": 10}}})
		case p == "/git/blobs/blob":
			io.WriteString(w, "file body\n")
		case p == "/issues":
			if phase == 2 {
				w.WriteHeader(403)
				return
			}
			items := []map[string]any{{"number": 2, "title": "PR", "body": "PR description", "state": "closed", "pull_request": map[string]any{}, "html_url": "https://github.com/owner/repo/pull/2"}}
			if phase == 0 {
				items = append(items, map[string]any{"number": 1, "title": "Issue", "body": "issue body", "state": "open", "html_url": "https://github.com/owner/repo/issues/1"})
			}
			write(items)
		case p == "/issues/1/comments", p == "/issues/2/comments", p == "/pulls/2/reviews", p == "/pulls/2/comments", p == "/issues/comments", p == "/pulls/comments":
			write([]any{})
		case p == "/pulls/2":
			write(map[string]any{"merged": true})
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	var conns struct {
		Sources []connectors.SourceInfo `json:"sources"`
	}
	if code := e.do("GET", "/api/connectors", nil, &conns); code != 200 {
		t.Fatal(code)
	}
	found := false
	for _, src := range conns.Sources {
		if src.Type == "github" {
			found = src.CredentialType == "github" && src.Capabilities.SupportsFiles && len(src.Fields) == 7
		}
	}
	if !found {
		t.Fatal("GitHub source not advertised")
	}
	var source, dest map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{"name": "GitHub", "type": "github", "config": map[string]any{"baseUrl": upstream.URL + "/api/v3", "token": "secret-token"}}, &source); code != 201 {
		t.Fatalf("source credential %d %v", code, source)
	}
	if source["config"].(map[string]any)["token"] != credentials.Mask {
		t.Fatal("PAT not masked")
	}
	if code := e.do("POST", "/api/credentials", map[string]any{"name": "Destination", "type": "hindsight", "config": map[string]any{"baseUrl": e.hsrv.URL}}, &dest); code != 201 {
		t.Fatal(code)
	}
	var task map[string]any
	if code := e.do("POST", "/api/tasks", map[string]any{"name": "GitHub task", "enabled": false, "sourceType": "github", "sourceCredentialId": source["id"], "sourceConfig": map[string]any{"repository": "owner/repo", "respectGitignore": false}, "destinationCredentialId": dest["id"], "destinationBankId": "dest"}, &task); code != 201 {
		t.Fatalf("task %d %v", code, task)
	}
	id := task["id"].(string)
	var preview sync.DryRunResult
	if code := e.do("POST", "/api/tasks/"+id+"/dry-run", nil, &preview); code != 200 || !preview.Complete || preview.CreatedCount != 3 {
		t.Fatalf("dry-run %d %+v", code, preview)
	}
	var itemCount, runCount int64
	e.db.Model(&models.TaskItem{}).Where("task_id = ?", id).Count(&itemCount)
	e.db.Model(&models.TaskRun{}).Where("task_id = ?", id).Count(&runCount)
	var st models.TaskState
	e.db.First(&st, "task_id = ?", id)
	if itemCount != 0 || runCount != 0 || st.CommittedCursorJSON != "" || len(e.hs.docs) != 0 {
		t.Fatal("dry-run mutated sync or destination state")
	}
	run := func(mode string) models.TaskRun {
		t.Helper()
		runID, err := e.runs.Start(id, models.TriggerManual, mode, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		var r models.TaskRun
		e.db.First(&r, "id = ?", runID)
		return r
	}
	if r := run(""); r.Status != models.RunSucceeded || r.CreatedCount != 3 {
		t.Fatalf("baseline %+v", r)
	}
	if r := run(""); r.Status != models.RunSucceeded || r.CreatedCount != 0 || r.UpdatedCount != 0 || r.UnchangedCount != 3 {
		t.Fatalf("unchanged %+v", r)
	}
	phase = 1
	if r := run(models.SyncFull); r.Status != models.RunSucceeded || r.DeletedCount != 2 || r.CreatedCount != 1 || r.UnchangedCount != 1 {
		t.Fatalf("reconcile %+v", r)
	}
	e.db.First(&st, "task_id = ?", id)
	before := st.CommittedCursorJSON
	phase = 2
	if r := run(models.SyncFull); r.Status != models.RunFailed || r.DeletedCount != 0 {
		t.Fatalf("failed scan %+v", r)
	}
	e.db.First(&st, "task_id = ?", id)
	if before != st.CommittedCursorJSON || len(e.hs.docs) != 2 {
		t.Fatal("failed scan advanced cursor or deleted documents")
	}
	var response map[string]any
	if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"sourceConfig": map[string]any{"repository": "owner/repo", "ref": "feature", "respectGitignore": false}}, &response); code != 200 {
		t.Fatalf("change ref %d %v", code, response)
	}
	e.db.First(&st, "task_id = ?", id)
	if st.CommittedCursorJSON != "" || response["reconcileRequired"] != true {
		t.Fatal("ref did not rebaseline")
	}
}

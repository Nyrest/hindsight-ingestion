package api_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	gitsource "github.com/Nyrest/hindsight-ingestion/internal/connectors/git"
	"github.com/Nyrest/hindsight-ingestion/internal/testutil"
)

func TestGitAnonymousTaskAndCacheDeletionAPI(t *testing.T) {
	e := newEnv(t)
	repository := testutil.NewGitRepository(t)
	repository.Write("README.md", "# source")
	repository.Commit("initial")
	source := &gitsource.Source{CacheDir: t.TempDir()}
	connectors.RegisterSource(source)
	var destination map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{"name": "Hindsight", "type": "hindsight", "config": map[string]any{"baseUrl": e.hsrv.URL}}, &destination); code != 201 {
		t.Fatalf("destination=%d %v", code, destination)
	}
	var task map[string]any
	body := map[string]any{"name": "Git", "enabled": false, "sourceType": "git", "sourceConfig": map[string]any{"repositoryUrl": repository.URL()}, "destinationCredentialId": destination["id"], "destinationBankId": "dest"}
	if code := e.do("POST", "/api/tasks", body, &task); code != 201 {
		t.Fatalf("anonymous Git task=%d %v", code, task)
	}
	if task["sourceCredentialId"] != "" {
		t.Fatal("public repository unexpectedly requires credentials")
	}
	taskID := task["id"].(string)
	var rejected map[string]any
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"sourceCredentialId": destination["id"]}, &rejected); code != 422 {
		t.Fatalf("wrong credential accepted: %d %v", code, rejected)
	}
	var started map[string]any
	if code := e.do("POST", "/api/tasks/"+taskID+"/run", nil, &started); code != 202 {
		t.Fatalf("run=%d %v", code, started)
	}
	waitIdle(t, e.runs, taskID)
	caches, err := os.ReadDir(source.CacheDir)
	if err != nil || len(caches) != 1 {
		t.Fatalf("cache=%v err=%v", caches, err)
	}
	cache := filepath.Join(source.CacheDir, caches[0].Name())
	if code := e.do("DELETE", "/api/tasks/"+taskID, nil, &rejected); code != 204 {
		t.Fatalf("delete task=%d %v", code, rejected)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("task deletion left Git cache: %v", err)
	}
}

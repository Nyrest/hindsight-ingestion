package sync_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	gitsource "github.com/Nyrest/hindsight-ingestion/internal/connectors/git"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/testutil"
)

func gitHarness(t *testing.T) (*harness, *testutil.GitRepository, *gitsource.Source) {
	t.Helper()
	h := newHarness(t)
	repository := testutil.NewGitRepository(t)
	source := &gitsource.Source{CacheDir: t.TempDir()}
	connectors.RegisterSource(source)
	h.task.SourceType = "git"
	h.task.SourceCredentialID = ""
	config, _ := json.Marshal(map[string]any{"repositoryUrl": repository.URL(), "keepCache": false})
	h.task.SourceConfigJSON = string(config)
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	return h, repository, source
}

func TestGitIncrementalAndSourceTruth(t *testing.T) {
	h, repository, _ := gitHarness(t)
	repository.Write("a.md", "alpha")
	repository.Write("b.txt", "beta")
	repository.Commit("initial")
	initial := h.run("")
	expectStatus(t, initial, models.RunSucceeded)
	if initial.CreatedCount != 2 || h.dest.retains != 1 {
		t.Fatalf("initial created=%d batches=%d", initial.CreatedCount, h.dest.retains)
	}
	unchanged := h.run("")
	expectStatus(t, unchanged, models.RunSucceeded)
	if unchanged.UnchangedCount != 2 || unchanged.SyncMode != models.SyncIncremental || h.dest.retains != 1 {
		t.Fatalf("unchanged=%+v retains=%d", unchanged, h.dest.retains)
	}
	repository.Write("a.md", "alpha updated")
	repository.Commit("one change")
	updated := h.run("")
	expectStatus(t, updated, models.RunSucceeded)
	if updated.UpdatedCount != 1 || updated.UnchangedCount != 1 {
		t.Fatalf("updated=%+v", updated)
	}
	repository.Command("mv", "b.txt", "renamed.txt")
	repository.Commit("rename")
	renamed := h.run("")
	expectStatus(t, renamed, models.RunSucceeded)
	if renamed.CreatedCount != 1 || renamed.DeletedCount != 1 {
		t.Fatalf("renamed=%+v", renamed)
	}
	repository.Command("rm", "a.md", "renamed.txt")
	repository.Commit("empty tree")
	empty := h.run("")
	expectStatus(t, empty, models.RunSucceeded)
	if empty.DeletedCount != 2 || len(h.dest.docs) != 0 {
		t.Fatalf("empty=%+v docs=%d", empty, len(h.dest.docs))
	}
}

func TestGitDryRunAndFailureKeepDestination(t *testing.T) {
	h, repository, source := gitHarness(t)
	repository.Write("a.txt", "alpha")
	repository.Commit("initial")
	expectStatus(t, h.run(""), models.RunSucceeded)
	repository.Command("rm", "a.txt")
	repository.Write("b.txt", "beta")
	repository.Commit("replace")
	preview, err := h.engine.DryRun(t.Context(), h.task.ID)
	if err != nil || !preview.Complete || preview.CreatedCount != 1 || preview.DeletedCount != 1 || len(h.dest.docs) != 1 {
		t.Fatalf("preview=%+v err=%v docs=%d", preview, err, len(h.dest.docs))
	}
	entries, err := os.ReadDir(source.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("dry-run temporary cache was not cleaned")
	}
	before := h.state().CommittedCursorJSON
	repository.Server.Close()
	failed := h.run("")
	expectStatus(t, failed, models.RunFailed)
	if failed.DeletedCount != 0 || len(h.dest.docs) != 1 || h.state().CommittedCursorJSON != before {
		t.Fatalf("failed scan changed destination/state: %+v", failed)
	}
}

func TestGitTaskFileLimitChangeRemovesUnchangedLargeFile(t *testing.T) {
	h, repository, _ := gitHarness(t)
	repository.Write("large.txt", strings.Repeat("x", (1<<20)+1))
	repository.Write("small.txt", "small")
	repository.Commit("initial")
	expectStatus(t, h.run(""), models.RunSucceeded)
	var config map[string]any
	_ = json.Unmarshal([]byte(h.task.SourceConfigJSON), &config)
	config["maxFileSizeMB"] = 1
	encoded, _ := json.Marshal(config)
	if err := h.db.Model(&models.Task{}).Where("id = ?", h.task.ID).Updates(map[string]any{"source_config_json": string(encoded), "config_revision": 2, "reconcile_required": true}).Error; err != nil {
		t.Fatal(err)
	}
	limited := h.run("")
	expectStatus(t, limited, models.RunSucceeded)
	if limited.DeletedCount != 1 || limited.UnchangedCount != 1 || len(h.dest.docs) != 1 {
		t.Fatalf("limit change=%+v docs=%d", limited, len(h.dest.docs))
	}
}

func TestGitTaskCleanupOnlyRemovesOwnedCache(t *testing.T) {
	h, repository, source := gitHarness(t)
	repository.Write("a.txt", "alpha")
	repository.Commit("initial")
	h.task.SourceConfigJSON = strings.Replace(h.task.SourceConfigJSON, `"keepCache":false`, `"keepCache":true`, 1)
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	expectStatus(t, h.run(""), models.RunSucceeded)
	outside := filepath.Join(source.CacheDir, "unrelated")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.CleanupTask(t.Context(), h.task.ID); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "unrelated" {
		t.Fatal("cleanup touched unrelated files or left the cache")
	}
}

package git

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
	"github.com/Nyrest/hindsight-ingestion/internal/testutil"
	"github.com/go-git/go-git/v5/plumbing"
)

func scan(t *testing.T, source *Source, cfg map[string]any) map[string]connectors.SourceItem {
	t.Helper()
	observed := map[string]connectors.SourceItem{}
	req := connectors.ScanRequest{TaskID: "task", Config: cfg, Credential: connectors.Credential{Proxy: proxy.Config{Type: "none"}}}
	req.Emit = func(item connectors.SourceItem) error {
		observed[item.Path] = item
		content, err := source.OpenContent(t.Context(), connectors.ContentRequest{Item: item, Config: cfg, MaxFileSize: 100 << 20})
		if errors.Is(err, connectors.ErrSkip) {
			return nil
		}
		if err != nil {
			return err
		}
		if content.Body != nil {
			_, err = io.Copy(io.Discard, content.Body)
			err = errors.Join(err, content.Body.Close())
		}
		return err
	}
	res, err := source.Scan(t.Context(), req)
	if err != nil || !res.Complete {
		t.Fatalf("scan=%+v err=%v", res, err)
	}
	var cursor map[string]any
	if err := json.Unmarshal(res.Cursor, &cursor); err != nil {
		t.Fatal(err)
	}
	return observed
}

func TestGitHTTPShallowInventoryAndCache(t *testing.T) {
	remote := testutil.NewGitRepository(t)
	remote.Write("README.md", "# hello")
	remote.Write("docs/报告.txt", "report")
	first := remote.Commit("initial")
	remote.Write("README.md", "# updated")
	remote.Commit("update")
	source := &Source{CacheDir: t.TempDir()}
	cfg := map[string]any{"repositoryUrl": remote.URL()}
	initial := scan(t, source, cfg)
	if len(initial) != 2 || initial["docs/报告.txt"].Size != 6 {
		t.Fatalf("inventory=%+v", initial)
	}
	dir, _ := source.taskPath("task")
	cache, err := openRepository(dir, sourceConfig{URL: remote.URL(), CacheLimit: 2 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.repo.CommitObject(plumbing.NewHash(first)); err == nil {
		t.Fatal("depth 1 fetched historical commit")
	}
	_ = cache.Close()
	remote.Write("README.md", "# next")
	remote.Commit("next")
	changed := scan(t, source, cfg)
	if initial["docs/报告.txt"].Revision != changed["docs/报告.txt"].Revision || initial["README.md"].Revision == changed["README.md"].Revision {
		t.Fatal("blob revisions do not isolate content changes")
	}
	if initial["README.md"].ID != changed["README.md"].ID {
		t.Fatal("file ID changed after content update")
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("repository was checked out")
	}
	cfg["keepCache"] = false
	scan(t, source, cfg)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary cache remains: %v", err)
	}
}

func TestGitRefsAndForcePush(t *testing.T) {
	remote := testutil.NewGitRepository(t)
	remote.Write("a.txt", "first")
	first := remote.Commit("first")
	remote.Command("tag", "v1.2.0")
	remote.Command("tag", "-a", "v1.10.0", "-m", "annotated")
	remote.Write("a.txt", "second")
	second := remote.Commit("second")
	remote.Command("tag", "v1.3.0")
	remote.Command("tag", "unversioned")
	source := &Source{CacheDir: t.TempDir()}
	cfg := map[string]any{"repositoryUrl": remote.URL(), "refType": "tag", "ref": "v1.10.0"}
	tagged := scan(t, source, cfg)
	cfg["refType"] = "latest_tag"
	cfg["tagPattern"] = "v*"
	latest := scan(t, source, cfg)
	if latest["a.txt"].Revision != tagged["a.txt"].Revision {
		t.Fatal("latest tag did not compare semantic versions")
	}
	cfg["refType"] = "commit"
	cfg["ref"] = second
	committed := scan(t, source, cfg)
	if committed["a.txt"].Revision == tagged["a.txt"].Revision {
		t.Fatal("commit SHA did not select requested snapshot")
	}
	cfg["refType"] = "branch"
	cfg["ref"] = "main"
	scan(t, source, cfg)
	remote.Command("reset", "--hard", first)
	forced := scan(t, source, cfg)
	if forced["a.txt"].Revision != tagged["a.txt"].Revision {
		t.Fatal("force push did not replace snapshot")
	}
}

func TestGitLimitsLFSAndFailureCompletion(t *testing.T) {
	remote := testutil.NewGitRepository(t)
	remote.Write("a.txt", "first")
	remote.Commit("first")
	source := &Source{CacheDir: t.TempDir()}
	cfg := map[string]any{"repositoryUrl": remote.URL(), "refType": "branch", "ref": "missing"}
	res, err := source.Scan(t.Context(), connectors.ScanRequest{TaskID: "task", Config: cfg})
	if err == nil || res.Complete {
		t.Fatalf("missing ref: %+v %v", res, err)
	}
	cfg["ref"] = "main"
	remote.Write("pointer.txt", "version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 200000000\n")
	random := make([]byte, 2<<20)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	remote.Write("large.txt", hex.EncodeToString(random))
	remote.Commit("large files")
	cfg["maxFileSizeMB"] = 1
	skipped := 0
	req := connectors.ScanRequest{TaskID: "task", Config: cfg, Emit: func(item connectors.SourceItem) error {
		_, err := source.OpenContent(t.Context(), connectors.ContentRequest{Config: cfg, Item: item})
		if errors.Is(err, connectors.ErrSkip) {
			skipped++
			return nil
		}
		return err
	}}
	res, err = source.Scan(t.Context(), req)
	if err != nil || !res.Complete || skipped != 1 {
		t.Fatalf("limits: %+v %v skipped=%d", res, err, skipped)
	}
	cfg["maxCacheSizeMB"] = 1
	res, err = source.Scan(t.Context(), req)
	if err == nil || res.Complete {
		t.Fatalf("cache cap: %+v %v", res, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, err = source.Scan(ctx, req)
	if !errors.Is(err, context.Canceled) || res.Complete {
		t.Fatalf("cancel: %+v %v", res, err)
	}
}

func TestGitEmptyAndCacheRecovery(t *testing.T) {
	remote := testutil.NewGitRepository(t)
	source := &Source{CacheDir: t.TempDir()}
	cfg := map[string]any{"repositoryUrl": remote.URL()}
	if files := scan(t, source, cfg); len(files) != 0 {
		t.Fatal(files)
	}
	remote.Write("a.txt", "content")
	remote.Commit("first")
	scan(t, source, cfg)
	dir, _ := source.taskPath("task")
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("broken config"), 0600); err != nil {
		t.Fatal(err)
	}
	scan(t, source, cfg)
	if err := os.RemoveAll(filepath.Join(dir, "objects", "pack")); err != nil {
		t.Fatal(err)
	}
	scan(t, source, cfg)
	if err := os.WriteFile(filepath.Join(dir, "ephemeral"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.RecoverCaches(t.Context(), []string{"task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted temporary cache was not removed")
	}
	scan(t, source, cfg)
	if err := source.RecoverCaches(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphan cache was not removed")
	}
}

func TestGitTrackedIgnoreSymlinksAndSubmodules(t *testing.T) {
	remote := testutil.NewGitRepository(t)
	remote.Write("a.txt", "hello")
	commit := remote.Commit("first")
	remote.Write(".gitignore", "*.txt\n")
	remote.Command("add", ".gitignore")
	remote.Write("target.txt", "a.txt")
	remote.Command("add", "--force", "target.txt")
	blob := remote.Command("hash-object", "target.txt")
	remote.Command("update-index", "--add", "--cacheinfo", "120000,"+blob+",link.txt")
	remote.Command("update-index", "--add", "--cacheinfo", "160000,"+commit+",submodule")
	remote.Command("rm", "--cached", "target.txt")
	remote.Command("commit", "-m", "links, submodule and tracked ignored file")
	listed := remote.Command("ls-tree", "HEAD")
	if !strings.Contains(listed, "120000") || !strings.Contains(listed, "160000") {
		t.Fatal("fixture did not contain both a symlink and submodule")
	}
	source := &Source{CacheDir: t.TempDir()}
	observed := scan(t, source, map[string]any{"repositoryUrl": remote.URL()})
	if _, ok := observed["a.txt"]; !ok {
		t.Fatal(".gitignore excluded an already tracked file")
	}
	if _, ok := observed["link.txt"]; ok {
		t.Fatal("symlink was emitted")
	}
	if _, ok := observed["submodule"]; ok {
		t.Fatal("submodule was emitted")
	}
}

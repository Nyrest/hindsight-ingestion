package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

func testCredential(base string) connectors.Credential {
	return connectors.Credential{Config: map[string]any{"baseUrl": base}}
}
func filesConfig() map[string]any {
	return map[string]any{"repository": "owner/repo", "includeIssues": false, "includePullRequests": false}
}
func emitScan(t *testing.T, cred connectors.Credential, cfg map[string]any, cur json.RawMessage, full bool) ([]connectors.SourceItem, connectors.ScanResult, error) {
	t.Helper()
	var items []connectors.SourceItem
	result, err := (&Source{}).Scan(context.Background(), connectors.ScanRequest{Credential: cred, Config: cfg, Cursor: cur, Full: full, Emit: func(i connectors.SourceItem) error { items = append(items, i); return nil }})
	return items, result, err
}
func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func entry(p, sha string) treeEntry {
	return treeEntry{Path: p, SHA: sha, Mode: "100644", Type: "blob", Size: 10}
}

func TestConfigAndCredential(t *testing.T) {
	s := &Source{}
	for _, repo := range []string{"", "owner", "owner/repo/extra", "../repo", "owner/repo?token=secret"} {
		cfg := filesConfig()
		cfg["repository"] = repo
		if s.ValidateConfig(cfg, connectors.Filter{}) == nil {
			t.Fatalf("accepted %q", repo)
		}
	}
	for _, f := range []string{"../outside", "/absolute", "a\\b"} {
		cfg := filesConfig()
		cfg["folder"] = f
		if s.ValidateConfig(cfg, connectors.Filter{}) == nil {
			t.Fatalf("accepted folder %q", f)
		}
	}
	cfg := filesConfig()
	cfg["includeFiles"] = false
	if s.ValidateConfig(cfg, connectors.Filter{}) == nil {
		t.Fatal("accepted no content")
	}
	if s.ValidateConfig(filesConfig(), connectors.Filter{Mode: "advanced", AdvancedQuery: "hello"}) == nil {
		t.Fatal("accepted advanced query")
	}
	for _, base := range []string{"https://user:secret@example.com", "file:///tmp", "https://example.com?token=secret", "https://example.com#fragment"} {
		if _, err := newClient(testCredential(base)); err == nil {
			t.Fatalf("accepted base %q", base)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Error("API version missing")
		}
		switch r.URL.Path {
		case "/api/v3/rate_limit":
			jsonResponse(w, map[string]any{"resources": map[string]any{}})
		case "/api/v3/user":
			if r.Header.Get("Authorization") != "Bearer valid" {
				w.WriteHeader(401)
				return
			}
			jsonResponse(w, map[string]string{"login": "octocat"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cred := testCredential(server.URL + "/api/v3/")
	if _, err := s.ValidateCredential(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	cred.Secrets = map[string]string{"token": "valid"}
	if name, err := s.ValidateCredential(context.Background(), cred); err != nil || !strings.Contains(name, "octocat") {
		t.Fatalf("%s %v", name, err)
	}
	cred.Secrets["token"] = "invalid"
	if _, err := s.ValidateCredential(context.Background(), cred); !httpx.IsStatus(err, 401) {
		t.Fatalf("bad token: %v", err)
	}
}

func TestPaginationAndRedirectBoundary(t *testing.T) {
	secretSent := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secretSent = true
		t.Error("followed external URL")
		jsonResponse(w, []int{3})
	}))
	defer other.Close()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/pages":
			if r.URL.Query().Get("page") == "2" {
				jsonResponse(w, []int{2})
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/pages?page=2>; rel="next"`, server.URL))
			jsonResponse(w, []int{1})
		case "/api/v3/loop":
			w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next"`, server.URL, r.URL.RequestURI()))
			jsonResponse(w, []int{1})
		case "/api/v3/external":
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, other.URL))
			jsonResponse(w, []int{1})
		case "/api/v3/outside":
			w.Header().Set("Link", fmt.Sprintf(`<%s/private>; rel="next"`, server.URL))
			jsonResponse(w, []int{1})
		case "/api/v3/traversal":
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/../private>; rel="next"`, server.URL))
			jsonResponse(w, []int{1})
		case "/api/v3/null":
			io.WriteString(w, "null")
		case "/api/v3/redirect":
			http.Redirect(w, r, other.URL, 302)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cred := testCredential(server.URL + "/api/v3")
	cred.Secrets = map[string]string{"token": "secret"}
	cred.Headers = map[string]string{"X-Secret": "secret"}
	c, err := newClient(cred)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Pace = nil
	c.http.MaxRetries = 0
	got, err := list[int](context.Background(), c, "pages", nil)
	if err != nil || !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("pages: %v %v", got, err)
	}
	for _, p := range []string{"loop", "external", "outside", "traversal", "null"} {
		if _, err := list[int](context.Background(), c, p, nil); err == nil {
			t.Fatalf("accepted %s", p)
		}
	}
	var out any
	if err := c.get(context.Background(), "redirect", nil, &out); err == nil {
		t.Fatal("followed redirect")
	}
	if secretSent {
		t.Fatal("external request sent")
	}
	if _, err := (&Source{}).Browse(context.Background(), testCredential(server.URL), connectors.BrowseRequest{Kind: connectors.KindRepository, Config: map[string]any{"repository": "../private"}}); err == nil {
		t.Fatal("accepted invalid repository while browsing")
	}
}

func TestSelectedFolderDeletion(t *testing.T) {
	head := "before"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo":
			jsonResponse(w, repository{ID: 42, FullName: "owner/repo", DefaultBranch: "main"})
		case "/repos/owner/repo/commits/main":
			jsonResponse(w, map[string]any{"commit": map[string]any{"tree": map[string]string{"sha": head}}})
		case "/repos/owner/repo/git/trees/before":
			jsonResponse(w, map[string]any{"tree": []treeEntry{{Path: "docs", Type: "tree", Mode: "040000", SHA: "docs-tree"}, entry("docs/readme.md", "readme")}})
		case "/repos/owner/repo/git/trees/after":
			jsonResponse(w, map[string]any{"tree": []treeEntry{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	cfg := filesConfig()
	cfg["folder"] = "docs"
	cred := testCredential(server.URL)
	before, first, err := emitScan(t, cred, cfg, nil, false)
	if err != nil || !first.Complete || len(before) != 1 {
		t.Fatalf("baseline: %v %v %v", before, first, err)
	}
	head = "after"
	removed, delta, err := emitScan(t, cred, cfg, first.Cursor, false)
	if err != nil || delta.Complete || len(removed) != 1 || !removed[0].Deleted || removed[0].ID != before[0].ID {
		t.Fatalf("folder deletion: %v %v %v", removed, delta, err)
	}
	remaining, full, err := emitScan(t, cred, cfg, delta.Cursor, true)
	if err != nil || !full.Complete || len(remaining) != 0 {
		t.Fatalf("full after deletion: %v %v %v", remaining, full, err)
	}
}

func TestRateLimitAndCancellation(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute).Unix()
	resp := &http.Response{StatusCode: 403, Header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(reset)}}}
	if retry, delay := retryResponse(resp, nil, 0); !retry || delay < 9*time.Minute {
		t.Fatalf("reset hint truncated: %t %s", retry, delay)
	}
	resp.Header = http.Header{"Retry-After": {"301"}}
	if retry, delay := retryResponse(resp, nil, 0); !retry || delay != 301*time.Second {
		t.Fatalf("retry hint: %t %s", retry, delay)
	}
	resp.Header = http.Header{}
	if retry, _ := retryResponse(resp, []byte("permission denied"), 0); retry {
		t.Fatal("permission error retried")
	}
	if retry, delay := retryResponse(resp, []byte("secondary rate limit"), 1); !retry || delay != 2*time.Minute {
		t.Fatal("secondary limit")
	}
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}))
	defer server.Close()
	c, _ := newClient(testCredential(server.URL))
	c.http.Pace = nil
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var out any
	if err := c.get(ctx, "limit", nil, &out); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestFilesPinnedIncrementalAndIgnoreChanges(t *testing.T) {
	head := "one"
	trees := map[string][]treeEntry{
		"one": {entry(".gitignore", "ignore1"), entry("a.go", "blob-a"), entry("gone.md", "blob-gone"), entry("blocked.md", "blob-blocked"), {Path: "link", SHA: "link", Mode: "120000", Type: "blob"}, {Path: "submodule", SHA: "commit", Mode: "160000", Type: "commit"}},
		"two": {entry(".gitignore", "ignore2"), entry("a.go", "blob-a"), entry("blocked.md", "blob-blocked"), entry("renamed.md", "blob-gone")},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/api/v3/repos/owner/repo")
		switch {
		case p == "":
			jsonResponse(w, repository{ID: 42, FullName: "owner/repo", HTMLURL: "https://github.com/owner/repo", DefaultBranch: "main"})
		case p == "/commits/main":
			jsonResponse(w, map[string]any{"commit": map[string]any{"tree": map[string]string{"sha": head}}})
		case strings.HasPrefix(p, "/git/trees/"):
			sha := strings.TrimPrefix(p, "/git/trees/")
			es, ok := trees[sha]
			if !ok {
				w.WriteHeader(404)
				return
			}
			jsonResponse(w, map[string]any{"tree": es})
		case strings.HasPrefix(p, "/git/blobs/"):
			if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
				t.Error("blob was not raw")
			}
			sha := strings.TrimPrefix(p, "/git/blobs/")
			switch sha {
			case "ignore1":
				io.WriteString(w, "blocked.md\n")
			case "ignore2":
				io.WriteString(w, "a.go\n")
			case "blob-a":
				io.WriteString(w, "package pinned\n")
			default:
				io.WriteString(w, sha)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cred := testCredential(server.URL + "/api/v3")
	cfg := filesConfig()
	items, first, err := emitScan(t, cred, cfg, nil, false)
	if err != nil || !first.Complete || len(items) != 3 {
		t.Fatalf("first: %v %v %v", items, first, err)
	}
	a := items[slices.IndexFunc(items, func(i connectors.SourceItem) bool { return i.Path == "a.go" })]
	head = "two"
	content, err := (&Source{}).OpenContent(context.Background(), connectors.ContentRequest{Credential: cred, Config: cfg, Item: a})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(content.Body)
	content.Body.Close()
	if err != nil || string(b) != "package pinned\n" {
		t.Fatalf("unpinned content %q %v", b, err)
	}
	items, second, err := emitScan(t, cred, cfg, first.Cursor, false)
	if err != nil || second.Complete {
		t.Fatalf("incremental: %v %v", second, err)
	}
	var deleted, present []string
	for _, i := range items {
		if i.Deleted {
			deleted = append(deleted, i.Path)
		} else {
			present = append(present, i.Path)
		}
	}
	sortStrings := func(v []string) []string { slices.Sort(v); return v }
	if !reflect.DeepEqual(sortStrings(deleted), []string{"a.go", "gone.md"}) || !reflect.DeepEqual(sortStrings(present), []string{".gitignore", "blocked.md", "renamed.md"}) {
		t.Fatalf("deleted %v present %v", deleted, present)
	}
	items, third, err := emitScan(t, cred, cfg, second.Cursor, false)
	if err != nil || third.Complete || len(items) != 3 {
		t.Fatalf("unchanged %v %v", third, err)
	}
	delete(trees, "two")
	head = "one"
	_, fallback, err := emitScan(t, cred, cfg, second.Cursor, false)
	if err != nil || !fallback.Complete {
		t.Fatalf("old tree fallback: %v %v", fallback, err)
	}
}

func TestTreeTruncation(t *testing.T) {
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sha := filepath.Base(r.URL.Path)
		if r.URL.Query().Get("recursive") != "" {
			jsonResponse(w, map[string]any{"truncated": true, "tree": []treeEntry{entry("partial.md", "x")}})
			return
		}
		if sha == "root" {
			jsonResponse(w, map[string]any{"tree": []treeEntry{{Path: "docs", Type: "tree", Mode: "040000", SHA: "child"}, entry("a.md", "a")}})
			return
		}
		jsonResponse(w, map[string]any{"truncated": fail, "tree": []treeEntry{entry("b.md", "b")}})
	}))
	defer server.Close()
	c, _ := newClient(testCredential(server.URL))
	c.http.Pace = nil
	es, err := readTree(context.Background(), c, "repos/owner/repo", "root")
	if err != nil || len(es) != 3 || es[2].Path != "docs/b.md" {
		t.Fatalf("tree %v %v", es, err)
	}
	fail = true
	if _, err := readTree(context.Background(), c, "repos/owner/repo", "root"); err == nil {
		t.Fatal("accepted incomplete fallback")
	}
}

func TestLFSAndContentLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "version https://git-lfs.github.com/spec/v1\noid sha256:abcdef\nsize 10\n")
	}))
	defer server.Close()
	opaque, _ := json.Marshal(contentData{Blob: "lfs"})
	req := connectors.ContentRequest{Credential: testCredential(server.URL), Config: filesConfig(), Item: connectors.SourceItem{Kind: connectors.KindFile, Opaque: opaque}}
	if _, err := (&Source{}).OpenContent(context.Background(), req); !errors.Is(err, connectors.ErrSkip) || !strings.Contains(err.Error(), "LFS") {
		t.Fatal(err)
	}
	req.Item.Size = 101 << 20
	if _, err := (&Source{}).OpenContent(context.Background(), req); !errors.Is(err, connectors.ErrSkip) {
		t.Fatal(err)
	}
	req.Item.Kind = connectors.KindDocument
	req.Item.Opaque, _ = json.Marshal(contentData{Text: "123456"})
	req.MaxFileSize = 5
	if _, err := (&Source{}).OpenContent(context.Background(), req); err == nil || errors.Is(err, connectors.ErrSkip) {
		t.Fatal("oversize discussion must fail without deleting previous content")
	}
}

func TestGitignoreAgainstGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("Git required for ignore parity")
	}
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", b, err)
	}
	rules := map[string]string{".gitignore": "*.log\n!keep.log\nblocked/\n!blocked/keep.md\n/build/\n**/cache/**\n\\#secret\n\\!literal\nspace\\ \ntrailing   \ndocs/*.tmp\n!docs/keep.tmp\n", "docs/.gitignore": "!allowed.log\nkeep.tmp\n*.txt\n!keep.txt\n"}
	paths := []string{"root.log", "keep.log", "blocked/keep.md", "build/a.md", "docs/build/a.md", "docs/cache/a.md", "#secret", "!literal", "space ", "trailing", "docs/a.tmp", "docs/keep.tmp", "docs/allowed.log", "docs/a.txt", "docs/keep.txt", "normal.go"}
	for name, body := range rules {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Use stdin so paths remain literal even on Windows and tracked files are checked.
	cmd = exec.Command("git", "-C", root, "-c", "core.excludesFile=", "check-ignore", "--no-index", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ignore: %s %v", b, err)
	}
	want := map[string]bool{}
	for _, p := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		want[strings.TrimSuffix(p, "\r")] = true
	}
	var entries []treeEntry
	directories := map[string]bool{}
	for _, p := range append(append([]string(nil), paths...), ".gitignore", "docs/.gitignore") {
		entries = append(entries, entry(p, p))
		for dir := pathDir(p); dir != "."; dir = pathDir(dir) {
			directories[dir] = true
		}
	}
	for dir := range directories {
		entries = append(entries, treeEntry{Path: dir, SHA: dir, Type: "tree", Mode: "040000"})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/trees/") {
			jsonResponse(w, map[string]any{"tree": entries})
			return
		}
		sha := strings.SplitN(r.URL.Path, "/git/blobs/", 2)[1]
		io.WriteString(w, rules[sha])
	}))
	defer server.Close()
	c, _ := newClient(testCredential(server.URL))
	c.http.Pace = nil
	items, err := fileInventory(context.Background(), fileScan{client: c, config: filesConfig(), repo: repository{ID: 1}, ref: "main", tree: "root"})
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	for _, i := range items {
		present[i.Path] = true
	}
	for _, p := range paths {
		if present[p] == want[p] {
			t.Errorf("Git parity for %q: present=%t Git ignored=%t", p, present[p], want[p])
		}
	}
	cfg := filesConfig()
	cfg["folder"] = "docs"
	items, err = fileInventory(context.Background(), fileScan{client: c, config: cfg, repo: repository{ID: 1}, ref: "main", tree: "root"})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range items {
		if !strings.HasPrefix(i.Path, "docs/") || want[i.Path] {
			t.Errorf("folder/ancestor rule: %s", i.Path)
		}
	}
	cfg["respectGitignore"] = false
	items, err = fileInventory(context.Background(), fileScan{client: c, config: cfg, repo: repository{ID: 1}, ref: "main", tree: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(items, func(i connectors.SourceItem) bool { return i.Path == "docs/a.txt" }) {
		t.Fatal("ignore toggle")
	}
}

func pathDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

func TestDiscussionContentAndCommentDelta(t *testing.T) {
	body := "first comment"
	deleted := false
	var server *httptest.Server
	sinceSeen := ""
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		root := server.URL + "/api/v3/repos/owner/repo"
		makeIssue := func(n int) issue {
			i := issue{Number: n, Title: fmt.Sprintf("Thread %d", n), Body: "description ![attachment](https://example.com/image.png)", State: "closed", HTMLURL: "https://github.com/owner/repo/issues/" + fmt.Sprint(n), User: user{Login: "author"}, UpdatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
			if n == 2 {
				i.PullRequest = &struct{}{}
			}
			return i
		}
		p := strings.TrimPrefix(r.URL.Path, "/api/v3/repos/owner/repo")
		switch p {
		case "":
			jsonResponse(w, repository{ID: 42, FullName: "owner/repo"})
		case "/issues":
			sinceSeen = r.URL.Query().Get("since")
			if sinceSeen != "" {
				jsonResponse(w, []issue{})
				return
			}
			if deleted {
				jsonResponse(w, []issue{makeIssue(2)})
				return
			}
			jsonResponse(w, []issue{makeIssue(1), makeIssue(2)})
		case "/issues/comments":
			jsonResponse(w, []comment{{ID: 9, IssueURL: root + "/issues/1", Body: body}})
		case "/pulls/comments":
			jsonResponse(w, []comment{{ID: 10, PullRequestURL: root + "/pulls/2"}})
		case "/issues/1":
			jsonResponse(w, makeIssue(1))
		case "/issues/2":
			jsonResponse(w, makeIssue(2))
		case "/issues/1/comments":
			if deleted {
				jsonResponse(w, []comment{})
				return
			}
			if r.URL.Query().Get("page") == "2" {
				jsonResponse(w, []comment{{ID: 11, Body: "second page"}})
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s/issues/1/comments?page=2>; rel="next"`, root))
			jsonResponse(w, []comment{{ID: 9, Body: body, User: user{Login: "commenter"}}})
		case "/issues/2/comments":
			jsonResponse(w, []comment{{ID: 12, Body: "PR ordinary comment"}})
		case "/pulls/2":
			jsonResponse(w, map[string]any{"merged": true, "head": map[string]string{"ref": "feature"}, "base": map[string]string{"ref": "main"}})
		case "/pulls/2/reviews":
			jsonResponse(w, []comment{{ID: 20, Body: "review summary", State: "APPROVED", User: user{Login: "reviewer"}}})
		case "/pulls/2/comments":
			jsonResponse(w, []map[string]any{{"id": 21, "body": "inline feedback", "path": "a.go", "line": 3, "in_reply_to_id": 19, "diff_hunk": "MUST NOT INGEST DIFF"}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cred := testCredential(server.URL + "/api/v3")
	cfg := map[string]any{"repository": "owner/repo", "includeFiles": false}
	items, first, err := emitScan(t, cred, cfg, nil, true)
	if err != nil || !first.Complete || len(items) != 2 {
		t.Fatalf("first %v %v", first, err)
	}
	var pr contentData
	json.Unmarshal(items[1].Opaque, &pr)
	for _, fragment := range []string{"PR ordinary comment", "review summary", "inline feedback", "Line: 3", "Reply to: 19", "merged", "feature → main"} {
		if !strings.Contains(pr.Text, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
	if strings.Contains(pr.Text, "MUST NOT INGEST DIFF") {
		t.Fatal("included diff")
	}
	var old cursor
	json.Unmarshal(first.Cursor, &old)
	body = "edited comment"
	changed, next, err := emitScan(t, cred, cfg, first.Cursor, false)
	if err != nil || next.Complete || len(changed) != 2 {
		t.Fatalf("delta %v %v", next, err)
	}
	if changed[0].Revision == items[0].Revision {
		t.Fatal("comment edit did not change hash")
	}
	if sinceSeen != old.Watermark.Add(-5*time.Minute).Format(time.RFC3339) {
		t.Fatalf("wrong watermark overlap: %s", sinceSeen)
	}
	if changed[1].Revision != items[1].Revision {
		t.Fatal("unchanged PR hash changed")
	}
	deleted = true
	remaining, full, err := emitScan(t, cred, cfg, next.Cursor, true)
	if err != nil || !full.Complete || len(remaining) != 1 {
		t.Fatalf("full deletion %v %v", full, err)
	}
	if remaining[0].ID != items[1].ID {
		t.Fatal("wrong discussion remained")
	}
}

func TestScanFailureEmitsNothing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/owner/repo"):
			jsonResponse(w, repository{ID: 1, FullName: "owner/repo", DefaultBranch: "main"})
		case strings.Contains(r.URL.Path, "/commits/"):
			jsonResponse(w, map[string]any{"commit": map[string]any{"tree": map[string]string{"sha": "root"}}})
		case strings.Contains(r.URL.Path, "/git/trees/"):
			jsonResponse(w, map[string]any{"tree": []treeEntry{entry(".gitignore", "ignore"), entry("a.md", "a")}})
		default:
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	items, result, err := emitScan(t, testCredential(server.URL), filesConfig(), nil, true)
	if err == nil || result.Complete || len(result.Cursor) != 0 || len(items) != 0 {
		t.Fatalf("unsafe failed scan: %v %v %v", items, result, err)
	}
}

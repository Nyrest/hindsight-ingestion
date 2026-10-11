package github

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	gitignore "github.com/denormal/go-gitignore"
)

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

func resolveTree(ctx context.Context, c *client, cfg map[string]any, repo repository) (string, string, error) {
	ref := connectors.AsString(cfg["ref"])
	if ref == "" {
		ref = repo.DefaultBranch
	}
	if ref == "" {
		return "", "", errors.New("GitHub repository has no default branch; select a ref")
	}
	var commit struct {
		Commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	if err := c.get(ctx, repoPath(cfg)+"/commits/"+url.PathEscape(ref), nil, &commit); err != nil {
		return "", "", err
	}
	if commit.Commit.Tree.SHA == "" {
		return "", "", errors.New("GitHub commit has no tree")
	}
	return ref, commit.Commit.Tree.SHA, nil
}

func readTree(ctx context.Context, c *client, repo, sha string) ([]treeEntry, error) {
	type result struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	var out result
	if err := c.get(ctx, repo+"/git/trees/"+url.PathEscape(sha), url.Values{"recursive": {"1"}}, &out); err != nil {
		return nil, err
	}
	if !out.Truncated && out.Tree == nil {
		return nil, errors.New("GitHub tree response is missing its entries")
	}
	if out.Truncated {
		out.Tree = nil
		var walk func(string, string) error
		walk = func(prefix, sha string) error {
			var local result
			if err := c.get(ctx, repo+"/git/trees/"+url.PathEscape(sha), nil, &local); err != nil {
				return err
			}
			if local.Truncated {
				return errors.New("GitHub non-recursive tree was truncated")
			}
			if local.Tree == nil {
				return errors.New("GitHub tree response is missing its entries")
			}
			for _, entry := range local.Tree {
				if path.Base(entry.Path) != entry.Path || entry.Path == "." || entry.Path == ".." {
					return errors.New("invalid GitHub tree entry")
				}
				entry.Path = path.Join(prefix, entry.Path)
				out.Tree = append(out.Tree, entry)
				if entry.Type == "tree" {
					if err := walk(entry.Path, entry.SHA); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := walk("", sha); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	directories := map[string]bool{".": true}
	for _, e := range out.Tree {
		if e.Path == "" || path.Clean(e.Path) != e.Path || strings.HasPrefix(e.Path, "/") || e.Path == ".." || strings.HasPrefix(e.Path, "../") || strings.ContainsRune(e.Path, '\x00') || seen[e.Path] || e.SHA == "" || e.Size < 0 {
			return nil, errors.New("invalid or duplicate GitHub tree entry")
		}
		seen[e.Path] = true
		if e.Type == "tree" {
			directories[e.Path] = true
		}
	}
	for _, e := range out.Tree {
		if !directories[path.Dir(e.Path)] {
			return nil, errors.New("GitHub tree response is missing a parent directory")
		}
	}
	sort.Slice(out.Tree, func(i, j int) bool { return out.Tree[i].Path < out.Tree[j].Path })
	return out.Tree, nil
}

type ignoreFile struct {
	dir   string
	rules gitignore.GitIgnore
}

func ignored(p string, dir bool, stack []ignoreFile) bool {
	ignored := false
	for _, f := range stack {
		rel := p
		if f.dir != "." {
			rel = strings.TrimPrefix(p, f.dir+"/")
		}
		if match := f.rules.Relative(rel, dir); match != nil {
			ignored = match.Ignore()
		}
	}
	return ignored
}

type fileScan struct {
	client    *client
	config    map[string]any
	repo      repository
	ref, tree string
	log       func(string, string)
	dirs      map[string][]treeEntry
	items     []connectors.SourceItem
}

func fileInventory(ctx context.Context, scan fileScan) ([]connectors.SourceItem, error) {
	entries, err := readTree(ctx, scan.client, repoPath(scan.config), scan.tree)
	if err != nil {
		return nil, err
	}
	scan.dirs = map[string][]treeEntry{}
	exists := folder(scan.config) == "."
	for _, e := range entries {
		scan.dirs[path.Dir(e.Path)] = append(scan.dirs[path.Dir(e.Path)], e)
		if e.Path == folder(scan.config) && e.Type == "tree" {
			exists = true
		}
	}
	if !exists {
		// The complete tree proves that a formerly selected folder was deleted.
		return nil, nil
	}
	if err := scan.walk(ctx, ".", nil); err != nil {
		return nil, err
	}
	return scan.items, nil
}

func (s *fileScan) walk(ctx context.Context, dir string, stack []ignoreFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if enabled(s.config, "respectGitignore") {
		for _, e := range s.dirs[dir] {
			if path.Base(e.Path) != ".gitignore" || !regularBlob(e) {
				continue
			}
			rules, err := readIgnore(ctx, s.client, repoPath(s.config), e)
			if err != nil {
				return err
			}
			stack = append(append([]ignoreFile(nil), stack...), ignoreFile{dir, rules})
		}
	}
	for _, e := range s.dirs[dir] {
		if err := s.visit(ctx, e, stack); err != nil {
			return err
		}
	}
	return nil
}

func (s *fileScan) visit(ctx context.Context, e treeEntry, stack []ignoreFile) error {
	if enabled(s.config, "respectGitignore") && ignored(e.Path, e.Type == "tree", stack) {
		s.logItem("debug", "Excluded by .gitignore: ", e.Path)
		return nil
	}
	base := folder(s.config)
	if e.Type == "tree" {
		if base == "." || e.Path == base || strings.HasPrefix(e.Path, base+"/") || strings.HasPrefix(base, e.Path+"/") {
			return s.walk(ctx, e.Path, stack)
		}
		return nil
	}
	if base != "." && !strings.HasPrefix(e.Path, base+"/") {
		return nil
	}
	if !regularBlob(e) {
		s.logItem("info", "Skipped symbolic link or submodule: ", e.Path)
		return nil
	}
	s.items = append(s.items, s.fileItem(e))
	return nil
}

func (s *fileScan) logItem(level, reason, p string) {
	if s.log != nil {
		s.log(level, reason+p)
	}
}
func regularBlob(e treeEntry) bool {
	return e.Type == "blob" && (e.Mode == "100644" || e.Mode == "100755")
}

func (s *fileScan) fileItem(e treeEntry) connectors.SourceItem {
	md := metadata(s.client, s.repo, "file")
	md["github_ref"], md["github_path"] = s.ref, e.Path
	md["url"] = s.repo.HTMLURL + "/blob/" + url.PathEscape(s.ref) + "/" + escapePath(e.Path)
	opaque, _ := json.Marshal(contentData{Blob: e.SHA})
	return connectors.SourceItem{ID: itemID(s.repo, "file", url.QueryEscape(s.ref)+":"+url.QueryEscape(e.Path)), Name: path.Base(e.Path), Path: e.Path, Kind: connectors.KindFile, Revision: e.SHA, Size: e.Size, MIMEType: fileMIME(e.Path), Metadata: md, Attributes: map[string]any{"contentType": "file", "modifiedAt": nil}, Opaque: opaque}
}

func readIgnore(ctx context.Context, c *client, repo string, e treeEntry) (gitignore.GitIgnore, error) {
	resp, err := c.rawBlob(ctx, repo, e.SHA)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", e.Path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("%s exceeds the 1 MB ignore-rule limit", e.Path)
	}
	var parseErr error
	rules := gitignore.New(strings.NewReader(string(data)), ".", func(e gitignore.Error) bool { parseErr = e.Underlying(); return false })
	if parseErr != nil {
		return nil, fmt.Errorf("parse %s: %w", e.Path, parseErr)
	}
	return rules, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func fileMIME(p string) string {
	name := path.Base(p)
	switch name {
	case "Dockerfile", "Containerfile", "Makefile", "LICENSE", ".gitignore", ".gitattributes", ".editorconfig":
		return "text/plain"
	}
	mt := mime.TypeByExtension(strings.ToLower(path.Ext(p)))
	if connectors.FileGroup(name, mt) == connectors.GroupPlainText {
		return "text/plain; charset=utf-8"
	}
	return mt
}

func (c *client) rawBlob(ctx context.Context, repo, sha string) (*http.Response, error) {
	h := *c.http
	h.AuthHeaders = maps.Clone(c.http.AuthHeaders)
	h.AuthHeaders["Accept"] = "application/vnd.github.raw+json"
	return h.Do(ctx, httpx.Request{Method: http.MethodGet, URL: c.endpoint(repo+"/git/blobs/"+url.PathEscape(sha), nil)})
}

type contentData struct {
	Blob string `json:"blob,omitempty"`
	Text string `json:"text,omitempty"`
}
type bufferedBody struct {
	*bufio.Reader
	io.Closer
}

func (*Source) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	if err := ctx.Err(); err != nil {
		return connectors.SourceContent{}, err
	}
	var data contentData
	if err := json.Unmarshal(req.Item.Opaque, &data); err != nil {
		return connectors.SourceContent{}, err
	}
	if req.Item.Kind != connectors.KindFile {
		if req.MaxFileSize > 0 && int64(len(data.Text)) > req.MaxFileSize {
			return connectors.SourceContent{}, errors.New("GitHub discussion exceeds the configured content size limit")
		}
		return connectors.SourceContent{Text: data.Text, Timestamp: req.Item.ModifiedAt, Context: req.Item.Metadata["url"]}, nil
	}
	if req.Item.Size > 100<<20 {
		return connectors.SourceContent{}, connectors.Skip("GitHub Blob API supports files up to 100 MB")
	}
	c, err := newClient(req.Credential)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	resp, err := c.rawBlob(ctx, repoPath(req.Config), data.Blob)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	reader := bufio.NewReader(resp.Body)
	prefix, err := reader.Peek(200)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		resp.Body.Close()
		return connectors.SourceContent{}, err
	}
	if strings.HasPrefix(string(prefix), "version https://git-lfs.github.com/spec/v1\n") || strings.HasPrefix(string(prefix), "version https://git-lfs.github.com/spec/v1\r\n") {
		resp.Body.Close()
		return connectors.SourceContent{}, connectors.Skip("Git LFS pointer; downloading LFS objects is not supported")
	}
	return connectors.SourceContent{Body: &bufferedBody{reader, resp.Body}, FileName: req.Item.Name, MIMEType: req.Item.MIMEType, Size: req.Item.Size, Context: req.Item.Metadata["url"]}, nil
}

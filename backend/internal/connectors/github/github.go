// Package github reads repository files and complete Issue/PR discussions.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

type Source struct{}

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{Type: "github", Name: "GitHub", Description: "GitHub repositories via a read-only PAT, or anonymous public access.", Fields: []connectors.FieldSpec{
		{Key: "baseUrl", Label: "API base URL", Type: connectors.FieldURL, Default: defaultBaseURL, Help: "GitHub REST API base URL. For Enterprise Server, include /api/v3."},
		{Key: "token", Label: "Personal access token", Type: connectors.FieldPassword, Secret: true, Help: "Optional for public repositories. Select Contents, Issues and Pull requests read permissions for the content you sync."},
	}})
	connectors.RegisterSource(&Source{})
}

func (*Source) Info() connectors.SourceInfo {
	return connectors.SourceInfo{Type: "github", Name: "GitHub", CredentialType: "github", Capabilities: connectors.Capabilities{SupportsFiles: true, IncrementalMode: connectors.IncrementalHighWater, DeletionMode: connectors.DeletionFullReconcile}, BrowseKinds: []string{connectors.KindRepository, connectors.KindFolder}, Fields: []connectors.FieldSpec{
		{Key: "repository", Label: "Repository", Type: connectors.FieldString, Required: true, Placeholder: "owner/repo", Browse: true, BrowseKind: connectors.KindRepository, Effect: connectors.EffectRebaseline},
		{Key: "ref", Label: "Branch, tag or commit", Type: connectors.FieldString, Effect: connectors.EffectRebaseline, Help: "Leave empty to follow the default branch. Applies to repository files only."},
		{Key: "folder", Label: "Folder", Type: connectors.FieldString, Default: ".", Browse: true, BrowseKind: connectors.KindFolder, Effect: connectors.EffectReconcile, Help: "Repository-relative folder. Applies to files only; includes sub-folders."},
		{Key: "includeFiles", Label: "Repository files", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
		{Key: "includeIssues", Label: "Issues and comments", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
		{Key: "includePullRequests", Label: "Pull requests and reviews", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
		{Key: "respectGitignore", Label: "Apply .gitignore", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile, Help: "Exclude matching committed files using root and nested .gitignore rules. Does not affect Issues or pull requests."},
	}, FilterFields: []connectors.FilterFieldSpec{
		{Key: "contentType", Label: "Content type", Type: connectors.FilterEnum, Operators: connectors.EnumOps, Options: []connectors.Option{{Value: "file", Label: "File"}, {Value: "issue", Label: "Issue"}, {Value: "pull_request", Label: "Pull request"}}},
		{Key: "name", Label: "Name", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "extension", Label: "Extension", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "size", Label: "Size (bytes)", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
		{Key: "state", Label: "State", Type: connectors.FilterEnum, Operators: connectors.EnumOps, Options: []connectors.Option{{Value: "open", Label: "Open"}, {Value: "closed", Label: "Closed"}, {Value: "merged", Label: "Merged"}}},
		{Key: "author", Label: "Author", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "labels", Label: "Labels", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "modifiedAt", Label: "Updated", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
	}}
}

func (*Source) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	c, err := newClient(cred)
	if err != nil {
		return "", err
	}
	if cred.String("token") == "" {
		var out map[string]any
		err := c.get(ctx, "rate_limit", nil, &out)
		return "Anonymous GitHub API access; public repositories only", err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := c.get(ctx, "user", nil, &user); err != nil {
		return "", err
	}
	return "Connected as " + user.Login, nil
}

func (s *Source) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	if err := validateRepository(cfg); err != nil {
		return err
	}
	f := folder(cfg)
	if !fs.ValidPath(f) || strings.ContainsAny(f, "\\\x00") {
		return &connectors.FieldError{Field: "folder", Message: "Folder must be a repository-relative path"}
	}
	if strings.ContainsRune(connectors.AsString(cfg["ref"]), '\x00') {
		return &connectors.FieldError{Field: "ref", Message: "Invalid ref"}
	}
	if !enabled(cfg, "includeFiles") && !enabled(cfg, "includeIssues") && !enabled(cfg, "includePullRequests") {
		return &connectors.FieldError{Field: "includeFiles", Message: "Select at least one content type"}
	}
	return connectors.ValidateFilter(filter, s.Info().FilterFields, false)
}

func validateRepository(cfg map[string]any) error {
	parts := strings.Split(connectors.AsString(cfg["repository"]), "/")
	if len(parts) != 2 || !validRepoPart(parts[0]) || !validRepoPart(parts[1]) {
		return &connectors.FieldError{Field: "repository", Message: "Repository must be owner/repo"}
	}
	return nil
}

func validRepoPart(v string) bool {
	if v == "" || v == "." || v == ".." {
		return false
	}
	for _, ch := range v {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
			return false
		}
	}
	return true
}

func enabled(cfg map[string]any, key string) bool { return connectors.BoolDefault(cfg, key, true) }
func folder(cfg map[string]any) string {
	if f := connectors.AsString(cfg["folder"]); f != "" {
		return f
	}
	return "."
}
func repoPath(cfg map[string]any) string { return "repos/" + connectors.AsString(cfg["repository"]) }

type repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
}

func loadRepo(ctx context.Context, c *client, cfg map[string]any) (repository, error) {
	if err := validateRepository(cfg); err != nil {
		return repository{}, err
	}
	var repo repository
	err := c.get(ctx, repoPath(cfg), nil, &repo)
	if err == nil && (repo.ID == 0 || repo.FullName == "") {
		err = errors.New("GitHub returned an invalid repository")
	}
	return repo, err
}

func (*Source) Browse(ctx context.Context, cred connectors.Credential, req connectors.BrowseRequest) (connectors.BrowseResult, error) {
	c, err := newClient(cred)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	res := connectors.BrowseResult{Items: []connectors.BrowseItem{}, Breadcrumbs: []connectors.Breadcrumb{{ID: "", Name: "Repositories"}}}
	if req.Kind == connectors.KindRepository {
		var repos []repository
		if cred.String("token") == "" {
			if connectors.AsString(req.Config["repository"]) == "" {
				return res, nil
			}
			r, err := loadRepo(ctx, c, req.Config)
			if err != nil {
				return res, err
			}
			repos = []repository{r}
		} else {
			repos, err = list[repository](ctx, c, "user/repos", url.Values{"sort": {"full_name"}})
			if err != nil {
				return res, err
			}
		}
		for _, r := range repos {
			res.Items = append(res.Items, connectors.BrowseItem{ID: r.FullName, Name: r.FullName, Path: r.FullName, Kind: connectors.KindRepository, Selectable: true})
		}
		return res, nil
	}
	if req.Kind != connectors.KindFolder {
		return res, errors.New("unsupported GitHub browse kind")
	}
	if err := (&Source{}).ValidateConfig(req.Config, connectors.Filter{}); err != nil {
		return res, err
	}
	repo, err := loadRepo(ctx, c, req.Config)
	if err != nil {
		return res, err
	}
	_, tree, err := resolveTree(ctx, c, req.Config, repo)
	if err != nil {
		return res, err
	}
	entries, err := readTree(ctx, c, repoPath(req.Config), tree)
	if err != nil {
		return res, err
	}
	parent := req.ParentID
	if parent == "" {
		parent = "."
	}
	if !fs.ValidPath(parent) {
		return res, errors.New("invalid GitHub folder")
	}
	res.ParentID = parent
	res.Breadcrumbs = []connectors.Breadcrumb{{ID: ".", Name: "Root"}}
	res.Items = append(res.Items, connectors.BrowseItem{ID: parent, Name: ".", Path: parent, Kind: connectors.KindFolder, Selectable: true})
	if parent != "." {
		p := ""
		for _, part := range strings.Split(parent, "/") {
			p = path.Join(p, part)
			res.Breadcrumbs = append(res.Breadcrumbs, connectors.Breadcrumb{ID: p, Name: part})
		}
	}
	for _, e := range entries {
		if e.Type == "tree" && path.Dir(e.Path) == parent {
			res.Items = append(res.Items, connectors.BrowseItem{ID: e.Path, Name: path.Base(e.Path), Path: e.Path, Kind: connectors.KindFolder, HasChildren: true, Selectable: true})
		}
	}
	return res, nil
}

type cursor struct {
	Version      int       `json:"version"`
	BaseURL      string    `json:"baseUrl"`
	RepositoryID int64     `json:"repositoryId"`
	Ref          string    `json:"ref"`
	Tree         string    `json:"tree"`
	Watermark    time.Time `json:"watermark"`
}

func (s *Source) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	if err := s.ValidateConfig(req.Config, req.Filter); err != nil {
		return connectors.ScanResult{}, err
	}
	c, err := newClient(req.Credential)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	started := time.Now().UTC()
	repo, err := loadRepo(ctx, c, req.Config)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	var old cursor
	full := req.Full || json.Unmarshal(req.Cursor, &old) != nil || old.Version != 1 || old.BaseURL != c.base.String() || old.RepositoryID != repo.ID || old.Watermark.IsZero()
	cur := cursor{Version: 1, BaseURL: c.base.String(), RepositoryID: repo.ID, Watermark: started}
	var items []connectors.SourceItem
	if enabled(req.Config, "includeFiles") {
		cur.Ref, cur.Tree, err = resolveTree(ctx, c, req.Config, repo)
		if err != nil {
			return connectors.ScanResult{}, err
		}
		if cur.Ref != old.Ref {
			full = true
		}
		files, err := fileInventory(ctx, fileScan{client: c, config: req.Config, repo: repo, ref: cur.Ref, tree: cur.Tree, log: req.Log})
		if err != nil {
			return connectors.ScanResult{}, err
		}
		items = append(items, files...)
		if !full && old.Tree != cur.Tree {
			previous, err := fileInventory(ctx, fileScan{client: c, config: req.Config, repo: repo, ref: old.Ref, tree: old.Tree})
			if err != nil {
				full = true
				if req.Log != nil {
					req.Log("warn", "Previous GitHub tree unavailable; performing full reconciliation")
				}
			} else {
				present := map[string]bool{}
				for _, f := range files {
					present[f.ID] = true
				}
				for _, f := range previous {
					if !present[f.ID] {
						f.Deleted = true
						items = append(items, f)
					}
				}
			}
		}
	}
	if enabled(req.Config, "includeIssues") || enabled(req.Config, "includePullRequests") {
		var since time.Time
		if !full {
			since = old.Watermark.Add(-5 * time.Minute)
		}
		discussions, err := discussionInventory(ctx, discussionScan{client: c, config: req.Config, repo: repo}, since)
		if err != nil {
			return connectors.ScanResult{}, err
		}
		items = append(items, discussions...)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return connectors.ScanResult{}, err
		}
		if err := req.Emit(item); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	b, _ := json.Marshal(cur)
	return connectors.ScanResult{Cursor: b, Complete: full}, nil
}

func itemID(repo repository, kind, suffix string) string {
	return strconv.FormatInt(repo.ID, 10) + ":" + kind + ":" + suffix
}
func metadata(c *client, repo repository, kind string) map[string]string {
	return map[string]string{"github_api_base": c.base.String(), "github_repository_id": strconv.FormatInt(repo.ID, 10), "github_repository": repo.FullName, "github_content_type": kind}
}

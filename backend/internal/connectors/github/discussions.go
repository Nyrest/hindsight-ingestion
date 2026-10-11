package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

type user struct {
	Login string `json:"login"`
}
type issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	User      user      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct{} `json:"pull_request"`
}
type comment struct {
	ID             int64     `json:"id"`
	Body           string    `json:"body"`
	HTMLURL        string    `json:"html_url"`
	IssueURL       string    `json:"issue_url"`
	PullRequestURL string    `json:"pull_request_url"`
	User           user      `json:"user"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	SubmittedAt    time.Time `json:"submitted_at"`
	State          string    `json:"state"`
	Path           string    `json:"path"`
	Line           *int      `json:"line"`
	OriginalLine   *int      `json:"original_line"`
	StartLine      *int      `json:"start_line"`
	InReplyToID    int64     `json:"in_reply_to_id"`
}
type pull struct {
	Merged bool `json:"merged"`
	Draft  bool `json:"draft"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type discussionScan struct {
	client *client
	config map[string]any
	repo   repository
}

func discussionInventory(ctx context.Context, scan discussionScan, since time.Time) ([]connectors.SourceItem, error) {
	candidates, err := scan.candidates(ctx, since)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(candidates))
	for n := range candidates {
		ids = append(ids, n)
	}
	sort.Ints(ids)
	var items []connectors.SourceItem
	for _, n := range ids {
		i := candidates[n]
		isPR := i.PullRequest != nil
		if isPR && !enabled(scan.config, "includePullRequests") || !isPR && !enabled(scan.config, "includeIssues") {
			continue
		}
		item, err := scan.read(ctx, i)
		if err != nil {
			return nil, fmt.Errorf("read discussion #%d: %w", n, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func (scan discussionScan) candidates(ctx context.Context, since time.Time) (map[int]issue, error) {
	q := url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"asc"}}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	issues, err := list[issue](ctx, scan.client, repoPath(scan.config)+"/issues", q)
	if err != nil {
		return nil, err
	}
	candidates := map[int]issue{}
	for _, i := range issues {
		if i.Number <= 0 {
			return nil, fmt.Errorf("GitHub returned an invalid discussion number")
		}
		candidates[i.Number] = i
	}
	if since.IsZero() {
		return candidates, nil
	}
	return scan.withChangedComments(ctx, candidates, since)
}

func (scan discussionScan) changedComments(ctx context.Context, since time.Time) ([]comment, error) {
	q := url.Values{"since": {since.UTC().Format(time.RFC3339)}, "sort": {"updated"}, "direction": {"asc"}}
	changed, err := list[comment](ctx, scan.client, repoPath(scan.config)+"/issues/comments", q)
	if err != nil {
		return nil, err
	}
	if !enabled(scan.config, "includePullRequests") {
		return changed, nil
	}
	reviews, err := list[comment](ctx, scan.client, repoPath(scan.config)+"/pulls/comments", q)
	if err != nil {
		return nil, err
	}
	return append(changed, reviews...), nil
}

func (scan discussionScan) commentNumber(c comment) (int, error) {
	parentURL := c.IssueURL
	if parentURL == "" {
		parentURL = c.PullRequestURL
	}
	u, err := url.Parse(parentURL)
	if err != nil || !scan.client.allowed(u) {
		return 0, fmt.Errorf("invalid GitHub comment parent URL")
	}
	prefix := scan.client.base.Path + "/" + repoPath(scan.config) + "/"
	rel := strings.TrimPrefix(u.Path, prefix)
	if rel == u.Path || (!strings.HasPrefix(rel, "issues/") && !strings.HasPrefix(rel, "pulls/")) {
		return 0, fmt.Errorf("GitHub comment parent belongs to another repository")
	}
	n, err := strconv.Atoi(rel[strings.IndexByte(rel, '/')+1:])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid GitHub comment parent number")
	}
	return n, nil
}

func (scan discussionScan) withChangedComments(ctx context.Context, candidates map[int]issue, since time.Time) (map[int]issue, error) {
	changed, err := scan.changedComments(ctx, since)
	if err != nil {
		return nil, err
	}
	for _, c := range changed {
		n, err := scan.commentNumber(c)
		if err != nil {
			return nil, err
		}
		if _, exists := candidates[n]; exists {
			continue
		}
		var i issue
		if err := scan.client.get(ctx, repoPath(scan.config)+"/issues/"+strconv.Itoa(n), nil, &i); err != nil {
			return nil, err
		}
		if i.Number != n {
			return nil, fmt.Errorf("GitHub returned a mismatched discussion number")
		}
		candidates[n] = i
	}
	return candidates, nil
}

func (scan discussionScan) read(ctx context.Context, i issue) (connectors.SourceItem, error) {
	c, cfg, repo := scan.client, scan.config, scan.repo
	root := repoPath(cfg)
	n := strconv.Itoa(i.Number)
	kind := "issue"
	var p pull
	var reviews, inline []comment
	if i.PullRequest != nil {
		kind = "pull_request"
		if err := c.get(ctx, root+"/pulls/"+n, nil, &p); err != nil {
			return connectors.SourceItem{}, err
		}
		var err error
		reviews, err = list[comment](ctx, c, root+"/pulls/"+n+"/reviews", nil)
		if err != nil {
			return connectors.SourceItem{}, err
		}
		inline, err = list[comment](ctx, c, root+"/pulls/"+n+"/comments", nil)
		if err != nil {
			return connectors.SourceItem{}, err
		}
	}
	comments, err := list[comment](ctx, c, root+"/issues/"+n+"/comments", nil)
	if err != nil {
		return connectors.SourceItem{}, err
	}
	state := i.State
	if p.Merged {
		state = "merged"
	}
	labels := make([]string, 0, len(i.Labels))
	for _, label := range i.Labels {
		labels = append(labels, label.Name)
	}
	sort.Strings(labels)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s #%d: %s\n\nSource: %s\nState: %s\nAuthor: %s\nCreated: %s\nUpdated: %s\nLabels: %s\n", kind, i.Number, i.Title, i.HTMLURL, state, i.User.Login, i.CreatedAt.UTC().Format(time.RFC3339), i.UpdatedAt.UTC().Format(time.RFC3339), strings.Join(labels, ", "))
	if kind == "pull_request" {
		fmt.Fprintf(&b, "Branches: %s → %s\nDraft: %t\n", p.Head.Ref, p.Base.Ref, p.Draft)
	}
	fmt.Fprintf(&b, "\n## Description\n\n%s\n", i.Body)
	modified := i.UpdatedAt
	for _, section := range []struct {
		heading string
		entries []comment
	}{
		{"Comments", comments}, {"Reviews", reviews}, {"Review comments", inline},
	} {
		modified = appendComments(&b, section.heading, section.entries, modified)
	}
	text := b.String()
	hash := sha256.Sum256([]byte(text))
	opaque, _ := json.Marshal(contentData{Text: text})
	md := metadata(c, repo, kind)
	md["url"] = i.HTMLURL
	md["github_number"] = n
	md["github_state"] = state
	return connectors.SourceItem{ID: itemID(repo, kind, n), Name: i.Title, Path: kind + "/" + n, Kind: connectors.KindDocument, Revision: hex.EncodeToString(hash[:]), ModifiedAt: modified, Size: int64(len(text)), MIMEType: "text/markdown", Metadata: md, Attributes: map[string]any{"contentType": kind, "state": state, "author": i.User.Login, "labels": labels}, Opaque: opaque}, nil
}

func commentTime(c comment) time.Time {
	if c.CreatedAt.IsZero() {
		return c.SubmittedAt
	}
	return c.CreatedAt
}

func appendComments(b *strings.Builder, heading string, entries []comment, modified time.Time) time.Time {
	sort.Slice(entries, func(a, z int) bool {
		at, zt := commentTime(entries[a]), commentTime(entries[z])
		if at.Equal(zt) {
			return entries[a].ID < entries[z].ID
		}
		return at.Before(zt)
	})
	if len(entries) > 0 {
		fmt.Fprintf(b, "\n## %s\n", heading)
	}
	for _, c := range entries {
		appendComment(b, c)
		for _, ts := range []time.Time{c.UpdatedAt, c.CreatedAt, c.SubmittedAt} {
			if ts.After(modified) {
				modified = ts
			}
		}
	}
	return modified
}

func appendComment(b *strings.Builder, c comment) {
	fmt.Fprintf(b, "\n### %s · %s · %d\n\nSource: %s\n", c.User.Login, commentTime(c).UTC().Format(time.RFC3339), c.ID, c.HTMLURL)
	if !c.UpdatedAt.IsZero() {
		fmt.Fprintf(b, "Updated: %s\n", c.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if c.State != "" {
		fmt.Fprintf(b, "Review state: %s\n", c.State)
	}
	appendCommentLocation(b, c)
	if c.InReplyToID != 0 {
		fmt.Fprintf(b, "Reply to: %d\n", c.InReplyToID)
	}
	fmt.Fprintf(b, "\n%s\n", c.Body)
}

func appendCommentLocation(b *strings.Builder, c comment) {
	if c.Path == "" {
		return
	}
	fmt.Fprintf(b, "File: %s\n", c.Path)
	if c.Line != nil {
		fmt.Fprintf(b, "Line: %d\n", *c.Line)
	} else if c.OriginalLine != nil {
		fmt.Fprintf(b, "Original line: %d\n", *c.OriginalLine)
	}
	if c.StartLine != nil {
		fmt.Fprintf(b, "Start line: %d\n", *c.StartLine)
	}
}

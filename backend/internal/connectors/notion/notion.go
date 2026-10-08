// Package notion implements the Notion source connector.
package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

// apiBase can be overridden with NOTION_API_BASE (e.g. for an egress proxy
// or a local test double).
var apiBase = envOr("NOTION_API_BASE", "https://api.notion.com/v1")

func envOr(key, def string) string {
	if v := strings.TrimRight(os.Getenv(key), "/"); v != "" {
		return v
	}
	return def
}

const (
	apiVersion = "2026-03-11"
	pageSize   = 100
	// Notion allows ~3 requests/second per integration.
	requestInterval = 350 * time.Millisecond
	// highWaterOverlap re-reads a small window to tolerate clock skew and
	// minute-granularity last_edited_time.
	highWaterOverlap = 2 * time.Minute
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type:        "notion",
		Name:        "Notion",
		Description: "Notion internal integration token with read access to shared data sources",
		Fields: []connectors.FieldSpec{
			{Key: "token", Label: "Integration token", Type: connectors.FieldPassword, Secret: true, Required: true, Placeholder: "ntn_…"},
		},
	})
	connectors.RegisterSource(&Connector{})
}

// Connector implements connectors.SourceConnector for Notion.
type Connector struct{}

// Info describes the connector.
func (c *Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type:           "notion",
		Name:           "Notion",
		CredentialType: "notion",
		Capabilities: connectors.Capabilities{
			SupportsInlineMultimodal: true,
			IncrementalMode:          connectors.IncrementalHighWater,
			DeletionMode:             connectors.DeletionFullReconcile,
		},
		BrowseKinds: []string{connectors.KindDataSource},
		Fields: []connectors.FieldSpec{
			{Key: "dataSourceId", Label: "Data source", Type: connectors.FieldString, Required: true, Browse: true,
				BrowseKind: connectors.KindDataSource, Help: "Notion data source ID (not the database ID). Use Browse to pick one shared with the integration.",
				Effect: connectors.EffectRebaseline},
		},
		FilterFields: []connectors.FilterFieldSpec{
			{Key: "name", Label: "Page title", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "modifiedAt", Label: "Last edited", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
			{Key: "createdAt", Label: "Created", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
		},
	}
}

func client(cred connectors.Credential) *httpx.Client {
	c := httpx.New(cred.Headers, map[string]string{
		"Authorization":  "Bearer " + cred.String("token"),
		"Notion-Version": apiVersion,
	})
	c.Pace = httpx.Pacer(requestInterval)
	return c
}

// ValidateCredential checks the token.
func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	var me struct {
		Name string `json:"name"`
		Bot  struct {
			WorkspaceName string `json:"workspace_name"`
		} `json:"bot"`
	}
	if err := client(cred).JSON(ctx, http.MethodGet, apiBase+"/users/me", nil, &me); err != nil {
		return "", err
	}
	if me.Bot.WorkspaceName != "" {
		return fmt.Sprintf("Connected as %s (workspace %s)", me.Name, me.Bot.WorkspaceName), nil
	}
	return "Connected as " + me.Name, nil
}

// ValidateConfig checks task configuration.
func (c *Connector) ValidateConfig(cfg map[string]any, f connectors.Filter) error {
	if err := connectors.Require(cfg, "dataSourceId"); err != nil {
		return err
	}
	return connectors.ValidateFilter(f, c.Info().FilterFields, false)
}

// Browse lists data sources shared with the integration.
func (c *Connector) Browse(ctx context.Context, cred connectors.Credential, req connectors.BrowseRequest) (connectors.BrowseResult, error) {
	cl := client(cred)
	res := connectors.BrowseResult{Breadcrumbs: []connectors.Breadcrumb{{ID: "", Name: "Data sources"}}}
	cursor := ""
	for range 20 {
		body := map[string]any{
			"filter":    map[string]any{"property": "object", "value": "data_source"},
			"page_size": pageSize,
		}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		var out struct {
			Results []struct {
				ID    string     `json:"id"`
				Title []richText `json:"title"`
			} `json:"results"`
			HasMore    bool   `json:"has_more"`
			NextCursor string `json:"next_cursor"`
		}
		if err := cl.JSON(ctx, http.MethodPost, apiBase+"/search", body, &out); err != nil {
			return res, err
		}
		for _, r := range out.Results {
			name := plainText(r.Title)
			if name == "" {
				name = r.ID
			}
			res.Items = append(res.Items, connectors.BrowseItem{ID: r.ID, Name: name, Kind: connectors.KindDataSource, Path: name, Selectable: true})
		}
		if !out.HasMore || out.NextCursor == "" {
			break
		}
		cursor = out.NextCursor
	}
	sort.Slice(res.Items, func(i, j int) bool { return strings.ToLower(res.Items[i].Name) < strings.ToLower(res.Items[j].Name) })
	return res, nil
}

type cursorState struct {
	HighWater time.Time `json:"highWater"`
}

type richText struct {
	PlainText string `json:"plain_text"`
}

type page struct {
	Object         string                     `json:"object"`
	ID             string                     `json:"id"`
	URL            string                     `json:"url"`
	CreatedTime    time.Time                  `json:"created_time"`
	LastEditedTime time.Time                  `json:"last_edited_time"`
	InTrash        bool                       `json:"in_trash"`
	Archived       bool                       `json:"archived"`
	Properties     map[string]json.RawMessage `json:"properties"`
}

type opaque struct {
	Title      string          `json:"title"`
	URL        string          `json:"url"`
	Properties json.RawMessage `json:"properties"`
	Source     string          `json:"source"`
}

// Scan queries the data source. Incremental scans use a last_edited_time
// high-water mark; full scans list every page (complete inventory).
func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	cl := client(req.Credential)
	dsID := connectors.AsString(req.Config["dataSourceId"])

	var prev cursorState
	if len(req.Cursor) > 0 && !req.Full {
		_ = json.Unmarshal(req.Cursor, &prev)
	}
	dsName := dsID
	var ds struct {
		Title []richText `json:"title"`
	}
	if err := cl.JSON(ctx, http.MethodGet, apiBase+"/data_sources/"+url.PathEscape(dsID), nil, &ds); err != nil {
		return connectors.ScanResult{}, fmt.Errorf("retrieve data source: %w", err)
	}
	if t := plainText(ds.Title); t != "" {
		dsName = t
	}

	scanStart := time.Now().UTC()
	highWater := prev.HighWater
	incremental := !req.Full && !prev.HighWater.IsZero()
	if incremental {
		req.Log("info", fmt.Sprintf("Querying pages edited since %s", prev.HighWater.Add(-highWaterOverlap).Format(time.RFC3339)))
	}

	cursor := ""
	for {
		body := map[string]any{
			"page_size":   pageSize,
			"result_type": "page",
			"sorts":       []map[string]string{{"timestamp": "last_edited_time", "direction": "ascending"}},
		}
		if incremental {
			body["filter"] = map[string]any{
				"timestamp":        "last_edited_time",
				"last_edited_time": map[string]string{"on_or_after": prev.HighWater.Add(-highWaterOverlap).Format(time.RFC3339)},
			}
		}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		var out struct {
			Results       []page `json:"results"`
			HasMore       bool   `json:"has_more"`
			NextCursor    string `json:"next_cursor"`
			RequestStatus *struct {
				Type             string `json:"type"`
				IncompleteReason string `json:"incomplete_reason"`
			} `json:"request_status"`
		}
		if err := cl.JSON(ctx, http.MethodPost, apiBase+"/data_sources/"+url.PathEscape(dsID)+"/query", body, &out); err != nil {
			return connectors.ScanResult{}, fmt.Errorf("query data source: %w", err)
		}
		if out.RequestStatus != nil && out.RequestStatus.Type == "incomplete" {
			return connectors.ScanResult{}, fmt.Errorf("notion query was incomplete: %s", out.RequestStatus.IncompleteReason)
		}
		for _, p := range out.Results {
			if p.Object != "page" {
				continue
			}
			if p.LastEditedTime.IsZero() {
				return connectors.ScanResult{}, fmt.Errorf("notion returned page %s without last_edited_time", p.ID)
			}
			if p.LastEditedTime.After(highWater) {
				highWater = p.LastEditedTime
			}
			item := toItem(p, dsID, dsName)
			if p.InTrash || p.Archived {
				item.Deleted = true
			}
			if err := req.Emit(item); err != nil {
				return connectors.ScanResult{}, err
			}
		}
		if !out.HasMore {
			break
		}
		if out.NextCursor == "" {
			return connectors.ScanResult{}, errors.New("notion returned has_more without next_cursor")
		}
		cursor = out.NextCursor
	}
	if highWater.After(scanStart) {
		highWater = scanStart
	}
	cur, _ := json.Marshal(cursorState{HighWater: highWater})
	return connectors.ScanResult{Cursor: cur, Complete: !incremental}, nil
}

func toItem(p page, dsID, dsName string) connectors.SourceItem {
	title := pageTitle(p)
	props, _ := json.Marshal(p.Properties)
	op, _ := json.Marshal(opaque{Title: title, URL: p.URL, Properties: props, Source: dsName})
	return connectors.SourceItem{
		ID:         p.ID,
		Name:       title,
		Path:       dsName + " / " + title,
		Kind:       connectors.KindPage,
		Revision:   p.LastEditedTime.UTC().Format(time.RFC3339Nano),
		ModifiedAt: p.LastEditedTime,
		Metadata: map[string]string{
			"notion_page_id":        p.ID,
			"notion_data_source_id": dsID,
			"notion_url":            p.URL,
			"title":                 title,
		},
		Tags:       []string{"notion_data_source_id:" + dsID},
		Attributes: map[string]any{"createdAt": p.CreatedTime},
		Opaque:     op,
	}
}

// OpenContent retrieves the page Markdown plus its properties.
func (c *Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	var op opaque
	_ = json.Unmarshal(req.Item.Opaque, &op)
	for attempt := 0; ; attempt++ {
		markdown, err := readMarkdown(ctx, req)
		if err != nil {
			return connectors.SourceContent{}, err
		}
		content := connectors.SourceContent{Text: markdown}
		expired := false
		if req.IncludesImages() {
			content, err = connectors.InlineImages(ctx, markdown, req.MaxFileSize, func(ctx context.Context, rawURL string) (*http.Response, error) {
				resp, err := connectors.FetchImage(ctx, rawURL, nil)
				u, _ := url.Parse(rawURL)
				if u != nil && u.Query().Get("X-Amz-Signature") != "" && (httpx.IsStatus(err, 401) || httpx.IsStatus(err, 403)) {
					expired = true
				}
				return resp, err
			})
			if err != nil {
				return connectors.SourceContent{}, err
			}
		}
		if expired && attempt == 0 {
			continue
		}
		properties := "\n\n---\n\nNotion page properties:\n" + PropertiesText(op.Properties)
		content.Text = strings.TrimSpace(markdown + properties)
		if len(content.Blocks) > 0 {
			content.Blocks = append(content.Blocks, connectors.TextBlock(properties))
		}
		content.Context = fmt.Sprintf("Notion Page %q in Data Source %q", op.Title, op.Source)
		content.Timestamp = req.Item.ModifiedAt
		return content, nil
	}
}

func readMarkdown(ctx context.Context, req connectors.ContentRequest) (string, error) {
	var md struct {
		Markdown       string   `json:"markdown"`
		Truncated      bool     `json:"truncated"`
		UnknownBlockID []string `json:"unknown_block_ids"`
	}
	u := apiBase + "/pages/" + url.PathEscape(req.Item.ID) + "/markdown?include_transcript=true"
	if err := client(req.Credential).JSON(ctx, http.MethodGet, u, nil, &md); err != nil {
		if httpx.IsStatus(err, http.StatusNotFound) {
			return "", connectors.Skip("page no longer accessible")
		}
		return "", err
	}
	if md.Truncated || len(md.UnknownBlockID) > 0 {
		return "", fmt.Errorf("notion page %s returned truncated markdown", req.Item.ID)
	}
	return md.Markdown, nil
}

func pageTitle(p page) string {
	for _, raw := range p.Properties {
		var prop struct {
			Type  string     `json:"type"`
			Title []richText `json:"title"`
		}
		if json.Unmarshal(raw, &prop) == nil && prop.Type == "title" {
			if t := plainText(prop.Title); t != "" {
				return t
			}
		}
	}
	return p.ID
}

func plainText(items []richText) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.PlainText)
	}
	return strings.TrimSpace(b.String())
}

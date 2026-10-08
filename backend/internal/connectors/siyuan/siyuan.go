// Package siyuan implements the SiYuan Note source connector.
package siyuan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type:        "siyuan",
		Name:        "SiYuan",
		Description: "SiYuan kernel API endpoint and token (Settings → About → API token)",
		Fields: []connectors.FieldSpec{
			{Key: "baseUrl", Label: "Base URL", Type: connectors.FieldURL, Required: true, Placeholder: "http://siyuan:6806"},
			{Key: "instanceId", Label: "Instance ID", Type: connectors.FieldString,
				Help: "Stable identifier for this SiYuan instance. Leave empty to use its base URL."},
			{Key: "token", Label: "API token", Type: connectors.FieldPassword, Secret: true, Required: true},
		},
	})
	connectors.RegisterSource(&Connector{})
}

// Connector implements SiYuan documents as a source.
type Connector struct{}

// Info describes the connector.
func (c *Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type:           "siyuan",
		Name:           "SiYuan",
		CredentialType: "siyuan",
		Capabilities: connectors.Capabilities{
			SupportsInlineMultimodal: true,
			IncrementalMode:          connectors.IncrementalHighWater,
			DeletionMode:             connectors.DeletionFullReconcile,
		},
		BrowseKinds: []string{connectors.KindNotebook},
		Fields: []connectors.FieldSpec{
			{Key: "notebookId", Label: "Notebook", Type: connectors.FieldString, Browse: true, BrowseKind: connectors.KindNotebook,
				Help: "Leave empty to sync every open notebook.", Effect: connectors.EffectRebaseline},
			{Key: "pathPrefix", Label: "Path prefix", Type: connectors.FieldString, Placeholder: "/Projects",
				Help: "Only documents whose human-readable path starts with this prefix.", Effect: connectors.EffectReconcile},
		},
		FilterFields: []connectors.FilterFieldSpec{
			{Key: "name", Label: "Title", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "tags", Label: "Tags", Type: connectors.FilterString, Operators: []string{connectors.OpContains, connectors.OpEquals, connectors.OpNotEquals, connectors.OpIn, connectors.OpNotIn}},
			{Key: "modifiedAt", Label: "Updated", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
		},
	}
}

type api struct {
	base string
	http *httpx.Client
}

func newAPI(cred connectors.Credential) *api {
	return &api{
		base: strings.TrimRight(cred.String("baseUrl"), "/"),
		http: httpx.New(cred.Headers, map[string]string{"Authorization": "Token " + cred.String("token")}),
	}
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (a *api) call(ctx context.Context, path string, in, out any) error {
	var env envelope
	if err := a.http.JSON(ctx, http.MethodPost, a.base+path, in, &env); err != nil {
		return err
	}
	if env.Code != 0 {
		return fmt.Errorf("siyuan %s: %s (code %d)", path, env.Msg, env.Code)
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// ValidateCredential checks connectivity.
func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	var ver string
	if err := newAPI(cred).call(ctx, "/api/system/version", map[string]any{}, &ver); err != nil {
		return "", err
	}
	return "Connected to SiYuan " + ver, nil
}

// ValidateConfig checks configuration.
func (c *Connector) ValidateConfig(cfg map[string]any, f connectors.Filter) error {
	if id := connectors.AsString(cfg["notebookId"]); id != "" && !idPattern.MatchString(id) {
		return fmt.Errorf("notebookId is not a valid SiYuan ID")
	}
	return connectors.ValidateFilter(f, c.Info().FilterFields, false)
}

// Browse lists notebooks.
func (c *Connector) Browse(ctx context.Context, cred connectors.Credential, _ connectors.BrowseRequest) (connectors.BrowseResult, error) {
	var out struct {
		Notebooks []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Closed bool   `json:"closed"`
		} `json:"notebooks"`
	}
	if err := newAPI(cred).call(ctx, "/api/notebook/lsNotebooks", map[string]any{}, &out); err != nil {
		return connectors.BrowseResult{}, err
	}
	res := connectors.BrowseResult{Breadcrumbs: []connectors.Breadcrumb{{Name: "Notebooks"}}}
	for _, nb := range out.Notebooks {
		name := nb.Name
		if nb.Closed {
			name += " (closed)"
		}
		res.Items = append(res.Items, connectors.BrowseItem{ID: nb.ID, Name: name, Kind: connectors.KindNotebook, Path: nb.Name, Selectable: !nb.Closed})
	}
	return res, nil
}

// idPattern matches SiYuan block IDs (e.g. 20210808180117-czj9bvb).
var idPattern = regexp.MustCompile(`^\d{14}-[0-9a-z]{7}$`)

type cursorState struct {
	Updated string `json:"updated"` // yyyyMMddHHmmss
}

type docRow struct {
	ID      string `json:"id"`
	Box     string `json:"box"`
	HPath   string `json:"hpath"`
	Content string `json:"content"`
	Tag     string `json:"tag"`
	Updated string `json:"updated"`
}

const pageSize = 500

// Scan enumerates documents with SQL ordered by (updated, id).
func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	a := newAPI(req.Credential)
	nb := connectors.AsString(req.Config["notebookId"])
	prefix := connectors.AsString(req.Config["pathPrefix"])
	var prev cursorState
	if len(req.Cursor) > 0 && !req.Full {
		_ = json.Unmarshal(req.Cursor, &prev)
	}
	incremental := !req.Full && prev.Updated != ""

	notebooks := map[string]string{}
	var nbs struct {
		Notebooks []struct{ ID, Name string } `json:"notebooks"`
	}
	if err := a.call(ctx, "/api/notebook/lsNotebooks", map[string]any{}, &nbs); err == nil {
		for _, n := range nbs.Notebooks {
			notebooks[n.ID] = n.Name
		}
	}

	high := prev.Updated
	where := []string{"type = 'd'"}
	if nb != "" {
		if !idPattern.MatchString(nb) {
			return connectors.ScanResult{}, fmt.Errorf("invalid notebook ID")
		}
		where = append(where, "box = '"+nb+"'")
	}
	if prefix != "" {
		where = append(where, "hpath LIKE '"+sqlEscape(prefix)+"%' ESCAPE '\\'")
	}
	if incremental {
		where = append(where, "updated >= '"+strings.ReplaceAll(prev.Updated, "'", "''")+"'")
	}
	for offset := 0; ; offset += pageSize {
		stmt := fmt.Sprintf("SELECT id, box, hpath, content, tag, updated FROM blocks WHERE %s ORDER BY updated ASC, id ASC LIMIT %d OFFSET %d",
			strings.Join(where, " AND "), pageSize, offset)
		var rows []docRow
		if err := a.call(ctx, "/api/query/sql", map[string]any{"stmt": stmt}, &rows); err != nil {
			return connectors.ScanResult{}, err
		}
		for _, r := range rows {
			if r.Updated > high {
				high = r.Updated
			}
			mod, _ := time.ParseInLocation("20060102150405", r.Updated, time.Local)
			var tags []string
			for _, t := range strings.Split(r.Tag, "#") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
			title := r.Content
			if title == "" {
				title = r.ID
			}
			item := connectors.SourceItem{
				ID: r.ID, Name: title, Path: notebooks[r.Box] + r.HPath, Kind: connectors.KindDocument,
				Revision: r.Updated, ModifiedAt: mod,
				Metadata: map[string]string{
					"siyuan_document_id": r.ID, "siyuan_notebook_id": r.Box, "siyuan_hpath": r.HPath, "title": title,
				},
				Tags:       []string{"siyuan_notebook_id:" + r.Box},
				Attributes: map[string]any{"tags": tags},
			}
			if err := req.Emit(item); err != nil {
				return connectors.ScanResult{}, err
			}
		}
		if len(rows) < pageSize {
			break
		}
	}
	cur, _ := json.Marshal(cursorState{Updated: high})
	return connectors.ScanResult{Cursor: cur, Complete: !incremental}, nil
}

// OpenContent exports the document as Markdown.
func (c *Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	var out struct {
		HPath   string `json:"hPath"`
		Content string `json:"content"`
	}
	if err := newAPI(req.Credential).call(ctx, "/api/export/exportMdContent", map[string]any{"id": req.Item.ID}, &out); err != nil {
		return connectors.SourceContent{}, err
	}
	content := connectors.SourceContent{Text: out.Content}
	if req.IncludesImages() {
		var err error
		content, err = connectors.InlineImages(ctx, out.Content, req.MaxFileSize, func(ctx context.Context, rawURL string) (*http.Response, error) {
			return fetchAsset(ctx, req, rawURL)
		})
		if err != nil {
			return connectors.SourceContent{}, err
		}
	}
	content.Context = fmt.Sprintf("SiYuan document %q", req.Item.Path)
	content.Timestamp = req.Item.ModifiedAt
	return content, nil
}

func fetchAsset(ctx context.Context, req connectors.ContentRequest, rawURL string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid image URL")
	}
	base, err := url.Parse(strings.TrimRight(req.Credential.String("baseUrl"), "/") + "/")
	if err != nil {
		return nil, fmt.Errorf("invalid SiYuan base URL")
	}
	if u.IsAbs() && (u.Scheme != base.Scheme || u.Host != base.Host) {
		return connectors.FetchImage(ctx, rawURL, nil)
	}
	return fetchLocalAsset(ctx, req, u, base)
}

func localAssetPath(u, base *url.URL) (string, error) {
	assetPath := strings.TrimPrefix(strings.TrimPrefix(u.Path, "/"), "./")
	if u.IsAbs() {
		assetPath = strings.TrimPrefix(u.Path, base.Path)
	}
	if !strings.HasPrefix(assetPath, "assets/") || path.Clean(assetPath) != assetPath || strings.Contains(assetPath, "\\") || u.Host != "" && !u.IsAbs() {
		return "", fmt.Errorf("image is not a valid SiYuan asset path")
	}
	return assetPath, nil
}

func fetchLocalAsset(ctx context.Context, req connectors.ContentRequest, u, base *url.URL) (*http.Response, error) {
	assetPath, err := localAssetPath(u, base)
	if err != nil {
		return nil, err
	}
	u.Scheme, u.Host, u.Path = base.Scheme, base.Host, base.Path+assetPath
	u.RawPath = ""
	query := u.Query()
	inferredBox := query.Get("box") == "" && query.Get("dataPath") == "" && req.Item.Metadata["siyuan_notebook_id"] != ""
	if inferredBox {
		query.Set("box", req.Item.Metadata["siyuan_notebook_id"])
	}
	u.RawQuery = query.Encode()
	resp, err := connectors.FetchImage(ctx, u.String(), &req.Credential)
	if inferredBox && (httpx.IsStatus(err, http.StatusNotFound) || httpx.IsStatus(err, http.StatusForbidden)) {
		// Older notes can reference global data/assets; a box-scoped route cannot serve those.
		query.Del("box")
		query.Set("dataPath", assetPath)
		u.RawQuery = query.Encode()
		return connectors.FetchImage(ctx, u.String(), &req.Credential)
	}
	return resp, err
}

func sqlEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "''")
	s = strings.ReplaceAll(s, "%", "\\%")
	return strings.ReplaceAll(s, "_", "\\_")
}

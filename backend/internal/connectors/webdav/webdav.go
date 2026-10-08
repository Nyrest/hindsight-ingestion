// Package webdav implements the WebDAV source connector.
package webdav

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type:        "webdav",
		Name:        "WebDAV",
		Description: "Nextcloud, ownCloud, Synology, Apache mod_dav, rclone serve, …",
		Fields: []connectors.FieldSpec{
			{Key: "baseUrl", Label: "Server URL", Type: connectors.FieldURL, Required: true,
				Placeholder: "https://cloud.example.com/remote.php/dav/files/alice"},
			{Key: "username", Label: "Username", Type: connectors.FieldString},
			{Key: "password", Label: "Password / app password", Type: connectors.FieldPassword, Secret: true},
			{Key: "bearerToken", Label: "Bearer token", Type: connectors.FieldPassword, Secret: true,
				Help: "Use instead of username/password if the server expects a token."},
		},
	})
	connectors.RegisterSource(&Connector{})
}

// Connector implements WebDAV as a source.
type Connector struct{}

// Info describes the connector.
func (c *Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type:           "webdav",
		Name:           "WebDAV",
		CredentialType: "webdav",
		Capabilities: connectors.Capabilities{
			IncrementalMode: connectors.IncrementalSyncToken,
			DeletionMode:    connectors.DeletionDelta,
			SupportsFiles:   true,
		},
		BrowseKinds: []string{connectors.KindFolder},
		Fields: []connectors.FieldSpec{
			{Key: "rootPath", Label: "Root folder", Type: connectors.FieldString, Default: "/", Browse: true, BrowseKind: connectors.KindFolder,
				Help: "Path relative to the server URL.", Effect: connectors.EffectRebaseline},
			{Key: "recursive", Label: "Include sub-folders", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
			{Key: "useSyncToken", Label: "Use sync-collection (RFC 6578) when available", Type: connectors.FieldBoolean, Default: true,
				Effect: connectors.EffectRebaseline},
		},
		FilterFields: []connectors.FilterFieldSpec{
			{Key: "name", Label: "File name", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "extension", Label: "Extension", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "fileType", Label: "File type", Type: connectors.FilterEnum, Operators: connectors.EnumOps, Options: fileTypeOptions},
			{Key: "size", Label: "Size (bytes)", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
			{Key: "modifiedAt", Label: "Modified", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
		},
	}
}

var fileTypeOptions = []connectors.Option{
	{Value: connectors.GroupPlainText, Label: "Plain text"}, {Value: connectors.GroupDocuments, Label: "Documents"},
	{Value: connectors.GroupImages, Label: "Images"}, {Value: connectors.GroupAudios, Label: "Audios"},
}

type client struct {
	base *url.URL
	http *httpx.Client
}

func newClient(cred connectors.Credential) (*client, error) {
	u, err := url.Parse(strings.TrimRight(cred.String("baseUrl"), "/") + "/")
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid WebDAV server URL")
	}
	auth := map[string]string{}
	if tok := cred.String("bearerToken"); tok != "" {
		auth["Authorization"] = "Bearer " + tok
	} else if user := cred.String("username"); user != "" {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.SetBasicAuth(user, cred.String("password"))
		auth["Authorization"] = req.Header.Get("Authorization")
	}
	return &client{base: u, http: httpx.New(cred.Headers, auth, cred.Proxy)}, nil
}

// resolve maps a root-relative path to an absolute URL.
func (c *client) resolve(p string) string {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.TrimRight(c.base.String(), "/") + "/" + strings.Join(segs, "/")
}

// relPath converts an href from a response to a root-relative path.
func (c *client) relPath(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	p := u.Path
	if unescaped, err := url.PathUnescape(p); err == nil {
		p = unescaped
	}
	base := c.base.Path
	if unescaped, err := url.PathUnescape(base); err == nil {
		base = unescaped
	}
	rel := strings.TrimPrefix(p, strings.TrimRight(base, "/"))
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	return rel
}

type multistatus struct {
	Responses []response `xml:"response"`
	SyncToken string     `xml:"sync-token"`
}

type response struct {
	Href     string     `xml:"href"`
	Status   string     `xml:"status"`
	Propstat []propstat `xml:"propstat"`
}

type propstat struct {
	Status string `xml:"status"`
	Prop   struct {
		DisplayName   string  `xml:"displayname"`
		ContentLength string  `xml:"getcontentlength"`
		ContentType   string  `xml:"getcontenttype"`
		ETag          string  `xml:"getetag"`
		LastModified  string  `xml:"getlastmodified"`
		ResourceType  restype `xml:"resourcetype"`
		SyncToken     string  `xml:"sync-token"`
		FileID        string  `xml:"fileid"`
	} `xml:"prop"`
}

type restype struct {
	Collection *struct{} `xml:"collection"`
}

type entry struct {
	Path     string
	IsDir    bool
	ETag     string
	Size     int64
	Modified time.Time
	MIME     string
	Gone     bool
}

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop>
<d:resourcetype/><d:getcontentlength/><d:getcontenttype/><d:getetag/><d:getlastmodified/><d:displayname/><d:sync-token/><oc:fileid/>
</d:prop></d:propfind>`

func (c *client) propfind(ctx context.Context, p string, depth string) ([]entry, string, error) {
	resp, err := c.http.Do(ctx, httpx.Request{
		Method: "PROPFIND", URL: c.resolve(p),
		Header: http.Header{"Depth": {depth}, "Content-Type": {"application/xml; charset=utf-8"}},
		Body:   []byte(propfindBody),
	})
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	return c.parse(resp.Body)
}

func (c *client) parse(r io.Reader) ([]entry, string, error) {
	var ms multistatus
	if err := xml.NewDecoder(r).Decode(&ms); err != nil {
		return nil, "", fmt.Errorf("parse WebDAV response: %w", err)
	}
	var out []entry
	token := ms.SyncToken
	for _, resp := range ms.Responses {
		e := entry{Path: c.relPath(resp.Href)}
		if strings.Contains(resp.Status, " 404") {
			e.Gone = true
			out = append(out, e)
			continue
		}
		for _, ps := range resp.Propstat {
			if ps.Status != "" && !strings.Contains(ps.Status, " 200") {
				continue
			}
			pr := ps.Prop
			if pr.ResourceType.Collection != nil {
				e.IsDir = true
			}
			if pr.ETag != "" {
				e.ETag = strings.Trim(pr.ETag, `"`)
			}
			if pr.ContentLength != "" {
				e.Size, _ = strconv.ParseInt(pr.ContentLength, 10, 64)
			}
			if pr.ContentType != "" {
				e.MIME = pr.ContentType
			}
			if pr.LastModified != "" {
				e.Modified, _ = http.ParseTime(pr.LastModified)
			}
			if pr.SyncToken != "" && token == "" {
				token = pr.SyncToken
			}
		}
		out = append(out, e)
	}
	return out, token, nil
}

// ValidateCredential issues a depth-0 PROPFIND on the base URL.
func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	cl, err := newClient(cred)
	if err != nil {
		return "", err
	}
	if _, _, err := cl.propfind(ctx, "/", "0"); err != nil {
		return "", err
	}
	return "Connected to " + cl.base.Host, nil
}

// ValidateConfig checks configuration.
func (c *Connector) ValidateConfig(cfg map[string]any, f connectors.Filter) error {
	return connectors.ValidateFilter(f, c.Info().FilterFields, false)
}

// Browse lists folders under ParentID (a root-relative path).
func (c *Connector) Browse(ctx context.Context, cred connectors.Credential, req connectors.BrowseRequest) (connectors.BrowseResult, error) {
	cl, err := newClient(cred)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	parent := path.Clean("/" + req.ParentID)
	entries, _, err := cl.propfind(ctx, parent, "1")
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	res := connectors.BrowseResult{Breadcrumbs: []connectors.Breadcrumb{{ID: "/", Name: "Root"}}}
	acc := ""
	for _, part := range strings.Split(strings.Trim(parent, "/"), "/") {
		if part != "" {
			acc += "/" + part
			res.Breadcrumbs = append(res.Breadcrumbs, connectors.Breadcrumb{ID: acc, Name: part})
		}
	}
	for _, e := range entries {
		p := path.Clean(e.Path)
		if p == parent {
			continue
		}
		kind := connectors.KindFile
		if e.IsDir {
			kind = connectors.KindFolder
		}
		res.Items = append(res.Items, connectors.BrowseItem{
			ID: p, Name: path.Base(p), Kind: kind, Path: p, HasChildren: e.IsDir, Selectable: e.IsDir,
		})
	}
	return res, nil
}

type cursorState struct {
	SyncToken string `json:"syncToken,omitempty"`
}

type opaque struct {
	Path string `json:"p"`
}

// Scan uses sync-collection when supported; otherwise a recursive PROPFIND
// inventory with ETags (deletions via scan generations).
func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	cl, err := newClient(req.Credential)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	root := path.Clean("/" + connectors.AsString(req.Config["rootPath"]))
	recursive := connectors.BoolDefault(req.Config, "recursive", true)
	useSync := connectors.BoolDefault(req.Config, "useSyncToken", true)

	var prev cursorState
	if len(req.Cursor) > 0 && !req.Full {
		_ = json.Unmarshal(req.Cursor, &prev)
	}

	emit := func(e entry) error {
		if e.IsDir {
			return nil
		}
		p := path.Clean(e.Path)
		if !strings.HasPrefix(p, strings.TrimRight(root, "/")+"/") && p != root {
			return nil
		}
		if !recursive && path.Dir(p) != root {
			return nil
		}
		if e.Gone {
			return req.Emit(connectors.SourceItem{ID: p, Path: p, Name: path.Base(p), Kind: connectors.KindFile, Deleted: true})
		}
		rev := e.ETag
		if rev == "" {
			rev = fmt.Sprintf("%d|%d", e.Modified.Unix(), e.Size)
		}
		op, _ := json.Marshal(opaque{Path: p})
		return req.Emit(connectors.SourceItem{
			ID: p, Name: path.Base(p), Path: p, Kind: connectors.KindFile, Revision: rev,
			ModifiedAt: e.Modified, MIMEType: e.MIME, Size: e.Size,
			Metadata: map[string]string{"webdav_href": cl.resolve(p), "webdav_path": p, "file_name": path.Base(p)},
			Opaque:   op,
		})
	}

	// Incremental via sync-token.
	if useSync && prev.SyncToken != "" && !req.Full {
		token, err := cl.syncCollection(ctx, root, prev.SyncToken, recursive, emit)
		if err == nil {
			cur, _ := json.Marshal(cursorState{SyncToken: token})
			return connectors.ScanResult{Cursor: cur, Complete: false}, nil
		}
		if ctx.Err() != nil {
			return connectors.ScanResult{}, ctx.Err()
		}
		req.Log("warn", "sync-collection failed, falling back to full inventory: "+err.Error())
	}

	// Initial token before the inventory, so changes made during the scan
	// are replayed next time.
	token := ""
	if useSync {
		if _, t, err := cl.syncCollectionInitial(ctx, root, recursive); err == nil {
			token = t
		}
	}
	if err := cl.walk(ctx, root, recursive, emit); err != nil {
		return connectors.ScanResult{}, err
	}
	cur, _ := json.Marshal(cursorState{SyncToken: token})
	return connectors.ScanResult{Cursor: cur, Complete: true}, nil
}

// walk lists the tree with Depth: 1 PROPFINDs (Depth: infinity is often
// disabled on servers).
func (c *client) walk(ctx context.Context, root string, recursive bool, fn func(entry) error) error {
	queue := []string{root}
	visited := map[string]bool{}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if visited[dir] {
			continue
		}
		visited[dir] = true
		entries, _, err := c.propfind(ctx, dir, "1")
		if err != nil {
			return fmt.Errorf("list %s: %w", dir, err)
		}
		for _, e := range entries {
			p := path.Clean(e.Path)
			if p == path.Clean(dir) {
				continue
			}
			if e.IsDir {
				if recursive {
					queue = append(queue, p)
				}
				continue
			}
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return nil
}

const syncBody = `<?xml version="1.0" encoding="utf-8"?>
<d:sync-collection xmlns:d="DAV:"><d:sync-token>%s</d:sync-token><d:sync-level>%s</d:sync-level>
<d:prop><d:resourcetype/><d:getcontentlength/><d:getcontenttype/><d:getetag/><d:getlastmodified/></d:prop></d:sync-collection>`

func (c *client) report(ctx context.Context, root, token string, recursive bool) ([]entry, string, error) {
	level := "1"
	if recursive {
		level = "infinite"
	}
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(token))
	resp, err := c.http.Do(ctx, httpx.Request{
		Method: "REPORT", URL: c.resolve(root),
		Header: http.Header{"Depth": {"0"}, "Content-Type": {"application/xml; charset=utf-8"}},
		Body:   []byte(fmt.Sprintf(syncBody, b.String(), level)),
	})
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	entries, newToken, err := c.parse(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if newToken == "" {
		return nil, "", errors.New("server returned no sync-token")
	}
	return entries, newToken, nil
}

func (c *client) syncCollectionInitial(ctx context.Context, root string, recursive bool) ([]entry, string, error) {
	return c.report(ctx, root, "", recursive)
}

func (c *client) syncCollection(ctx context.Context, root, token string, recursive bool, fn func(entry) error) (string, error) {
	entries, newToken, err := c.report(ctx, root, token, recursive)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if err := fn(e); err != nil {
			return "", err
		}
	}
	return newToken, nil
}

// OpenContent streams the file with GET.
func (c *Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	cl, err := newClient(req.Credential)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	resp, err := cl.http.DoStream(ctx, http.MethodGet, cl.resolve(req.Item.Path), nil, nil)
	if err != nil {
		if httpx.IsStatus(err, http.StatusNotFound) {
			return connectors.SourceContent{}, connectors.Skip("file no longer exists")
		}
		return connectors.SourceContent{}, err
	}
	mt := req.Item.MIMEType
	if mt == "" {
		mt = resp.Header.Get("Content-Type")
	}
	return connectors.SourceContent{
		Body: resp.Body, FileName: req.Item.Name, MIMEType: mt, Size: resp.ContentLength,
		Context:   fmt.Sprintf("File %q from WebDAV", req.Item.Path),
		Timestamp: req.Item.ModifiedAt,
	}, nil
}

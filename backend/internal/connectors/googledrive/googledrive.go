// Package googledrive implements the Google Drive source connector.
package googledrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

var apiBase = "https://www.googleapis.com/drive/v3"

const fileFields = "id,name,mimeType,size,modifiedTime,createdTime,md5Checksum,version,trashed,parents,driveId,webViewLink"

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type:        "google_drive",
		Name:        "Google Drive",
		Description: "Google account via OAuth (read-only Drive access)",
		OAuth:       true,
		Fields: []connectors.FieldSpec{
			{Key: "clientId", Label: "OAuth client ID", Type: connectors.FieldString, Required: true},
			{Key: "clientSecret", Label: "OAuth client secret", Type: connectors.FieldPassword, Secret: true, Required: true},
		},
	})
	connectors.RegisterSource(&Connector{})
}

// Connector implements Google Drive as a source.
type Connector struct{}

// Info describes the connector.
func (c *Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type:           "google_drive",
		Name:           "Google Drive",
		CredentialType: "google_drive",
		Capabilities: connectors.Capabilities{
			IncrementalMode:        connectors.IncrementalDeltaToken,
			DeletionMode:           connectors.DeletionDelta,
			SupportsFiles:          true,
			SupportsOAuth:          true,
			SupportsAdvancedFilter: true,
		},
		BrowseKinds: []string{connectors.KindDrive, connectors.KindFolder},
		Fields: []connectors.FieldSpec{
			{Key: "driveId", Label: "Drive", Type: connectors.FieldString, Browse: true, BrowseKind: connectors.KindDrive,
				Help: "Leave empty for My Drive, or pick a shared drive.", Effect: connectors.EffectRebaseline},
			{Key: "folderId", Label: "Folder", Type: connectors.FieldString, Browse: true, BrowseKind: connectors.KindFolder,
				Help: "Leave empty for the whole drive.", Effect: connectors.EffectRebaseline},
			{Key: "recursive", Label: "Include sub-folders", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
			{Key: "exportGoogleDocs", Label: "Export Google Docs/Sheets/Slides", Type: connectors.FieldBoolean, Default: true,
				Help: "Docs → Markdown, Sheets → CSV, Slides → PDF.", Effect: connectors.EffectReconcile},
		},
		FilterFields: []connectors.FilterFieldSpec{
			{Key: "name", Label: "File name", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "extension", Label: "Extension", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "mimeType", Label: "MIME type", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "fileType", Label: "File type", Type: connectors.FilterEnum, Operators: connectors.EnumOps, Options: []connectors.Option{
				{Value: connectors.GroupPlainText, Label: "Plain text"}, {Value: connectors.GroupDocuments, Label: "Documents"},
				{Value: connectors.GroupImages, Label: "Images"}, {Value: connectors.GroupAudios, Label: "Audios"},
			}},
			{Key: "size", Label: "Size (bytes)", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
			{Key: "modifiedAt", Label: "Modified", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
		},
	}
}

func client(cred connectors.Credential) *httpx.Client {
	token := ""
	if cred.OAuth != nil {
		token = cred.OAuth.AccessToken
	}
	c := httpx.New(cred.Headers, map[string]string{"Authorization": "Bearer " + token})
	c.Token = cred.AccessToken
	return c
}

// ValidateCredential fetches the signed-in user.
func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	if cred.OAuth == nil || cred.OAuth.AccessToken == "" {
		return "", errors.New("not connected; use OAuth Connect")
	}
	var about struct {
		User struct {
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
		} `json:"user"`
	}
	if err := client(cred).JSON(ctx, http.MethodGet, apiBase+"/about?fields=user", nil, &about); err != nil {
		return "", err
	}
	return fmt.Sprintf("Connected as %s <%s>", about.User.DisplayName, about.User.EmailAddress), nil
}

// ValidateConfig checks configuration.
func (c *Connector) ValidateConfig(cfg map[string]any, f connectors.Filter) error {
	if f.Mode == "advanced" && strings.TrimSpace(f.AdvancedQuery) == "" {
		return errors.New("filter: advanced query must not be empty")
	}
	return connectors.ValidateFilter(f, c.Info().FilterFields, true)
}

type file struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	MimeType     string    `json:"mimeType"`
	Size         string    `json:"size"`
	ModifiedTime time.Time `json:"modifiedTime"`
	MD5          string    `json:"md5Checksum"`
	Version      string    `json:"version"`
	Trashed      bool      `json:"trashed"`
	Parents      []string  `json:"parents"`
	DriveID      string    `json:"driveId"`
	WebViewLink  string    `json:"webViewLink"`
}

const folderMime = "application/vnd.google-apps.folder"

// Browse lists drives at the root (My Drive = "root", shared drives by
// ID) and sub-folders below. Folder items are selectable for folderId;
// drive items are selectable for driveId.
func (c *Connector) Browse(ctx context.Context, cred connectors.Credential, req connectors.BrowseRequest) (connectors.BrowseResult, error) {
	cl := client(cred)
	res := connectors.BrowseResult{Breadcrumbs: []connectors.Breadcrumb{{ID: "", Name: "Drives"}}}
	pickDrive := req.Kind == connectors.KindDrive
	parent := req.ParentID
	if parent == "" && !pickDrive {
		// Folder picker: start inside the configured drive when set.
		parent = connectors.AsString(req.Config["driveId"])
	}
	if parent == "" {
		res.Items = append(res.Items, connectors.BrowseItem{ID: "root", Name: "My Drive", Kind: connectors.KindDrive, Path: "My Drive",
			HasChildren: !pickDrive, Selectable: pickDrive})
		page := ""
		for {
			var out struct {
				Drives []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"drives"`
				NextPageToken string `json:"nextPageToken"`
			}
			q := url.Values{"pageSize": {"100"}}
			if page != "" {
				q.Set("pageToken", page)
			}
			if err := cl.JSON(ctx, http.MethodGet, apiBase+"/drives?"+q.Encode(), nil, &out); err != nil {
				return res, err
			}
			for _, d := range out.Drives {
				res.Items = append(res.Items, connectors.BrowseItem{ID: d.ID, Name: d.Name, Kind: connectors.KindDrive, Path: d.Name,
					HasChildren: !pickDrive, Selectable: pickDrive})
			}
			if out.NextPageToken == "" {
				break
			}
			page = out.NextPageToken
		}
		if pickDrive {
			// "root" means My Drive, which is stored as an empty driveId.
			res.Items[0].ID = ""
		}
		return res, nil
	}

	q := url.Values{
		"q":                         {fmt.Sprintf("'%s' in parents and mimeType = '%s' and trashed = false", escapeQ(parent), folderMime)},
		"fields":                    {"files(id,name)"},
		"pageSize":                  {"1000"},
		"orderBy":                   {"name"},
		"supportsAllDrives":         {"true"},
		"includeItemsFromAllDrives": {"true"},
	}
	var out struct {
		Files []file `json:"files"`
	}
	if err := cl.JSON(ctx, http.MethodGet, apiBase+"/files?"+q.Encode(), nil, &out); err != nil {
		return res, err
	}
	res.Breadcrumbs = append(res.Breadcrumbs, connectors.Breadcrumb{ID: parent, Name: folderName(ctx, cl, parent)})
	for _, f := range out.Files {
		res.Items = append(res.Items, connectors.BrowseItem{ID: f.ID, Name: f.Name, Kind: connectors.KindFolder, Path: f.Name, HasChildren: true, Selectable: true})
	}
	return res, nil
}

func folderName(ctx context.Context, cl *httpx.Client, id string) string {
	if id == "root" {
		return "My Drive"
	}
	var f file
	if err := cl.JSON(ctx, http.MethodGet, apiBase+"/files/"+url.PathEscape(id)+"?fields=name&supportsAllDrives=true", nil, &f); err == nil && f.Name != "" {
		return f.Name
	}
	var d struct {
		Name string `json:"name"`
	}
	if err := cl.JSON(ctx, http.MethodGet, apiBase+"/drives/"+url.PathEscape(id), nil, &d); err == nil && d.Name != "" {
		return d.Name
	}
	return id
}

type cursorState struct {
	PageToken string `json:"pageToken"`
}

type opaque struct {
	MimeType string `json:"m"`
}

// Scan: initial sync gets a start token, performs a baseline inventory,
// then commits the start token so changes made during the baseline are
// replayed by the next run. Incremental runs consume the Changes API.
func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	cl := client(req.Credential)
	driveID := connectors.AsString(req.Config["driveId"])
	folderID := connectors.AsString(req.Config["folderId"])
	recursive := connectors.BoolDefault(req.Config, "recursive", true)
	export := connectors.BoolDefault(req.Config, "exportGoogleDocs", true)
	advanced := ""
	if req.Filter.Mode == "advanced" {
		advanced = strings.TrimSpace(req.Filter.AdvancedQuery)
	}

	var prev cursorState
	if len(req.Cursor) > 0 && !req.Full {
		_ = json.Unmarshal(req.Cursor, &prev)
	}
	sc := &scanner{cl: cl, driveID: driveID, root: folderID, recursive: recursive, export: export, emit: req.Emit, paths: map[string]string{}, folders: map[string]*folderInfo{}}

	if prev.PageToken != "" && advanced == "" {
		token, err := sc.changes(ctx, prev.PageToken)
		if err == nil {
			cur, _ := json.Marshal(cursorState{PageToken: token})
			return connectors.ScanResult{Cursor: cur}, nil
		}
		if ctx.Err() != nil {
			return connectors.ScanResult{}, ctx.Err()
		}
		req.Log("warn", "Changes API failed; performing full inventory: "+err.Error())
	}

	start, err := sc.startToken(ctx)
	if err != nil {
		return connectors.ScanResult{}, fmt.Errorf("get start page token: %w", err)
	}
	if err := sc.inventory(ctx, advanced); err != nil {
		return connectors.ScanResult{}, err
	}
	// With an advanced query the Changes API cannot apply the same filter,
	// so every run is a full inventory (cursor stays empty).
	if advanced != "" {
		return connectors.ScanResult{Cursor: json.RawMessage(`{}`), Complete: true}, nil
	}
	cur, _ := json.Marshal(cursorState{PageToken: start})
	return connectors.ScanResult{Cursor: cur, Complete: true}, nil
}

type scanner struct {
	cl        *httpx.Client
	driveID   string
	root      string
	recursive bool
	export    bool
	emit      func(connectors.SourceItem) error
	paths     map[string]string // folder id → path (inventory)
	folders   map[string]*folderInfo
	rootID    string
}

func (s *scanner) baseQuery() url.Values {
	q := url.Values{"supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
	if s.driveID != "" {
		q.Set("driveId", s.driveID)
	}
	return q
}

func (s *scanner) startToken(ctx context.Context) (string, error) {
	q := s.baseQuery()
	q.Del("includeItemsFromAllDrives")
	var out struct {
		StartPageToken string `json:"startPageToken"`
	}
	if err := s.cl.JSON(ctx, http.MethodGet, apiBase+"/changes/startPageToken?"+q.Encode(), nil, &out); err != nil {
		return "", err
	}
	return out.StartPageToken, nil
}

func (s *scanner) list(ctx context.Context, query string, fn func(file) error) error {
	page := ""
	for {
		q := s.baseQuery()
		q.Set("q", query)
		q.Set("fields", "nextPageToken,files("+fileFields+")")
		q.Set("pageSize", "1000")
		if s.driveID != "" {
			q.Set("corpora", "drive")
		} else {
			q.Set("corpora", "user")
		}
		if page != "" {
			q.Set("pageToken", page)
		}
		var out struct {
			Files         []file `json:"files"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := s.cl.JSON(ctx, http.MethodGet, apiBase+"/files?"+q.Encode(), nil, &out); err != nil {
			return err
		}
		for _, f := range out.Files {
			if err := fn(f); err != nil {
				return err
			}
		}
		if out.NextPageToken == "" {
			return nil
		}
		page = out.NextPageToken
	}
}

// inventory walks the folder tree (or runs the advanced query).
func (s *scanner) inventory(ctx context.Context, advanced string) error {
	if advanced != "" {
		return s.list(ctx, "("+advanced+") and trashed = false and mimeType != '"+folderMime+"'", func(f file) error {
			return s.emitFile(f, "/"+f.Name)
		})
	}
	root := s.root
	if root == "" {
		root = s.driveID
	}
	if root == "" {
		root = "root"
	}
	s.paths[root] = ""
	queue := []string{root}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		err := s.list(ctx, fmt.Sprintf("'%s' in parents and trashed = false", escapeQ(dir)), func(f file) error {
			p := s.paths[dir] + "/" + f.Name
			if f.MimeType == folderMime {
				s.paths[f.ID] = p
				if s.recursive {
					queue = append(queue, f.ID)
				}
				return nil
			}
			return s.emitFile(f, p)
		})
		if err != nil {
			return fmt.Errorf("list folder: %w", err)
		}
	}
	return nil
}

// changes consumes the Changes API from token, returning the new start token.
func (s *scanner) changes(ctx context.Context, token string) (string, error) {
	type change struct {
		removed bool
		file    *file
	}
	// A file can appear several times across pages; only the last change
	// reflects its current state.
	latest := map[string]change{}
	var order []string
	newToken := ""
	for newToken == "" {
		q := s.baseQuery()
		q.Set("pageToken", token)
		q.Set("pageSize", "1000")
		q.Set("includeRemoved", "true")
		q.Set("fields", "nextPageToken,newStartPageToken,changes(fileId,removed,changeType,file("+fileFields+"))")
		var out struct {
			Changes []struct {
				FileID     string `json:"fileId"`
				Removed    bool   `json:"removed"`
				ChangeType string `json:"changeType"`
				File       *file  `json:"file"`
			} `json:"changes"`
			NextPageToken     string `json:"nextPageToken"`
			NewStartPageToken string `json:"newStartPageToken"`
		}
		if err := s.cl.JSON(ctx, http.MethodGet, apiBase+"/changes?"+q.Encode(), nil, &out); err != nil {
			return "", err
		}
		for _, ch := range out.Changes {
			if ch.ChangeType == "drive" {
				continue
			}
			if _, seen := latest[ch.FileID]; !seen {
				order = append(order, ch.FileID)
			}
			latest[ch.FileID] = change{removed: ch.Removed || ch.File == nil || ch.File.Trashed, file: ch.File}
		}
		switch {
		case out.NewStartPageToken != "":
			newToken = out.NewStartPageToken
		case out.NextPageToken == "":
			return "", errors.New("changes response without page token")
		default:
			token = out.NextPageToken
		}
	}

	for _, id := range order {
		ch := latest[id]
		if ch.removed {
			if err := s.emit(connectors.SourceItem{ID: id, Kind: connectors.KindFile, Deleted: true}); err != nil {
				return "", err
			}
			continue
		}
		f := *ch.file
		if f.MimeType == folderMime {
			continue
		}
		p, in, err := s.resolvePath(ctx, f)
		if err != nil {
			return "", err
		}
		if !in {
			// Moved out of scope: report as deleted for this task.
			if err := s.emit(connectors.SourceItem{ID: f.ID, Kind: connectors.KindFile, Deleted: true}); err != nil {
				return "", err
			}
			continue
		}
		if err := s.emitFile(f, p); err != nil {
			return "", err
		}
	}
	return newToken, nil
}

// resolvePath walks parents up to the configured root to compute the path
// and decide whether the file is within scope.
func (s *scanner) resolvePath(ctx context.Context, f file) (string, bool, error) {
	segments := []string{f.Name}
	parents := f.Parents
	for depth := 0; depth < 64; depth++ {
		if len(parents) == 0 {
			// Top of a tree without meeting a configured folder.
			return "/" + strings.Join(reverse(segments), "/"), s.root == "", nil
		}
		pid := parents[0]
		if s.root != "" && pid == s.root {
			return "/" + strings.Join(reverse(segments), "/"), s.recursive || depth == 0, nil
		}
		if s.root == "" && s.isDriveRoot(ctx, pid) {
			return "/" + strings.Join(reverse(segments), "/"), s.recursive || depth == 0, nil
		}
		info, err := s.folder(ctx, pid)
		if err != nil {
			return "", false, err
		}
		if info == nil {
			return "", false, nil
		}
		segments = append(segments, info.Name)
		parents = info.Parents
	}
	return "", false, nil
}

type folderInfo struct {
	Name    string   `json:"name"`
	Parents []string `json:"parents"`
}

// folder returns (cached) folder name and parents; nil when inaccessible.
func (s *scanner) folder(ctx context.Context, id string) (*folderInfo, error) {
	if fi, ok := s.folders[id]; ok {
		return fi, nil
	}
	var fi folderInfo
	q := url.Values{"fields": {"name,parents"}, "supportsAllDrives": {"true"}}
	if err := s.cl.JSON(ctx, http.MethodGet, apiBase+"/files/"+url.PathEscape(id)+"?"+q.Encode(), nil, &fi); err != nil {
		if httpx.IsStatus(err, http.StatusNotFound) {
			s.folders[id] = nil
			return nil, nil
		}
		return nil, err
	}
	s.folders[id] = &fi
	return &fi, nil
}

func (s *scanner) isDriveRoot(ctx context.Context, id string) bool {
	if s.driveID != "" {
		return id == s.driveID
	}
	if s.rootID == "" {
		var f file
		if err := s.cl.JSON(ctx, http.MethodGet, apiBase+"/files/root?fields=id", nil, &f); err != nil {
			return false
		}
		s.rootID = f.ID
	}
	return id == s.rootID
}

func (s *scanner) emitFile(f file, p string) error {
	name, mime := f.Name, f.MimeType
	if strings.HasPrefix(f.MimeType, "application/vnd.google-apps.") {
		ext, exportMime, ok := exportFormat(f.MimeType)
		if !ok || !s.export {
			return nil
		}
		name += ext
		p += ext
		mime = exportMime
	}
	var size int64
	fmt.Sscan(f.Size, &size)
	rev := f.MD5
	if rev == "" {
		rev = f.Version
	}
	rev += "|" + f.ModifiedTime.UTC().Format(time.RFC3339Nano)
	op, _ := json.Marshal(opaque{MimeType: f.MimeType})
	md := map[string]string{"google_file_id": f.ID, "file_name": name, "google_web_view_link": f.WebViewLink}
	tags := []string{}
	if f.DriveID != "" {
		md["google_drive_id"] = f.DriveID
		tags = append(tags, "google_drive_id:"+f.DriveID)
	}
	return s.emit(connectors.SourceItem{
		ID: f.ID, Name: name, Path: p, Kind: connectors.KindFile, Revision: rev,
		ModifiedAt: f.ModifiedTime, MIMEType: mime, Size: size, Metadata: md, Tags: tags, Opaque: op,
	})
}

func exportFormat(mime string) (ext, exportMime string, ok bool) {
	switch mime {
	case "application/vnd.google-apps.document":
		return ".md", "text/markdown", true
	case "application/vnd.google-apps.spreadsheet":
		return ".csv", "text/csv", true
	case "application/vnd.google-apps.presentation":
		return ".pdf", "application/pdf", true
	}
	return "", "", false
}

// OpenContent streams the file (or its export).
func (c *Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	var op opaque
	_ = json.Unmarshal(req.Item.Opaque, &op)
	u := apiBase + "/files/" + url.PathEscape(req.Item.ID) + "?alt=media&supportsAllDrives=true"
	if strings.HasPrefix(op.MimeType, "application/vnd.google-apps.") {
		_, exportMime, ok := exportFormat(op.MimeType)
		if !ok {
			return connectors.SourceContent{}, connectors.Skip("unsupported Google Workspace type")
		}
		u = apiBase + "/files/" + url.PathEscape(req.Item.ID) + "/export?mimeType=" + url.QueryEscape(exportMime)
	}
	resp, err := client(req.Credential).DoStream(ctx, http.MethodGet, u, nil, nil)
	if err != nil {
		if httpx.IsStatus(err, http.StatusNotFound) {
			return connectors.SourceContent{}, connectors.Skip("file no longer exists")
		}
		return connectors.SourceContent{}, err
	}
	return connectors.SourceContent{
		Body: resp.Body, FileName: req.Item.Name, MIMEType: req.Item.MIMEType, Size: resp.ContentLength,
		Context:   fmt.Sprintf("Google Drive file %q", req.Item.Path),
		Timestamp: req.Item.ModifiedAt,
	}, nil
}

func escapeQ(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

func reverse(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

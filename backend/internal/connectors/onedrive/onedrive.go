// Package onedrive implements the OneDrive / SharePoint source connector via
// Microsoft Graph.
package onedrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

var graphBase = "https://graph.microsoft.com/v1.0"

const selectFields = "id,name,size,file,folder,deleted,parentReference,lastModifiedDateTime,eTag,cTag,webUrl,root"

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type:        "onedrive",
		Name:        "OneDrive",
		Description: "Microsoft account / Microsoft 365 via OAuth (OneDrive and SharePoint)",
		OAuth:       true,
		Fields: []connectors.FieldSpec{
			{Key: "clientId", Label: "Application (client) ID", Type: connectors.FieldString, Required: true},
			{Key: "clientSecret", Label: "Client secret", Type: connectors.FieldPassword, Secret: true, Required: true},
			{Key: "tenant", Label: "Tenant", Type: connectors.FieldString, Default: "common", Placeholder: "common",
				Help: "common, organizations, consumers, or a tenant ID."},
		},
	})
	connectors.RegisterSource(&Connector{})
}

// Connector implements OneDrive as a source.
type Connector struct{}

// Info describes the connector.
func (c *Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type:           "onedrive",
		Name:           "OneDrive",
		CredentialType: "onedrive",
		Capabilities: connectors.Capabilities{
			IncrementalMode: connectors.IncrementalDeltaToken,
			DeletionMode:    connectors.DeletionDelta,
			SupportsFiles:   true,
			SupportsOAuth:   true,
		},
		BrowseKinds: []string{connectors.KindDrive, connectors.KindFolder},
		Fields: []connectors.FieldSpec{
			{Key: "driveId", Label: "Drive", Type: connectors.FieldString, Browse: true, BrowseKind: connectors.KindDrive,
				Help: "Leave empty for your personal OneDrive.", Effect: connectors.EffectRebaseline},
			{Key: "folderId", Label: "Folder", Type: connectors.FieldString, Browse: true, BrowseKind: connectors.KindFolder,
				Help: "Leave empty for the whole drive.", Effect: connectors.EffectRebaseline},
			{Key: "recursive", Label: "Include sub-folders", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
		},
		FilterFields: []connectors.FilterFieldSpec{
			{Key: "name", Label: "File name", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "extension", Label: "Extension", Type: connectors.FilterString, Operators: connectors.StringOps},
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
	var me struct {
		DisplayName       string `json:"displayName"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := client(cred).JSON(ctx, http.MethodGet, graphBase+"/me?$select=displayName,userPrincipalName", nil, &me); err != nil {
		return "", err
	}
	return fmt.Sprintf("Connected as %s <%s>", me.DisplayName, me.UserPrincipalName), nil
}

// ValidateConfig checks configuration.
func (c *Connector) ValidateConfig(cfg map[string]any, f connectors.Filter) error {
	return connectors.ValidateFilter(f, c.Info().FilterFields, false)
}

type driveItem struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Size                 int64     `json:"size"`
	ETag                 string    `json:"eTag"`
	CTag                 string    `json:"cTag"`
	WebURL               string    `json:"webUrl"`
	LastModifiedDateTime time.Time `json:"lastModifiedDateTime"`
	File                 *struct {
		MimeType string `json:"mimeType"`
	} `json:"file"`
	Folder  *struct{} `json:"folder"`
	Deleted *struct{} `json:"deleted"`
	Root    *struct{} `json:"root"`
	Parent  struct {
		DriveID string `json:"driveId"`
		ID      string `json:"id"`
		Path    string `json:"path"`
	} `json:"parentReference"`
}

func driveBase(driveID string) string {
	if driveID == "" {
		return graphBase + "/me/drive"
	}
	return graphBase + "/drives/" + url.PathEscape(driveID)
}

// Browse lists drives at the root, then folders.
func (c *Connector) Browse(ctx context.Context, cred connectors.Credential, req connectors.BrowseRequest) (connectors.BrowseResult, error) {
	cl := client(cred)
	res := connectors.BrowseResult{Breadcrumbs: []connectors.Breadcrumb{{ID: "", Name: "Drives"}}}
	pickDrive := req.Kind == connectors.KindDrive
	driveID := connectors.AsString(req.Config["driveId"])
	parent := req.ParentID
	if parent == "" && pickDrive {
		var out struct {
			Value []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				DriveType string `json:"driveType"`
			} `json:"value"`
		}
		if err := cl.JSON(ctx, http.MethodGet, graphBase+"/me/drives", nil, &out); err != nil {
			return res, err
		}
		res.Items = append(res.Items, connectors.BrowseItem{ID: "", Name: "My OneDrive", Kind: connectors.KindDrive, Path: "My OneDrive", Selectable: true})
		for _, d := range out.Value {
			res.Items = append(res.Items, connectors.BrowseItem{ID: d.ID, Name: d.Name + " (" + d.DriveType + ")", Kind: connectors.KindDrive, Path: d.Name, Selectable: true})
		}
		return res, nil
	}
	u := driveBase(driveID) + "/root/children"
	if parent != "" {
		u = driveBase(driveID) + "/items/" + url.PathEscape(parent) + "/children"
		var it driveItem
		if err := cl.JSON(ctx, http.MethodGet, driveBase(driveID)+"/items/"+url.PathEscape(parent)+"?$select=id,name", nil, &it); err == nil {
			res.Breadcrumbs = append(res.Breadcrumbs, connectors.Breadcrumb{ID: parent, Name: it.Name})
		}
	}
	u += "?$select=id,name,folder&$top=999"
	for u != "" {
		var out struct {
			Value    []driveItem `json:"value"`
			NextLink string      `json:"@odata.nextLink"`
		}
		if err := cl.JSON(ctx, http.MethodGet, u, nil, &out); err != nil {
			return res, err
		}
		for _, it := range out.Value {
			if it.Folder != nil {
				res.Items = append(res.Items, connectors.BrowseItem{ID: it.ID, Name: it.Name, Kind: connectors.KindFolder, Path: it.Name, HasChildren: true, Selectable: true})
			}
		}
		u = out.NextLink
	}
	return res, nil
}

type folderNode struct {
	Parent string `json:"p"`
	Name   string `json:"n"`
}

type cursorState struct {
	DeltaLink string `json:"deltaLink"`
	// Folders maps folder ID → parent/name. Graph delta does not return
	// parentReference.path, so scope and paths are resolved by walking
	// ancestor IDs; the map is carried in the cursor between runs.
	Folders map[string]folderNode `json:"folders,omitempty"`
	RootID  string                `json:"rootId,omitempty"`
}

// Scan consumes the Graph delta feed. Without a cursor the delta feed
// enumerates every item (complete inventory) and returns a deltaLink.
// Items may appear several times in one feed; the last occurrence wins.
func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	cl := client(req.Credential)
	driveID := connectors.AsString(req.Config["driveId"])
	folderID := connectors.AsString(req.Config["folderId"])
	recursive := connectors.BoolDefault(req.Config, "recursive", true)

	var prev cursorState
	if len(req.Cursor) > 0 && !req.Full {
		_ = json.Unmarshal(req.Cursor, &prev)
	}
	full := prev.DeltaLink == ""
	state := cursorState{Folders: map[string]folderNode{}}
	if !full {
		for k, v := range prev.Folders {
			state.Folders[k] = v
		}
		state.RootID = prev.RootID
	}

	var driveRoot driveItem
	if err := cl.JSON(ctx, http.MethodGet, driveBase(driveID)+"/root?$select=id,parentReference", nil, &driveRoot); err != nil {
		return connectors.ScanResult{}, fmt.Errorf("resolve drive root: %w", err)
	}
	state.RootID = driveRoot.ID
	scopeID := driveRoot.ID
	if folderID != "" {
		scopeID = folderID
	}

	u := prev.DeltaLink
	if full {
		u = driveBase(driveID) + "/root/delta?$select=" + selectFields
	}
	files := map[string]driveItem{}
	var order []string
	for {
		var out struct {
			Value     []driveItem `json:"value"`
			NextLink  string      `json:"@odata.nextLink"`
			DeltaLink string      `json:"@odata.deltaLink"`
		}
		if err := cl.JSON(ctx, http.MethodGet, u, nil, &out); err != nil {
			if httpx.IsStatus(err, http.StatusGone) && !full {
				// Nothing has been emitted yet (items are emitted after the
				// whole feed is read), so a fresh full scan is safe.
				req.Log("warn", "Delta token expired; performing full resync")
				return c.Scan(ctx, connectors.ScanRequest{Credential: req.Credential, Config: req.Config, Filter: req.Filter, Full: true, Emit: req.Emit, Log: req.Log})
			}
			return connectors.ScanResult{}, fmt.Errorf("graph delta: %w", err)
		}
		for _, it := range out.Value {
			switch {
			case it.Root != nil:
				state.RootID = it.ID
			case it.Folder != nil:
				if it.Deleted != nil {
					delete(state.Folders, it.ID)
				} else {
					state.Folders[it.ID] = folderNode{Parent: it.Parent.ID, Name: it.Name}
				}
			default:
				if _, seen := files[it.ID]; !seen {
					order = append(order, it.ID)
				}
				files[it.ID] = it
			}
		}
		if out.DeltaLink != "" {
			state.DeltaLink = out.DeltaLink
			break
		}
		if out.NextLink == "" {
			return connectors.ScanResult{}, errors.New("graph delta response without next or delta link")
		}
		u = out.NextLink
	}

	for _, id := range order {
		it := files[id]
		drive := it.Parent.DriveID
		if drive == "" {
			drive = driveRoot.Parent.DriveID
		}
		key := drive + "!" + it.ID
		if it.Deleted != nil || it.File == nil {
			if err := req.Emit(connectors.SourceItem{ID: key, Kind: connectors.KindFile, Deleted: true}); err != nil {
				return connectors.ScanResult{}, err
			}
			continue
		}
		dir, depth, inScope := state.resolve(it.Parent.ID, scopeID)
		if inScope && !recursive && depth > 0 {
			inScope = false
		}
		if !inScope {
			if !full {
				// May have moved out of scope: delete if previously synced.
				if err := req.Emit(connectors.SourceItem{ID: key, Kind: connectors.KindFile, Deleted: true}); err != nil {
					return connectors.ScanResult{}, err
				}
			}
			continue
		}
		rev := it.CTag
		if rev == "" {
			rev = it.ETag
		}
		if err := req.Emit(connectors.SourceItem{
			ID: key, Name: it.Name, Path: dir + "/" + it.Name, Kind: connectors.KindFile, Revision: rev,
			ModifiedAt: it.LastModifiedDateTime, MIMEType: it.File.MimeType, Size: it.Size,
			Metadata: map[string]string{"onedrive_item_id": it.ID, "onedrive_drive_id": drive, "file_name": it.Name, "onedrive_web_url": it.WebURL},
			Tags:     []string{"onedrive_drive_id:" + drive},
		}); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	cur, _ := json.Marshal(state)
	return connectors.ScanResult{Cursor: cur, Complete: full}, nil
}

// resolve walks ancestors of folder parentID up to scopeID, returning the
// path relative to the scope, the folder depth below it, and whether the
// item is inside the scope.
func (s cursorState) resolve(parentID, scopeID string) (string, int, bool) {
	var segs []string
	id := parentID
	for depth := 0; depth < 256; depth++ {
		if id == scopeID {
			for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
				segs[i], segs[j] = segs[j], segs[i]
			}
			p := ""
			if len(segs) > 0 {
				p = "/" + strings.Join(segs, "/")
			}
			return p, len(segs), true
		}
		if id == "" || id == s.RootID {
			return "", 0, false
		}
		node, ok := s.Folders[id]
		if !ok {
			return "", 0, false
		}
		segs = append(segs, node.Name)
		id = node.Parent
	}
	return "", 0, false
}

// OpenContent streams the file via /content (Graph redirects to a
// pre-authenticated download URL).
func (c *Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	drive, itemID, ok := strings.Cut(req.Item.ID, "!")
	if !ok {
		return connectors.SourceContent{}, fmt.Errorf("invalid OneDrive item ID")
	}
	u := graphBase + "/drives/" + url.PathEscape(drive) + "/items/" + url.PathEscape(itemID) + "/content"
	resp, err := client(req.Credential).DoStream(ctx, http.MethodGet, u, nil, nil)
	if err != nil {
		if httpx.IsStatus(err, http.StatusNotFound) {
			return connectors.SourceContent{}, connectors.Skip("file no longer exists")
		}
		return connectors.SourceContent{}, err
	}
	return connectors.SourceContent{
		Body: resp.Body, FileName: req.Item.Name, MIMEType: req.Item.MIMEType, Size: resp.ContentLength,
		Context:   fmt.Sprintf("OneDrive file %q", path.Clean("/"+req.Item.Path)),
		Timestamp: req.Item.ModifiedAt,
	}, nil
}

// Package remotefs shares inventory and content handling for remote file sources.
package remotefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/google/uuid"
)

type Entry struct {
	Name      string
	Directory bool
	Regular   bool
	Size      int64
	Modified  time.Time
}

type Client interface {
	List(context.Context, string) ([]Entry, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Close() error
}

type Source struct {
	Type     string
	Name     string
	Dial     func(context.Context, connectors.Credential) (Client, error)
	sessions sync.Map
}

func Endpoint(cred connectors.Credential, defaultPort int) (string, error) {
	host := strings.TrimSpace(cred.String("host"))
	if host == "" || strings.ContainsAny(host, "/?#@\r\n\t ") {
		return "", &connectors.FieldError{Field: "host", Message: "Host must be a hostname or IP address"}
	}
	host = strings.Trim(host, "[]")
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", &connectors.FieldError{Field: "host", Message: "Invalid host IP address"}
	}
	port := defaultPort
	if raw := connectors.AsString(cred.Config["port"]); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 65535 {
			return "", &connectors.FieldError{Field: "port", Message: "Port must be between 1 and 65535"}
		}
		port = parsed
	}
	return net.JoinHostPort(strings.ToLower(host), strconv.Itoa(port)), nil
}

func Root(cfg map[string]any) string {
	root, _ := cfg["rootPath"].(string)
	if root == "" {
		return "/"
	}
	return path.Clean(root)
}

func ValidatePath(p string) error {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\r\n") {
		return &connectors.FieldError{Field: "rootPath", Message: "Remote path must be absolute and contain no control characters"}
	}
	return nil
}

func (s *Source) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type: s.Type, Name: s.Name, CredentialTypes: []string{s.Type},
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalInventory, DeletionMode: connectors.DeletionScanGeneration, SupportsFiles: true},
		BrowseKinds:  []string{connectors.KindFolder},
		Fields: []connectors.FieldSpec{
			{Key: "rootPath", Label: "Root folder", Type: connectors.FieldString, Default: "/", Browse: true, BrowseKind: connectors.KindFolder, Effect: connectors.EffectRebaseline},
			{Key: "recursive", Label: "Include sub-folders", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
		}, FilterFields: FilterFields(),
	}
}

func FilterFields() []connectors.FilterFieldSpec {
	return []connectors.FilterFieldSpec{
		{Key: "name", Label: "File name", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "extension", Label: "Extension", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "size", Label: "Size (bytes)", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
		{Key: "modifiedAt", Label: "Modified", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
	}
}

func (s *Source) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	if err := ValidatePath(Root(cfg)); err != nil {
		return err
	}
	return connectors.ValidateFilter(filter, s.Info().FilterFields, false)
}

func (s *Source) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	client, err := s.Dial(ctx, cred)
	if err != nil {
		return "", err
	}
	defer client.Close()
	_, err = client.List(ctx, "/")
	return s.Name + " connection is readable", err
}

func (s *Source) Browse(ctx context.Context, cred connectors.Credential, req connectors.BrowseRequest) (connectors.BrowseResult, error) {
	parent := req.ParentID
	if parent == "" {
		parent = "/"
	}
	if err := ValidatePath(parent); err != nil {
		return connectors.BrowseResult{}, err
	}
	parent = path.Clean(parent)
	client, err := s.Dial(ctx, cred)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	defer client.Close()
	entries, err := client.List(ctx, parent)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	res := connectors.BrowseResult{ParentID: parent, Items: []connectors.BrowseItem{}, Breadcrumbs: []connectors.Breadcrumb{{ID: "/", Name: "Root"}}}
	current := "/"
	if parent != "/" {
		for _, part := range strings.Split(strings.TrimPrefix(parent, "/"), "/") {
			current = path.Join(current, part)
			res.Breadcrumbs = append(res.Breadcrumbs, connectors.Breadcrumb{ID: current, Name: part})
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if !entry.Directory || entry.Name == "." || entry.Name == ".." {
			continue
		}
		if err := validName(entry.Name); err != nil {
			return res, err
		}
		p := path.Join(parent, entry.Name)
		res.Items = append(res.Items, connectors.BrowseItem{ID: p, Path: p, Name: entry.Name, Kind: connectors.KindFolder, Selectable: true, HasChildren: true})
	}
	return paginate(req, res)
}

func paginate(req connectors.BrowseRequest, res connectors.BrowseResult) (connectors.BrowseResult, error) {
	sort.Slice(res.Items, func(i, j int) bool { return res.Items[i].Path < res.Items[j].Path })
	offset := 0
	if req.Cursor != "" {
		var err error
		offset, err = strconv.Atoi(req.Cursor)
		if err != nil || offset < 0 || offset > len(res.Items) {
			return res, errors.New("invalid browse cursor")
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	end := min(offset+limit, len(res.Items))
	if end < len(res.Items) {
		res.NextCursor = strconv.Itoa(end)
	}
	res.Items = res.Items[offset:end]
	return res, nil
}

func validName(name string) error {
	if name == "" || strings.ContainsAny(name, "/\x00\r\n") {
		return errors.New("invalid filename in remote directory listing")
	}
	return nil
}

func Identity(cred connectors.Credential, protocol, remotePath string) string {
	defaultPort := 22
	if protocol == "ftp" {
		defaultPort = 21
		if cred.String("security") == "implicit" {
			defaultPort = 990
		}
	}
	origin, _ := Endpoint(cred, defaultPort)
	sum := sha256.Sum256([]byte(protocol + "\x00" + cred.ID + "\x00" + origin + "\x00" + cred.String("username") + "\x00" + path.Clean(remotePath)))
	return hex.EncodeToString(sum[:])
}

func Item(cred connectors.Credential, protocol, remotePath string, entry Entry) connectors.SourceItem {
	revision := ""
	if !entry.Modified.IsZero() {
		revision = fmt.Sprintf("%d:%d", entry.Modified.UnixNano(), entry.Size)
	}
	return connectors.SourceItem{ID: Identity(cred, protocol, remotePath), Name: entry.Name, Path: remotePath, Kind: connectors.KindFile, Revision: revision, ModifiedAt: entry.Modified, Size: entry.Size, MIMEType: mime.TypeByExtension(strings.ToLower(path.Ext(remotePath)))}
}

func (s *Source) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	if err := s.ValidateConfig(req.Config, req.Filter); err != nil {
		return connectors.ScanResult{}, err
	}
	client, err := s.Dial(ctx, req.Credential)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	defer client.Close()
	token := uuid.NewString()
	s.sessions.Store(token, client)
	defer s.sessions.Delete(token)
	inventory := inventory{sourceType: s.Type, client: client, req: req, token: token}
	if err := inventory.walk(ctx); err != nil {
		return connectors.ScanResult{}, err
	}
	return connectors.ScanResult{Cursor: json.RawMessage(`{}`), Complete: true}, nil
}

// A successful ancestor listing distinguishes a removed root from a denied listing.
func missingRoot(ctx context.Context, client Client, root string) bool {
	for root != "/" {
		parent := path.Dir(root)
		entries, err := client.List(ctx, parent)
		if ctx.Err() != nil {
			return false
		}
		if err != nil {
			root = parent
			continue
		}
		for _, entry := range entries {
			if validName(entry.Name) != nil {
				return false
			}
			if entry.Name == path.Base(root) {
				return false
			}
		}
		return true
	}
	return false
}

type ownedBody struct {
	io.ReadCloser
	client Client
}

func (b *ownedBody) Close() error { return errors.Join(b.ReadCloser.Close(), b.client.Close()) }

func (s *Source) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	if err := ValidatePath(req.Item.Path); err != nil {
		return connectors.SourceContent{}, err
	}
	var token string
	_ = json.Unmarshal(req.Item.Opaque, &token)
	cached, ok := s.sessions.Load(token)
	var client Client
	var err error
	if ok {
		client = cached.(Client)
	} else {
		client, err = s.Dial(ctx, req.Credential)
	}
	if err != nil {
		return connectors.SourceContent{}, err
	}
	body, err := client.Open(ctx, req.Item.Path)
	if err != nil {
		if !ok {
			_ = client.Close()
		}
		return connectors.SourceContent{}, err
	}
	if !ok {
		body = &ownedBody{ReadCloser: body, client: client}
	}
	return connectors.SourceContent{Body: body, FileName: req.Item.Name, MIMEType: req.Item.MIMEType, Size: req.Item.Size, Timestamp: req.Item.ModifiedAt, Context: req.Item.Path}, nil
}

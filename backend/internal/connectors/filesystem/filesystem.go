// Package filesystem reads files within a credential's local root directory.
package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: "filesystem", Name: "File System", Description: "Local files accessible to the application.",
		Fields: []connectors.FieldSpec{{Key: "rootPath", Label: "Root directory", Type: connectors.FieldString, Required: true,
			Help: "Absolute path on the application server. In Docker, use the mounted container path."}},
	})
	connectors.RegisterSource(&Source{})
}

type Source struct{}

func (*Source) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type: "filesystem", Name: "File System", CredentialType: "filesystem",
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalInventory, DeletionMode: connectors.DeletionScanGeneration, SupportsFiles: true},
		BrowseKinds:  []string{connectors.KindFolder},
		Fields: []connectors.FieldSpec{
			{Key: "folder", Label: "Folder", Type: connectors.FieldString, Default: ".", Browse: true, BrowseKind: connectors.KindFolder, Effect: connectors.EffectRebaseline, Help: "Relative to the credential's root directory. Use . for the root."},
			{Key: "recursive", Label: "Include sub-folders", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
		},
		FilterFields: []connectors.FilterFieldSpec{
			{Key: "name", Label: "File name", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "extension", Label: "Extension", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "size", Label: "Size (bytes)", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
			{Key: "modifiedAt", Label: "Modified", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
		},
	}
}

func openRoot(cred connectors.Credential) (*os.Root, error) {
	rootPath := cred.String("rootPath")
	if !filepath.IsAbs(rootPath) {
		return nil, errors.New("root directory must be an absolute path")
	}
	return os.OpenRoot(rootPath)
}

func folder(cfg map[string]any) string {
	if p := connectors.AsString(cfg["folder"]); p != "" {
		return p
	}
	return "."
}

func (*Source) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	root, err := openRoot(cred)
	if err != nil {
		return "", err
	}
	defer root.Close()
	_, err = fs.ReadDir(root.FS(), ".")
	return "Local directory is readable", err
}

func (s *Source) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	if !fs.ValidPath(folder(cfg)) || strings.Contains(folder(cfg), "\\") {
		return errors.New("folder must be a relative path within the root directory")
	}
	return connectors.ValidateFilter(filter, s.Info().FilterFields, false)
}

func (*Source) Browse(ctx context.Context, cred connectors.Credential, req connectors.BrowseRequest) (connectors.BrowseResult, error) {
	root, err := openRoot(cred)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	defer root.Close()
	parent := req.ParentID
	if parent == "" {
		parent = "."
	}
	entries, err := fs.ReadDir(root.FS(), parent)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	res := connectors.BrowseResult{ParentID: parent, Items: []connectors.BrowseItem{}, Breadcrumbs: []connectors.Breadcrumb{{ID: ".", Name: "Root"}}}
	if parent != "." {
		p := ""
		for _, part := range strings.Split(parent, "/") {
			p = path.Join(p, part)
			res.Breadcrumbs = append(res.Breadcrumbs, connectors.Breadcrumb{ID: p, Name: part})
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if !entry.IsDir() {
			continue
		}
		p := path.Join(parent, entry.Name())
		res.Items = append(res.Items, connectors.BrowseItem{ID: p, Name: entry.Name(), Path: p, Kind: connectors.KindFolder, HasChildren: true, Selectable: true})
	}
	return res, nil
}

func (*Source) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	root, err := openRoot(req.Credential)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	defer root.Close()
	base := folder(req.Config)
	stat, err := fs.Stat(root.FS(), base)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	if !stat.IsDir() {
		return connectors.ScanResult{}, errors.New("folder is not a directory")
	}
	recursive := connectors.BoolDefault(req.Config, "recursive", true)
	err = fs.WalkDir(root.FS(), base, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if p != base && !recursive {
				return fs.SkipDir
			}
			return nil
		}
		// Avoid following symlinks and reading devices or sockets.
		if !entry.Type().IsRegular() {
			return nil
		}
		stat, err := entry.Info()
		if err != nil {
			return err
		}
		return req.Emit(connectors.SourceItem{
			ID: p, Name: entry.Name(), Path: p, Kind: connectors.KindFile,
			Revision:   fmt.Sprintf("%d:%d", stat.ModTime().UnixNano(), stat.Size()),
			ModifiedAt: stat.ModTime(), Size: stat.Size(), MIMEType: mime.TypeByExtension(strings.ToLower(path.Ext(p))),
		})
	})
	return connectors.ScanResult{Complete: err == nil}, err
}

func (*Source) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	if err := ctx.Err(); err != nil {
		return connectors.SourceContent{}, err
	}
	root, err := openRoot(req.Credential)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	defer root.Close()
	file, err := root.Open(filepath.FromSlash(req.Item.ID))
	if err != nil {
		return connectors.SourceContent{}, err
	}
	return connectors.SourceContent{Body: file, FileName: req.Item.Name, MIMEType: req.Item.MIMEType, Size: req.Item.Size, Timestamp: req.Item.ModifiedAt, Context: req.Item.Path}, nil
}

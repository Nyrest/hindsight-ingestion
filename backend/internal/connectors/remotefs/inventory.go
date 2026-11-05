package remotefs

import (
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

type inventory struct {
	sourceType string
	client     Client
	req        connectors.ScanRequest
	token      string
}

func (i *inventory) walk(ctx context.Context) error {
	pending := []string{Root(i.req.Config)}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		children, err := i.visit(ctx, dir)
		if err != nil {
			return err
		}
		pending = append(pending, children...)
	}
	return ctx.Err()
}

func (i *inventory) entries(ctx context.Context, dir string) ([]Entry, error) {
	entries, err := i.client.List(ctx, dir)
	if err == nil {
		return entries, nil
	}
	if dir == Root(i.req.Config) && missingRoot(ctx, i.client, dir) {
		return nil, nil
	}
	return nil, fmt.Errorf("list %s: %w", dir, err)
}

func (i *inventory) visit(ctx context.Context, dir string) ([]string, error) {
	entries, err := i.entries(ctx, dir)
	if err != nil {
		return nil, err
	}
	var children []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Name == "." || entry.Name == ".." {
			continue
		}
		if err := validName(entry.Name); err != nil {
			return nil, err
		}
		p := path.Join(dir, entry.Name)
		if entry.Directory {
			if connectors.BoolDefault(i.req.Config, "recursive", true) {
				children = append(children, p)
			}
			continue
		}
		if entry.Regular {
			if err := i.emit(p, entry); err != nil {
				return nil, err
			}
		}
	}
	return children, nil
}

func (i *inventory) emit(p string, entry Entry) error {
	item := Item(i.req.Credential, i.sourceType, p, entry)
	item.Opaque, _ = json.Marshal(i.token)
	// Missing timestamps must not make changed content look unchanged forever.
	if item.Revision == "" {
		item.Revision = i.token
	}
	return i.req.Emit(item)
}

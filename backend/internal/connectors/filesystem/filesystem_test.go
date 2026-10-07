package filesystem

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestLocalFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"a.txt": "alpha", "nested/b.md": "beta"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cred := connectors.Credential{Config: map[string]any{"rootPath": root}}
	src := &Source{}
	ctx := context.Background()
	if _, err := src.ValidateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	browse, err := src.Browse(ctx, cred, connectors.BrowseRequest{})
	if err != nil || len(browse.Items) != 1 || browse.Items[0].ID != "nested" {
		t.Fatalf("browse: %+v %v", browse, err)
	}
	var items []connectors.SourceItem
	scan := func(cfg map[string]any) connectors.ScanResult {
		t.Helper()
		items = nil
		res, err := src.Scan(ctx, connectors.ScanRequest{Credential: cred, Config: cfg, Emit: func(item connectors.SourceItem) error { items = append(items, item); return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := scan(nil); !res.Complete || len(items) != 2 {
		t.Fatalf("inventory: %+v %+v", res, items)
	}
	first := items[0]
	content, err := src.OpenContent(ctx, connectors.ContentRequest{Credential: cred, Item: first})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(content.Body)
	content.Body.Close()
	if err != nil || string(body) != "alpha" {
		t.Fatalf("content %q %v", body, err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha changed"), 0644); err != nil {
		t.Fatal(err)
	}
	scan(nil)
	if items[0].Revision == first.Revision || items[0].ID != first.ID {
		t.Fatal("change must alter revision and preserve ID")
	}
	scan(map[string]any{"recursive": false})
	if len(items) != 1 {
		t.Fatalf("nonrecursive: %+v", items)
	}
	scan(map[string]any{"folder": "nested"})
	if len(items) != 1 || items[0].ID != "nested/b.md" {
		t.Fatalf("folder scope: %+v", items)
	}
	res, err := src.Scan(ctx, connectors.ScanRequest{Credential: cred, Config: map[string]any{"folder": "missing"}})
	if err == nil || res.Complete {
		t.Fatal("failed inventory must not be complete")
	}
}

func TestRootBoundary(t *testing.T) {
	cred := connectors.Credential{Config: map[string]any{"rootPath": t.TempDir()}}
	src := &Source{}
	for _, invalid := range []string{"../outside", "/absolute", "a/../../outside", "a\\..\\outside"} {
		if err := src.ValidateConfig(map[string]any{"folder": invalid}, connectors.Filter{}); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	if _, err := src.OpenContent(context.Background(), connectors.ContentRequest{Credential: cred, Item: connectors.SourceItem{ID: "../outside.txt"}}); err == nil {
		t.Fatal("read escaped root")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cred.String("rootPath"), "link")); err != nil {
		t.Skip("symlink privilege unavailable")
	}
	if _, err := src.OpenContent(context.Background(), connectors.ContentRequest{Credential: cred, Item: connectors.SourceItem{ID: "link/secret.txt"}}); err == nil {
		t.Fatal("symlink escaped root")
	}
}

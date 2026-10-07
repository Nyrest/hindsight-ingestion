package webdav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/webdav"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

// TestAgainstServer runs the connector against golang.org/x/net/webdav
// (no sync-collection support → PROPFIND inventory fallback).
func TestAgainstServer(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "notes", "deep dir"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "notes", "a.md"), []byte("# A"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "notes", "deep dir", "b ü.txt"), []byte("B"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "root.txt"), []byte("R"), 0o644))

	h := &webdav.Handler{Prefix: "/dav", FileSystem: webdav.Dir(dir), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "alice" || p != "pw" {
			w.WriteHeader(401)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()

	cred := connectors.Credential{
		Config:  map[string]any{"baseUrl": srv.URL + "/dav", "username": "alice"},
		Secrets: map[string]string{"password": "pw"},
	}
	c := &Connector{}
	ctx := context.Background()
	if _, err := c.ValidateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	scan := func(cfg map[string]any) []connectors.SourceItem {
		var items []connectors.SourceItem
		res, err := c.Scan(ctx, connectors.ScanRequest{Credential: cred, Config: cfg, Full: true,
			Emit: func(it connectors.SourceItem) error { items = append(items, it); return nil }, Log: func(string, string) {}})
		if err != nil || !res.Complete {
			t.Fatalf("scan: %v", err)
		}
		return items
	}
	items := scan(map[string]any{"rootPath": "/notes", "recursive": true})
	if len(items) != 2 {
		t.Fatalf("recursive: %+v", items)
	}
	var b connectors.SourceItem
	for _, it := range items {
		if strings.HasSuffix(it.Path, "b ü.txt") {
			b = it
		}
	}
	if b.Path != "/notes/deep dir/b ü.txt" || b.Revision == "" {
		t.Fatalf("unicode path item: %+v", b)
	}
	if items := scan(map[string]any{"rootPath": "/notes", "recursive": false}); len(items) != 1 {
		t.Fatalf("flat: %+v", items)
	}
	if items := scan(map[string]any{"rootPath": "/"}); len(items) != 3 {
		t.Fatalf("root: %+v", items)
	}

	content, err := c.OpenContent(ctx, connectors.ContentRequest{Credential: cred, Item: b})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(content.Body)
	content.Body.Close()
	if string(data) != "B" {
		t.Fatalf("content = %q", data)
	}

	res, err := c.Browse(ctx, cred, connectors.BrowseRequest{ParentID: "/notes"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range res.Items {
		if it.Name == "deep dir" && it.Selectable {
			found = true
		}
	}
	if !found {
		t.Fatalf("browse: %+v", res.Items)
	}
}

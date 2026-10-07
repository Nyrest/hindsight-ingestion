package onedrive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestDeltaScan(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/me/drive/root":
			io.WriteString(w, `{"id":"ROOT","parentReference":{"driveId":"D1"}}`)
		case r.URL.Path == "/me/drive/root/delta" && r.URL.Query().Get("token") == "":
			// Real delta responses omit parentReference.path.
			io.WriteString(w, `{"value":[
				{"id":"ROOT","root":{},"folder":{}},
				{"id":"FOLDER","name":"Docs","folder":{},"parentReference":{"driveId":"D1","id":"ROOT"}},
				{"id":"SUB","name":"Sub","folder":{},"parentReference":{"driveId":"D1","id":"FOLDER"}},
				{"id":"i1","name":"a.md","size":3,"cTag":"c1","file":{"mimeType":"text/markdown"},"parentReference":{"driveId":"D1","id":"FOLDER"}},
				{"id":"i2","name":"x.md","size":3,"cTag":"c2","file":{"mimeType":"text/markdown"},"parentReference":{"driveId":"D1","id":"ROOT"}}
			],"@odata.nextLink":"`+srvURL+`/me/drive/root/delta?token=p2"}`)
		case r.URL.Path == "/me/drive/root/delta" && r.URL.Query().Get("token") == "p2":
			io.WriteString(w, `{"value":[
				{"id":"i3","name":"b.pdf","size":5,"cTag":"c3","file":{"mimeType":"application/pdf"},"parentReference":{"driveId":"D1","id":"SUB"}},
				{"id":"i1","name":"a.md","size":4,"cTag":"c1b","file":{"mimeType":"text/markdown"},"parentReference":{"driveId":"D1","id":"FOLDER"}}
			],"@odata.deltaLink":"`+srvURL+`/me/drive/root/delta?token=d1"}`)
		case r.URL.Path == "/me/drive/root/delta" && r.URL.Query().Get("token") == "d1":
			io.WriteString(w, `{"value":[
				{"id":"i3","name":"b.pdf","cTag":"c4","file":{"mimeType":"application/pdf"},"parentReference":{"driveId":"D1","id":"OTHER"}},
				{"id":"i1","name":"a.md","cTag":"c5","file":{"mimeType":"text/markdown"},"parentReference":{"driveId":"D1","id":"FOLDER"}},
				{"id":"i1","deleted":{},"parentReference":{"driveId":"D1"}}
			],"@odata.deltaLink":"`+srvURL+`/me/drive/root/delta?token=d2"}`)
		case r.URL.Path == "/drives/D1/items/i1/content":
			io.WriteString(w, "abc")
		default:
			t.Logf("unhandled %s?%s", r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	old := graphBase
	graphBase = srv.URL
	defer func() { graphBase = old }()

	cred := connectors.Credential{OAuth: &connectors.OAuthToken{AccessToken: "at"}}
	cfg := map[string]any{"folderId": "FOLDER", "recursive": true}
	c := &Connector{}
	var items []connectors.SourceItem
	emit := func(it connectors.SourceItem) error { items = append(items, it); return nil }
	res, err := c.Scan(context.Background(), connectors.ScanRequest{Credential: cred, Config: cfg, Full: true, Emit: emit, Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	// i1 appears twice; the last occurrence (cTag c1b) wins and is emitted once.
	if len(items) != 2 || items[0].ID != "D1!i1" || items[0].Path != "/a.md" || items[0].Revision != "c1b" ||
		items[1].Path != "/Sub/b.pdf" || !res.Complete {
		t.Fatalf("baseline: %+v", items)
	}
	var cur cursorState
	json.Unmarshal(res.Cursor, &cur)
	if !strings.Contains(cur.DeltaLink, "token=d1") {
		t.Fatalf("cursor = %s", res.Cursor)
	}

	items = nil
	res, err = c.Scan(context.Background(), connectors.ScanRequest{Credential: cred, Config: cfg, Cursor: res.Cursor, Emit: emit, Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	// Moved out of scope + modified-then-deleted (last occurrence wins) →
	// both reported deleted.
	if len(items) != 2 || !items[0].Deleted || !items[1].Deleted || res.Complete {
		t.Fatalf("delta: %+v", items)
	}

	content, err := c.OpenContent(context.Background(), connectors.ContentRequest{Credential: cred, Item: connectors.SourceItem{ID: "D1!i1", Name: "a.md"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(content.Body)
	content.Body.Close()
	if string(b) != "abc" {
		t.Fatalf("content = %q", b)
	}
}

package googledrive

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

func TestScanBaselineThenChanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at" {
			w.WriteHeader(401)
			return
		}
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/changes/startPageToken":
			io.WriteString(w, `{"startPageToken":"100"}`)
		case r.URL.Path == "/files" && strings.Contains(q.Get("q"), "'root' in parents"):
			io.WriteString(w, `{"files":[
				{"id":"f1","name":"a.pdf","mimeType":"application/pdf","size":"10","md5Checksum":"m1","modifiedTime":"2026-01-01T00:00:00Z"},
				{"id":"d1","name":"Sub","mimeType":"application/vnd.google-apps.folder"},
				{"id":"g1","name":"Notes","mimeType":"application/vnd.google-apps.document","version":"7","modifiedTime":"2026-01-01T00:00:00Z"}]}`)
		case r.URL.Path == "/files" && strings.Contains(q.Get("q"), "'d1' in parents"):
			io.WriteString(w, `{"files":[{"id":"f2","name":"b.txt","mimeType":"text/plain","size":"3","md5Checksum":"m2","modifiedTime":"2026-01-01T00:00:00Z"}]}`)
		case r.URL.Path == "/files/root":
			io.WriteString(w, `{"id":"ROOTID"}`)
		case r.URL.Path == "/files/d1":
			io.WriteString(w, `{"name":"Sub","parents":["ROOTID"]}`)
		case r.URL.Path == "/changes":
			if q.Get("pageToken") != "100" {
				t.Errorf("pageToken = %s", q.Get("pageToken"))
			}
			io.WriteString(w, `{"newStartPageToken":"105","changes":[
				{"fileId":"f1","removed":true},
				{"fileId":"f2","file":{"id":"f2","name":"b.txt","mimeType":"text/plain","md5Checksum":"m3","parents":["d1"],"modifiedTime":"2026-02-01T00:00:00Z"}},
				{"fileId":"f3","file":{"id":"f3","name":"t.txt","mimeType":"text/plain","trashed":true}}]}`)
		case strings.HasPrefix(r.URL.Path, "/files/g1/export"):
			if q.Get("mimeType") != "text/markdown" {
				t.Errorf("export mime %s", q.Get("mimeType"))
			}
			io.WriteString(w, "# Notes")
		default:
			t.Logf("unhandled %s %s", r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL
	defer func() { apiBase = old }()

	cred := connectors.Credential{OAuth: &connectors.OAuthToken{AccessToken: "at"}}
	c := &Connector{}
	var items []connectors.SourceItem
	emit := func(it connectors.SourceItem) error { items = append(items, it); return nil }
	res, err := c.Scan(context.Background(), connectors.ScanRequest{Credential: cred, Config: map[string]any{}, Full: true, Emit: emit, Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || !res.Complete {
		t.Fatalf("baseline items: %+v", items)
	}
	var cur cursorState
	json.Unmarshal(res.Cursor, &cur)
	if cur.PageToken != "100" {
		t.Fatalf("baseline must commit the start token, got %q", cur.PageToken)
	}
	var doc connectors.SourceItem
	for _, it := range items {
		if it.Metadata["google_drive_id"] != "ROOTID" {
			t.Errorf("My Drive identity = %q", it.Metadata["google_drive_id"])
		}
		if it.ID == "g1" {
			doc = it
		}
		if it.ID == "f2" && it.Path != "/Sub/b.txt" {
			t.Errorf("path = %s", it.Path)
		}
	}
	if doc.Name != "Notes.md" || doc.MIMEType != "text/markdown" {
		t.Fatalf("google doc item: %+v", doc)
	}
	content, err := c.OpenContent(context.Background(), connectors.ContentRequest{Credential: cred, Item: doc})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(content.Body)
	content.Body.Close()
	if string(b) != "# Notes" {
		t.Fatalf("export = %q", b)
	}

	items = nil
	res, err = c.Scan(context.Background(), connectors.ScanRequest{Credential: cred, Config: map[string]any{}, Cursor: res.Cursor, Emit: emit, Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(res.Cursor, &cur)
	if cur.PageToken != "105" || res.Complete {
		t.Fatalf("changes cursor %q complete=%v", cur.PageToken, res.Complete)
	}
	if len(items) != 3 || !items[0].Deleted || items[1].Path != "/Sub/b.txt" || !items[2].Deleted {
		t.Fatalf("change items: %+v", items)
	}
}

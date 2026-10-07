package notion

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

func TestPropertiesText(t *testing.T) {
	raw := json.RawMessage(`{
		"Name": {"type":"title","title":[{"plain_text":"Hello"}]},
		"Tags": {"type":"multi_select","multi_select":[{"name":"a"},{"name":"b"}]},
		"Done": {"type":"checkbox","checkbox":true},
		"Due": {"type":"date","date":{"start":"2026-01-01","end":null}},
		"Score": {"type":"number","number":4.5},
		"Empty": {"type":"rich_text","rich_text":[]}
	}`)
	got := PropertiesText(raw)
	for _, want := range []string{"- Name: Hello", "- Tags: a, b", "- Done: Yes", "- Due: 2026-01-01", "- Score: 4.5"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Empty") {
		t.Error("empty property should be omitted")
	}
}

// TestScanAndContent exercises pagination, high-water cursor and markdown
// retrieval against a fake Notion API.
func TestScanAndContent(t *testing.T) {
	var queries []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Notion-Version") == "" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/data_sources/ds1":
			io.WriteString(w, `{"title":[{"plain_text":"Wiki"}]}`)
		case r.URL.Path == "/data_sources/ds1/query":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			queries = append(queries, body)
			if body["start_cursor"] == nil {
				io.WriteString(w, `{"results":[{"object":"page","id":"p1","last_edited_time":"2026-01-01T00:00:00.000Z","properties":{"Name":{"type":"title","title":[{"plain_text":"One"}]}}}],"has_more":true,"next_cursor":"c2"}`)
			} else {
				io.WriteString(w, `{"results":[{"object":"page","id":"p2","last_edited_time":"2026-02-01T00:00:00.000Z","in_trash":true,"properties":{}}],"has_more":false}`)
			}
		case r.URL.Path == "/pages/p1/markdown":
			io.WriteString(w, `{"markdown":"# One\nbody","truncated":false,"unknown_block_ids":[]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL
	defer func() { apiBase = old }()

	cred := connectors.Credential{Secrets: map[string]string{"token": "secret"}}
	var items []connectors.SourceItem
	c := &Connector{}
	res, err := c.Scan(context.Background(), connectors.ScanRequest{
		Credential: cred, Config: map[string]any{"dataSourceId": "ds1"}, Full: true,
		Emit: func(it connectors.SourceItem) error { items = append(items, it); return nil },
		Log:  func(string, string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "One" || !items[1].Deleted || !res.Complete {
		t.Fatalf("items=%+v complete=%v", items, res.Complete)
	}
	if !strings.Contains(string(res.Cursor), "2026-02-01") {
		t.Fatalf("cursor = %s", res.Cursor)
	}

	// Incremental scan sends a last_edited_time filter.
	_, err = c.Scan(context.Background(), connectors.ScanRequest{
		Credential: cred, Config: map[string]any{"dataSourceId": "ds1"}, Cursor: res.Cursor,
		Emit: func(connectors.SourceItem) error { return nil }, Log: func(string, string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries[len(queries)-2]["filter"] == nil {
		t.Fatal("incremental query missing filter")
	}

	content, err := c.OpenContent(context.Background(), connectors.ContentRequest{Credential: cred, Item: items[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content.Text, "# One") || !strings.Contains(content.Text, "Notion page properties:") {
		t.Fatalf("content = %q", content.Text)
	}
}

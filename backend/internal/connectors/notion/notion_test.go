package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestInlineImagesRefreshAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		name            string
		enabled, images bool
	}{
		{"off", false, true}, {"excluded", true, false}, {"on", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, downloads := 0, 0
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/pages/p1/markdown" {
					if r.Header.Get("Authorization") != "Bearer secret" {
						t.Error("missing source auth")
					}
					reads++
					image := "/expired?X-Amz-Signature=secret"
					if reads > 1 {
						image = "/fresh"
					}
					json.NewEncoder(w).Encode(map[string]any{"markdown": "before ![caption](" + srv.URL + image + ") after"})
					return
				}
				downloads++
				if r.Header.Get("Authorization") != "" {
					t.Error("source auth leaked to image")
				}
				if r.URL.Path == "/expired" {
					w.WriteHeader(403)
					return
				}
				w.Write(append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, bytes.Repeat([]byte{0}, 24)...))
			}))
			defer srv.Close()
			old := apiBase
			apiBase = srv.URL
			defer func() { apiBase = old }()
			op, _ := json.Marshal(opaque{Title: "Title", Source: "Wiki", Properties: json.RawMessage(`{"Name":{"type":"title","title":[{"plain_text":"Title"}]}}`)})
			content, err := (&Connector{}).OpenContent(t.Context(), connectors.ContentRequest{
				Credential: connectors.Credential{Secrets: map[string]string{"token": "secret"}},
				Item: connectors.SourceItem{
					ID: "p1", Opaque: op, ModifiedAt: time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC),
					Attributes: map[string]any{"createdAt": time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)},
				},
				InlineMultimodalEnabled: tc.enabled, FilePolicy: connectors.FilePolicy{Images: tc.images}, MaxFileSize: 1000,
			})
			if err != nil || len(content.Warnings) > 0 {
				t.Fatalf("content: %+v %v", content, err)
			}
			if !content.TimestampUnset || !content.Timestamp.IsZero() {
				t.Fatalf("Notion content must not use page timestamps: %+v", content)
			}
			if tc.enabled && tc.images {
				if reads != 2 || downloads != 2 {
					t.Fatalf("refresh reads=%d downloads=%d", reads, downloads)
				}
				if len(content.Blocks) != 5 || content.Blocks[2].Type != "image" || !strings.Contains(content.Blocks[4].Text, "- Name: Title") {
					t.Fatalf("order/properties: %+v", content.Blocks)
				}
			} else if reads != 1 || downloads != 0 || len(content.Blocks) != 0 {
				t.Fatal("policy did not gate downloads")
			}
		})
	}
}

func TestBrowseSearch(t *testing.T) {
	for _, query := range []string{"", " \t ", "  项目 '计划'  "} {
		t.Run(query, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/search" || r.Method != http.MethodPost {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if wanted := strings.TrimSpace(query); wanted == "" {
					if _, exists := body["query"]; exists {
						t.Errorf("empty search must omit query: %v", body["query"])
					}
				} else if body["query"] != wanted {
					t.Errorf("search query: %v, want %q", body["query"], wanted)
				}
				if filter, ok := body["filter"].(map[string]any); !ok || filter["value"] != "data_source" {
					t.Errorf("search filter: %v", body["filter"])
				}
				requests++
				if requests == 1 {
					if body["start_cursor"] != nil {
						t.Error("first request has a cursor")
					}
					io.WriteString(w, `{"results":[{"id":"z","title":[{"plain_text":"Zulu"}]}],"has_more":true,"next_cursor":"next"}`)
				} else {
					if body["start_cursor"] != "next" {
						t.Errorf("pagination cursor: %v", body["start_cursor"])
					}
					io.WriteString(w, `{"results":[{"id":"a","title":[{"plain_text":"Alpha"}]}],"has_more":false}`)
				}
			}))
			defer srv.Close()
			old := apiBase
			apiBase = srv.URL
			defer func() { apiBase = old }()
			listing, err := (&Connector{}).Browse(t.Context(), connectors.Credential{}, connectors.BrowseRequest{Query: query})
			if err != nil || !listing.SupportsSearch || requests != 2 || len(listing.Items) != 2 {
				t.Fatalf("browse: %+v, requests=%d, err=%v", listing, requests, err)
			}
			if listing.Items[0].Name != "Alpha" || !listing.Items[0].Selectable || listing.Items[0].Kind != connectors.KindDataSource {
				t.Fatalf("selection and sort: %+v", listing.Items)
			}
		})
	}
}

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

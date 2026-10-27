package notion

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func testPage(id string) map[string]any {
	return map[string]any{"object": "page", "id": id, "url": "https://notion.so/" + id,
		"last_edited_time": "2026-10-01T00:00:00Z", "created_time": "2026-09-01T00:00:00Z",
		"properties": map[string]any{"title": map[string]any{"type": "title", "title": []map[string]string{{"plain_text": id}}}}}
}

func TestHierarchyConfig(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   map[string]any
		field string
	}{
		{"legacy", map[string]any{"dataSourceId": "ds"}, ""},
		{"pages", map[string]any{"mode": "pages", "pageIds": []any{"a", "b"}, "maxDepth": nil}, ""},
		{"zero", map[string]any{"mode": "pages", "pageIds": []string{"a"}, "maxDepth": 0}, ""},
		{"no database", map[string]any{}, "dataSourceId"},
		{"empty pages", map[string]any{"mode": "pages", "pageIds": []any{" "}}, "pageIds"},
		{"wrong list", map[string]any{"mode": "pages", "pageIds": []any{1}}, "pageIds"},
		{"mode", map[string]any{"mode": "all"}, "mode"},
		{"negative", map[string]any{"dataSourceId": "ds", "maxDepth": -1}, "maxDepth"},
		{"fraction", map[string]any{"dataSourceId": "ds", "maxDepth": 1.5}, "maxDepth"},
		{"overflow", map[string]any{"dataSourceId": "ds", "maxDepth": 2147483648.0}, "maxDepth"},
		{"boolean", map[string]any{"dataSourceId": "ds", "maxDepth": true}, "maxDepth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (&Connector{}).ValidateConfig(tc.cfg, connectors.Filter{})
			if tc.field == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var fe *connectors.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field {
				t.Fatalf("validation = %v, want field %s", err, tc.field)
			}
		})
	}
	cfg, err := parseConfig(map[string]any{"dataSourceId": "ds"})
	if err != nil || cfg.mode != "database" || cfg.includeSubpages || cfg.maxDepth != 3 {
		t.Fatalf("legacy defaults: %+v %v", cfg, err)
	}
	ids, err := pageIDs([]any{" ABCD-EF ", "abcdef", "other"})
	if err != nil || !reflect.DeepEqual(ids, []string{"ABCD-EF", "other"}) {
		t.Fatal(ids, err)
	}
}

func TestHierarchyDepthAndNestedBlocks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		depth    any
		explicit bool
		want     int
	}{
		{"default", nil, false, 4}, {"unlimited", nil, true, 5}, {"zero", 0, true, 1}, {"one", 1, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				if parts[0] == "pages" {
					json.NewEncoder(w).Encode(testPage(parts[1]))
					return
				}
				if parts[0] != "blocks" {
					t.Errorf("unexpected path: %s", r.URL)
					w.WriteHeader(400)
					return
				}
				var blocks []map[string]any
				next := ""
				switch parts[1] {
				case "p0":
					if r.URL.Query().Get("start_cursor") == "" {
						blocks = []map[string]any{{"id": "toggle", "type": "toggle", "has_children": true}, {"id": "database", "type": "child_database", "has_children": true}, {"id": "link", "type": "link_to_page", "has_children": true}}
						next = "second"
					} else {
						blocks = []map[string]any{{"id": "archived", "type": "child_page", "archived": true}}
					}
				case "toggle":
					blocks = []map[string]any{{"id": "column", "type": "column", "has_children": true}}
				case "column":
					blocks = []map[string]any{{"id": "p1", "type": "child_page"}}
				case "p1", "p2", "p3":
					var n int
					fmt.Sscanf(parts[1], "p%d", &n)
					blocks = []map[string]any{{"id": fmt.Sprintf("p%d", n+1), "type": "child_page"}}
				case "p4":
					blocks = []map[string]any{{"id": "p0", "type": "child_page"}}
				default:
					t.Errorf("traversed excluded subtree: %s", r.URL)
					w.WriteHeader(400)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"results": blocks, "has_more": next != "", "next_cursor": next})
			}))
			defer server.Close()
			cfg := map[string]any{"mode": "pages", "pageIds": []string{"p0"}, "includeSubpages": true}
			if tc.explicit {
				cfg["maxDepth"] = tc.depth
			}
			var items []connectors.SourceItem
			res, err := (&Connector{}).Scan(t.Context(), connectors.ScanRequest{Credential: connectors.Credential{Config: map[string]any{"baseUrl": server.URL}}, Config: cfg,
				Cursor: json.RawMessage(`{"highWater":"2026-10-25T00:00:00Z"}`), Emit: func(it connectors.SourceItem) error { items = append(items, it); return nil }})
			if err != nil || !res.Complete || len(items) != tc.want {
				t.Fatalf("scan complete=%v items=%+v err=%v", res.Complete, items, err)
			}
			for i, it := range items {
				if it.ID != fmt.Sprintf("p%d", i) || it.Metadata["notion_root_page_id"] != "p0" || it.Metadata["notion_data_source_id"] != "" || !reflect.DeepEqual(it.Tags, []string{"notion_root_page_id:p0"}) {
					t.Fatalf("identity/scope: %+v", it)
				}
			}
		})
	}
}

func TestHierarchyOverlappingRoots(t *testing.T) {
	for _, roots := range [][]string{{"a", "b"}, {"b", "a"}} {
		t.Run(strings.Join(roots, ","), func(t *testing.T) {
			t.Parallel()
			reads := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads[r.URL.Path]++
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				if parts[0] == "pages" {
					json.NewEncoder(w).Encode(testPage(parts[1]))
					return
				}
				child := "b"
				if parts[1] == "b" {
					child = "c"
				}
				json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": child, "type": "child_page"}}})
			}))
			defer server.Close()
			items := map[string]connectors.SourceItem{}
			res, err := (&Connector{}).Scan(t.Context(), connectors.ScanRequest{Credential: connectors.Credential{Config: map[string]any{"baseUrl": server.URL}}, Config: map[string]any{"mode": "pages", "pageIds": roots, "includeSubpages": true, "maxDepth": 1},
				Emit: func(it connectors.SourceItem) error {
					if _, ok := items[it.ID]; ok {
						t.Errorf("duplicate: %s", it.ID)
					}
					items[it.ID] = it
					return nil
				}})
			if err != nil || !res.Complete || len(items) != 3 || items["c"].Metadata["notion_root_page_id"] != "b" || reads["/pages/b"] != 1 {
				t.Fatal(res, items, reads, err)
			}
		})
	}
}

func TestDatabaseHierarchyIgnoresHighWater(t *testing.T) {
	queries := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data_sources/ds":
			fmt.Fprint(w, `{"title":[{"plain_text":"Wiki"}]}`)
		case "/data_sources/ds/query":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["filter"] != nil {
				t.Error("hierarchy used a high-water filter")
			}
			queries++
			json.NewEncoder(w).Encode(map[string]any{"results": []any{testPage("root")}})
		case "/blocks/root/children":
			fmt.Fprint(w, `{"results":[{"id":"child","type":"child_page"}]}`)
		case "/pages/child":
			json.NewEncoder(w).Encode(testPage("child"))
		default:
			t.Errorf("unexpected path: %s", r.URL)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	var items []connectors.SourceItem
	res, err := (&Connector{}).Scan(t.Context(), connectors.ScanRequest{Credential: connectors.Credential{Config: map[string]any{"baseUrl": server.URL}}, Config: map[string]any{"dataSourceId": "ds", "includeSubpages": true, "maxDepth": 1}, Cursor: json.RawMessage(`{"highWater":"2026-10-25T00:00:00Z"}`),
		Emit: func(it connectors.SourceItem) error { items = append(items, it); return nil }})
	if err != nil || !res.Complete || queries != 1 || len(items) != 2 || items[1].Path != "Wiki / root / child" || items[1].Tags[0] != "notion_data_source_id:ds" {
		t.Fatal(res, items, err)
	}
}

func TestHierarchyFailuresNeverComplete(t *testing.T) {
	for _, failure := range []string{"root403", "child404", "blocks403", "pagination", "cycle cursor", "metadata", "emit"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/pages/root":
					if failure == "root403" {
						w.WriteHeader(403)
						return
					}
					p := testPage("root")
					if failure == "metadata" {
						delete(p, "last_edited_time")
					}
					json.NewEncoder(w).Encode(p)
				case "/pages/child":
					w.WriteHeader(404)
				case "/blocks/root/children":
					switch failure {
					case "blocks403":
						w.WriteHeader(403)
					case "pagination":
						fmt.Fprint(w, `{"results":[],"has_more":true}`)
					case "cycle cursor":
						fmt.Fprint(w, `{"results":[],"has_more":true,"next_cursor":"same"}`)
					default:
						fmt.Fprint(w, `{"results":[{"id":"child","type":"child_page"}]}`)
					}
				default:
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			res, err := (&Connector{}).Scan(t.Context(), connectors.ScanRequest{Credential: connectors.Credential{Config: map[string]any{"baseUrl": server.URL}}, Config: map[string]any{"mode": "pages", "pageIds": []string{"root"}, "includeSubpages": true}, Emit: func(connectors.SourceItem) error {
				if failure == "emit" {
					return errors.New("stopped")
				}
				return nil
			}})
			if err == nil || res.Complete || len(res.Cursor) > 0 {
				t.Fatal("failed inventory was complete", res, err)
			}
		})
	}
}

func TestBrowsePages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Filter struct {
				Value string `json:"value"`
			} `json:"filter"`
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Filter.Value != "page" || body.Query != "notes" {
			t.Errorf("search: %+v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []any{testPage("notes")}})
	}))
	defer server.Close()
	res, err := (&Connector{}).Browse(t.Context(), connectors.Credential{Config: map[string]any{"baseUrl": server.URL}}, connectors.BrowseRequest{Kind: connectors.KindPage, Query: " notes "})
	if err != nil || len(res.Items) != 1 || res.Items[0].Kind != connectors.KindPage || res.Items[0].Name != "notes" || !res.Items[0].Selectable {
		t.Fatal(res, err)
	}
}

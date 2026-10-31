package notion

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestBrowseIsBounded(t *testing.T) {
	for _, kind := range []string{connectors.KindPage, connectors.KindDataSource} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["page_size"] != float64(50) {
					t.Errorf("default batch size: %v, want 50", body["page_size"])
				}
				if requests == 1 {
					fmt.Fprint(w, `{"results":[],"has_more":true,"next_cursor":"next"}`)
				} else {
					fmt.Fprint(w, `{"results":[],"has_more":false}`)
				}
			}))
			defer srv.Close()
			res, err := (&Connector{}).Browse(t.Context(), connectors.Credential{Config: map[string]any{"baseUrl": srv.URL}}, connectors.BrowseRequest{Kind: kind})
			if err != nil || requests != 1 || res.NextCursor != "next" || res.Items == nil {
				t.Fatalf("browse must return one batch, including empty batches: %+v, requests=%d, err=%v", res, requests, err)
			}
		})
	}
}

func TestBrowseEmptyPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[],"has_more":false}`)
	}))
	defer srv.Close()
	res, err := (&Connector{}).Browse(t.Context(), connectors.Credential{Config: map[string]any{"baseUrl": srv.URL}}, connectors.BrowseRequest{Kind: connectors.KindPage})
	if err != nil || res.Items == nil || len(res.Items) != 0 {
		t.Fatal("empty search must return an array for the resource browser", res, err)
	}
}

func TestBrowseLimitsAndCursor(t *testing.T) {
	for _, tc := range []struct{ limit, want int }{{-1, 50}, {1, 1}, {25, 25}, {100, 100}, {1000, 100}} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["page_size"] != float64(tc.want) || body["start_cursor"] != "continue" || body["query"] != "notes" {
					t.Errorf("bounded continuation request: %v", body)
				}
				// Preserve provider order and discard archived pages, even at a batch boundary.
				fmt.Fprint(w, `{"results":[{"id":"z"},{"id":"hidden","archived":true},{"id":"a"}],"has_more":false,"next_cursor":"ignored"}`)
			}))
			defer srv.Close()
			res, err := (&Connector{}).Browse(t.Context(), connectors.Credential{Config: map[string]any{"baseUrl": srv.URL}}, connectors.BrowseRequest{Kind: connectors.KindPage, Query: " notes ", Cursor: "continue", Limit: tc.limit})
			if err != nil || requests != 1 || res.NextCursor != "" || len(res.Items) != 2 || res.Items[0].ID != "z" || res.Items[1].ID != "a" {
				t.Fatalf("continuation: %+v, requests=%d, err=%v", res, requests, err)
			}
		})
	}
}

func TestBrowseInvalidCursor(t *testing.T) {
	for _, next := range []string{"", "same"} {
		t.Run(next, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "has_more": true, "next_cursor": next})
			}))
			defer srv.Close()
			_, err := (&Connector{}).Browse(t.Context(), connectors.Credential{Config: map[string]any{"baseUrl": srv.URL}}, connectors.BrowseRequest{Kind: connectors.KindPage, Cursor: "same"})
			if err == nil {
				t.Fatal("invalid continuation cursor must fail")
			}
		})
	}
}

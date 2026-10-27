package notion

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

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

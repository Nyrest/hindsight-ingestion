package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

func TestExplicitAcceptedStatuses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(304) }))
	defer server.Close()
	client := New(nil, nil, proxy.Default)
	if _, err := client.Do(t.Context(), Request{Method: "GET", URL: server.URL}); !IsStatus(err, 304) {
		t.Fatalf("default client accepted 304: %v", err)
	}
	response, err := client.Do(t.Context(), Request{Method: "GET", URL: server.URL, AcceptStatuses: []int{304}})
	if err != nil || response.StatusCode != 304 {
		t.Fatalf("explicit 304 not accepted: %v", err)
	}
	response.Body.Close()
}

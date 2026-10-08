package httpx

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

func TestProxyErrorsDoNotExposeAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(407)
		io.WriteString(w, "proxy-secret "+r.Header.Get("Proxy-Authorization"))
	}))
	defer server.Close()
	c := New(nil, nil, proxy.Config{Type: "http", Address: strings.TrimPrefix(server.URL, "http://"), Username: "user", Password: "proxy-secret"})
	for _, stream := range []bool{false, true} {
		var err error
		if stream {
			_, err = c.DoStream(t.Context(), "GET", "http://remote.invalid", nil, nil)
		} else {
			_, err = c.Do(t.Context(), Request{Method: "GET", URL: "http://remote.invalid"})
		}
		if err == nil || !IsStatus(err, 407) {
			t.Fatal("wrong status", err)
		}
		if strings.Contains(err.Error(), "proxy-secret") || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte("user:proxy-secret"))) {
			t.Fatal("proxy auth exposed")
		}
	}
}

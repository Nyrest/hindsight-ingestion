package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

func TestRedirectsProtectCredentialsAcrossOriginsAndReturnHops(t *testing.T) {
	var sourceURL string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		for _, header := range []string{"X-Api-Key", "CF-Access-Client-Secret", "Authorization", "Cookie"} {
			if req.Header.Get(header) != "" {
				t.Errorf("cross-origin redirect exposed %s", header)
			}
		}
		http.Redirect(w, req, sourceURL+"/return", http.StatusFound)
	}))
	defer foreign.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/start":
			http.Redirect(w, req, "/same-origin", http.StatusFound)
		case "/same-origin":
			if req.Header.Get("X-Api-Key") != "api-secret" || req.Header.Get("Authorization") != "Bearer auth-secret" {
				t.Error("same-origin redirect lost credentials")
			}
			http.Redirect(w, req, foreign.URL, http.StatusFound)
		case "/return":
			if req.Header.Get("X-Api-Key") != "" || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
				t.Error("return hop restored credentials")
			}
		}
	}))
	defer source.Close()
	sourceURL = source.URL
	client := New(map[string]string{"X-Api-Key": "api-secret", "CF-Access-Client-Secret": "cf-secret"}, map[string]string{"Authorization": "Bearer auth-secret"}, proxy.Default)
	response, err := client.Do(t.Context(), Request{Method: "GET", URL: source.URL + "/start", Header: http.Header{"Cookie": {"session=secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestRedirectRejectsDowngradeAndComparesEffectivePorts(t *testing.T) {
	client := New(nil, nil, proxy.Default)
	for _, address := range []string{"http://source.test", "ftp://source.test"} {
		initial, _ := http.NewRequest("GET", "https://source.test", nil)
		redirect, _ := http.NewRequest("GET", address, nil)
		if client.checkRedirect(redirect, []*http.Request{initial}) == nil {
			t.Fatalf("accepted redirect to %s", address)
		}
	}
	initial, _ := url.Parse("https://SOURCE.test/path")
	explicit, _ := url.Parse("https://source.test:443/other")
	if origin(initial) != origin(explicit) {
		t.Fatal("default ports or hostname case changed origin")
	}
}

type failingTransport struct{ err error }

func (transport failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, transport.err
}

func TestNetworkErrorsRedactSignedURLsAndPreserveCause(t *testing.T) {
	cause := errors.New("connection failed: dynamic-token-secret")
	client := New(nil, nil, proxy.Default)
	client.Token = func(context.Context) (string, error) { return "dynamic-token-secret", nil }
	client.MaxRetries = 0
	client.HTTP.Transport = failingTransport{cause}
	for _, stream := range []bool{false, true} {
		address := "https://user:password@source.test/path-secret?signature=query-secret#fragment-secret"
		var err error
		if stream {
			_, err = client.DoStream(t.Context(), "GET", address, nil, nil)
		} else {
			_, err = client.Do(t.Context(), Request{Method: "GET", URL: address})
		}
		if !errors.Is(err, cause) {
			t.Fatalf("network error lost cause: %v", err)
		}
		assertNoURLSecrets(t, err.Error())
	}
}

func TestStatusErrorsRedactEchoedHeadersAndURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "https://source.test/path-secret?signature=query-secret "+req.Header.Get("X-Api-Key")+" "+strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
	}))
	defer server.Close()
	client := New(map[string]string{"X-Api-Key": "api-secret"}, nil, proxy.Default)
	client.Token = func(context.Context) (string, error) { return "dynamic-token-secret", nil }
	_, err := client.Do(t.Context(), Request{Method: "GET", URL: server.URL + "/path-secret?signature=query-secret"})
	if !IsStatus(err, http.StatusForbidden) {
		t.Fatalf("lost status: %v", err)
	}
	assertNoURLSecrets(t, err.Error())
	var status *HTTPError
	if !errors.As(err, &status) {
		t.Fatal("lost HTTPError type")
	}
	assertNoURLSecrets(t, status.URL+status.Body)
}

func TestResponseBodyReadsRespectClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-req.Context().Done()
	}))
	defer server.Close()
	client := New(nil, nil, proxy.Default)
	if client.HTTP.Timeout != 10*time.Minute {
		t.Fatal("shared client has no bounded request deadline")
	}
	client.HTTP.Timeout = 100 * time.Millisecond
	var response map[string]any
	if err := client.JSON(t.Context(), "GET", server.URL, nil, &response); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled response body did not time out: %v", err)
	}
}

func assertNoURLSecrets(t *testing.T, message string) {
	t.Helper()
	for _, secret := range []string{"password", "path-secret", "query-secret", "fragment-secret", "api-secret", "dynamic-token-secret"} {
		if strings.Contains(message, secret) {
			t.Errorf("diagnostic exposed %s", secret)
		}
	}
}

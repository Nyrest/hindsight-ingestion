package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := BasicAuth("admin", "s3cret", func(r *http.Request) bool { return r.URL.Path == "/api/health" })(ok)

	cases := []struct {
		name, path, user, pass string
		setAuth                bool
		want                   int
	}{
		{"no auth", "/", "", "", false, 401},
		{"api no auth", "/api/tasks", "", "", false, 401},
		{"wrong pass", "/", "admin", "nope", true, 401},
		{"wrong user", "/", "root", "s3cret", true, 401},
		{"ok", "/api/tasks", "admin", "s3cret", true, 204},
		{"public health", "/api/health", "", "", false, 204},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		if c.setAuth {
			req.SetBasicAuth(c.user, c.pass)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d want %d", c.name, rec.Code, c.want)
		}
		if c.want == 401 && rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: missing WWW-Authenticate", c.name)
		}
	}
}

func TestCSRFProtectionWrites(t *testing.T) {
	cases := []struct {
		name, header, origin, fetchSite string
		want                            int
	}{
		{"form submission", "", "https://evil.example", "cross-site", 403},
		{"no origin or header", "", "", "", 403},
		{"wrong header", "wrong", "https://app.example", "same-origin", 403},
		{"same origin", "1", "https://app.example", "same-origin", 204},
		{"cross site with header", "1", "https://evil.example", "cross-site", 403},
		{"same site with header", "1", "https://sub.app.example", "same-site", 403},
		{"origin fallback allowed", "1", "https://app.example", "", 204},
		{"origin fallback rejected", "1", "https://evil.example", "", 403},
		{"null origin rejected", "1", "null", "", 403},
		{"direct API client", "1", "", "", 204},
		{"same origin through dev proxy", "1", "http://localhost:5173", "same-origin", 204},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, c := range cases {
			t.Run(method+"/"+c.name, func(t *testing.T) {
				called := false
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					w.WriteHeader(http.StatusNoContent)
				})
				req := httptest.NewRequest(method, "https://app.example/api/tasks/task/full-reingest", nil)
				req.Header.Set("X-CSRF-Protection", c.header)
				req.Header.Set("Origin", c.origin)
				req.Header.Set("Sec-Fetch-Site", c.fetchSite)
				rec := httptest.NewRecorder()
				CSRFProtection(next).ServeHTTP(rec, req)
				if rec.Code != c.want || called != (c.want == http.StatusNoContent) {
					t.Fatalf("status=%d called=%v, want status=%d", rec.Code, called, c.want)
				}
				if c.want == http.StatusForbidden {
					var body struct{ Code string }
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "csrf" {
						t.Fatalf("invalid CSRF error envelope: %s (%v)", rec.Body.String(), err)
					}
				}
			})
		}
	}
}

func TestCSRFProtectionWithBasicAuth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := BasicAuth("admin", "s3cret", func(r *http.Request) bool {
		return r.URL.Path == "/api/oauth/callback" || r.URL.Path == "/api/health"
	})(CSRFProtection(next))
	cases := []struct {
		method, path string
		auth         bool
		want         int
	}{
		{http.MethodPost, "/api/tasks/task/run", false, 401},
		{http.MethodPost, "/api/tasks/task/run", true, 403},
		{http.MethodGet, "/api/oauth/callback", false, 204},
		{http.MethodGet, "/api/health", false, 204},
		{http.MethodGet, "/api/tasks", true, 204},
		{http.MethodHead, "/api/tasks", true, 204},
		{http.MethodOptions, "/api/tasks", true, 204},
	}
	for _, c := range cases {
		t.Run(c.method+c.path, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			if c.auth {
				req.SetBasicAuth("admin", "s3cret")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status=%d, want %d", rec.Code, c.want)
			}
		})
	}
}

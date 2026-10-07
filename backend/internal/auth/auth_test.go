package auth

import (
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

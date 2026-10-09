package api

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/config"
)

func TestOAuthRedirectURI(t *testing.T) {
	for _, tc := range []struct {
		name, publicURL, origin, requestURL, want string
		tls, invalid                              bool
	}{
		{name: "browser HTTPS through HTTP proxy", origin: "https://memory.example", requestURL: "http://internal:8080/start", want: "https://memory.example/api/oauth/callback"},
		{name: "browser nonstandard port", origin: "http://localhost:5173", requestURL: "http://localhost:8080/start", want: "http://localhost:5173/api/oauth/callback"},
		{name: "explicit public URL wins", publicURL: "https://fixed.example", origin: "https://other.example", requestURL: "http://internal/start", want: "https://fixed.example/api/oauth/callback"},
		{name: "direct HTTP without Origin", requestURL: "http://localhost:8080/start", want: "http://localhost:8080/api/oauth/callback"},
		{name: "direct HTTPS without Origin", requestURL: "https://memory.example/start", tls: true, want: "https://memory.example/api/oauth/callback"},
		{name: "opaque origin", origin: "null", requestURL: "http://internal/start", invalid: true},
		{name: "non HTTP origin", origin: "javascript:example", requestURL: "http://internal/start", invalid: true},
		{name: "origin with user info", origin: "https://user@memory.example", requestURL: "http://internal/start", invalid: true},
		{name: "origin with path", origin: "https://memory.example/path", requestURL: "http://internal/start", invalid: true},
		{name: "origin with query", origin: "https://memory.example?next=elsewhere", requestURL: "http://internal/start", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", tc.requestURL, nil)
			r.Header.Set("Origin", tc.origin)
			// Forwarded headers do not choose the browser's OAuth redirect URI.
			r.Header.Set("X-Forwarded-Host", "unrelated.example")
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			s := Server{Cfg: &config.Config{PublicURL: tc.publicURL}}
			got, err := s.oauthRedirectURI(r)
			if (err != nil) != tc.invalid || (!tc.invalid && got != tc.want) {
				t.Fatalf("got %q, %v; want %q, invalid=%v", got, err, tc.want, tc.invalid)
			}
		})
	}
}

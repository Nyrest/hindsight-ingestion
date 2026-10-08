// Package auth implements the mandatory-by-default HTTP Basic Auth.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// BasicAuth returns middleware enforcing Basic Auth with constant-time
// comparison. Credentials are hashed first so comparison time does not
// depend on their length.
func BasicAuth(username, password string, public func(*http.Request) bool) func(http.Handler) http.Handler {
	wantUser := sha256.Sum256([]byte(username))
	wantPass := sha256.Sum256([]byte(password))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public != nil && public(r) {
				next.ServeHTTP(w, r)
				return
			}
			u, p, ok := r.BasicAuth()
			gotUser := sha256.Sum256([]byte(u))
			gotPass := sha256.Sum256([]byte(p))
			userOK := subtle.ConstantTimeCompare(gotUser[:], wantUser[:])
			passOK := subtle.ConstantTimeCompare(gotPass[:], wantPass[:])
			if !ok || userOK&passOK != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="Hindsight Ingestion", charset="UTF-8"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRFProtection requires a non-safelisted header so browser forms cannot
// reuse cached Basic Auth credentials, even when origin headers are absent.
func CSRFProtection(next http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-CSRF-Protection") != "1" || protection.Check(r) != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"CSRF protection rejected the request","code":"csrf"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

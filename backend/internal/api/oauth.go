package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"

	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/oauth"
)

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	redirectURI, err := s.oauthRedirectURI(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	u, err := s.OAuth.Start(r.Context(), r.PathValue("id"), redirectURI)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			s.fail(w, err)
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	authorization, err := url.Parse(u)
	if err != nil {
		s.fail(w, err)
		return
	}
	callback, err := url.Parse(redirectURI)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "hi-oauth-state", Value: authorization.Query().Get("state"),
		Path: "/api/oauth/callback", HttpOnly: true, Secure: callback.Scheme == "https",
		SameSite: http.SameSiteLaxMode, MaxAge: int(oauth.StateLifetime.Seconds())})
	writeJSON(w, http.StatusOK, oauthStartDTO{u})
}

func (s *Server) oauthRedirectURI(r *http.Request) (string, error) {
	if configured := s.Cfg.OAuthRedirectURI(); configured != "" {
		return configured, nil
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		origin = (&url.URL{Scheme: scheme, Host: r.Host}).String()
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid OAuth request origin")
	}
	return origin + "/api/oauth/callback", nil
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	stateCookie, err := r.Cookie("hi-oauth-state")
	if err != nil || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(q.Get("state"))) != 1 {
		http.Redirect(w, r, "/credentials?oauth=error&message=Invalid+OAuth+session", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "hi-oauth-state", Path: "/api/oauth/callback", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if e := q.Get("error"); e != "" {
		msg := e
		if d := q.Get("error_description"); d != "" {
			msg += ": " + d
		}
		http.Redirect(w, r, "/credentials?oauth=error&message="+url.QueryEscape(msg), http.StatusFound)
		return
	}
	id, err := s.OAuth.Callback(r.Context(), q.Get("state"), q.Get("code"))
	if err != nil {
		s.Log.Warn("oauth callback failed", "error", err)
		http.Redirect(w, r, "/credentials?oauth=error&id="+url.QueryEscape(id)+"&message="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/credentials?oauth=success&id="+url.QueryEscape(id), http.StatusFound)
}

package oauth

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"

	"github.com/Nyrest/hindsight-ingestion/internal/config"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestOAuthUsesStoredRedirectAndRefreshNeedsNoPublicURL(t *testing.T) {
	for _, typ := range []string{"google_drive", "onedrive"} {
		for _, fixed := range []string{"", "https://fixed.example"} {
			t.Run(typ+"/"+fixed, func(t *testing.T) {
				connectors.RegisterCredentialType(connectors.CredentialType{Type: typ, OAuth: true, Fields: []connectors.FieldSpec{
					{Key: "clientId", Type: connectors.FieldString}, {Key: "clientSecret", Type: connectors.FieldPassword},
				}})
				redirect := "https://browser.example:8443/api/oauth/callback"
				if fixed != "" {
					redirect = fixed + "/api/oauth/callback"
				}
				var exchanged, refreshed atomic.Bool
				tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					w.Header().Set("Content-Type", "application/json")
					switch r.Form.Get("grant_type") {
					case "authorization_code":
						exchanged.Store(true)
						if r.Form.Get("redirect_uri") != redirect || r.Form.Get("code") != "test-code" {
							t.Error("code exchange changed the redirect URI")
						}
						io.WriteString(w, `{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600,"token_type":"Bearer"}`)
					case "refresh_token":
						refreshed.Store(true)
						if r.Form.Get("redirect_uri") != "" || r.Form.Get("refresh_token") != "rt-1" {
							t.Error("refresh depended on a redirect URI or lost the token")
						}
						io.WriteString(w, `{"access_token":"at-2","refresh_token":"rt-2","expires_in":3600,"token_type":"Bearer"}`)
					default:
						t.Error("unexpected token grant")
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				defer tokenServer.Close()
				original := providers[typ]
				providers[typ] = provider{endpoint: func(string) oauth2.Endpoint {
					return oauth2.Endpoint{AuthURL: tokenServer.URL + "/authorize", TokenURL: tokenServer.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}
				}, scopes: original.scopes}
				defer func() { providers[typ] = original }()

				log := slog.New(slog.NewTextHandler(io.Discard, nil))
				db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "oauth.db"), log)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { sqlDB, _ := db.DB(); sqlDB.Close() }()
				cipher, _ := crypto.New(bytes.Repeat([]byte{7}, 32))
				creds := credentials.NewService(db, cipher)
				name := "OAuth account"
				row := models.Credential{ID: "account"}
				if err := creds.Apply(&row, credentials.Input{Name: &name, Type: typ, Config: map[string]any{"clientId": "client", "clientSecret": "secret"}}, true); err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
				cfg := &config.Config{PublicURL: fixed}
				manager := NewManager(cfg, db, creds, log)
				authURL, err := manager.Start(context.Background(), row.ID, "https://browser.example:8443/api/oauth/callback")
				if err != nil {
					t.Fatal(err)
				}
				parsed, _ := url.Parse(authURL)
				if parsed.Query().Get("redirect_uri") != redirect {
					t.Fatal("authorization did not use the effective redirect URI", authURL)
				}
				// A restart/configuration change between authorization and callback must not change the exchange URI.
				cfg.PublicURL = "https://changed.example"
				manager = NewManager(cfg, db, creds, log)
				if id, err := manager.Callback(context.Background(), parsed.Query().Get("state"), "test-code"); err != nil || id != row.ID || !exchanged.Load() {
					t.Fatal("callback failed", id, err)
				}
				if err := db.First(&row, "id = ?", row.ID).Error; err != nil || row.OAuthState != "" || row.OAuthRedirectURI != "" {
					t.Fatal("completed flow was not cleared", err)
				}
				cfg.PublicURL = ""
				if token, err := manager.Refresh(context.Background(), row.ID); err != nil || !refreshed.Load() || token.AccessToken != "at-2" || token.RefreshToken != "rt-2" {
					t.Fatal("refresh without PUBLIC_URL failed", token, err)
				}
			})
		}
	}
}

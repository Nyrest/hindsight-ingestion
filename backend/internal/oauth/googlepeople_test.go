package oauth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/config"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/googlepeople"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"golang.org/x/oauth2"
)

func TestGooglePeopleOAuthBindsVerifiedAccount(t *testing.T) {
	var subject atomic.Value
	subject.Store("account")
	userInfo := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Error("userinfo received wrong access token")
		}
		json.NewEncoder(w).Encode(map[string]string{"sub": subject.Load().(string)})
	}))
	defer userInfo.Close()
	transport := httpx.Shared.Clone()
	transport.Proxy = nil
	certificates := x509.NewCertPool()
	certificates.AddCert(userInfo.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: certificates, ServerName: userInfo.Certificate().DNSNames[0]}
	baseDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "openidconnect.googleapis.com:443" {
			address = userInfo.Listener.Addr().String()
		}
		return baseDial(ctx, network, address)
	}
	previousTransport := httpx.Shared
	httpx.Shared = transport
	defer func() { httpx.Shared = previousTransport; transport.CloseIdleConnections() }()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("code_verifier") == "" {
			t.Error("authorization exchange omitted PKCE verifier")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"access","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer tokens.Close()
	previousProvider := providers["google_people"]
	providers["google_people"] = provider{scopes: previousProvider.scopes, endpoint: func(string) oauth2.Endpoint {
		return oauth2.Endpoint{AuthURL: tokens.URL + "/auth", TokenURL: tokens.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}
	}}
	defer func() { providers["google_people"] = previousProvider }()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "oauth.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sql, _ := db.DB(); sql.Close() }()
	cipher, _ := crypto.New(bytes.Repeat([]byte{7}, 32))
	creds := credentials.NewService(db, cipher)
	row := models.Credential{ID: "people"}
	name := "Contacts"
	if err := creds.Apply(&row, credentials.Input{Name: &name, Type: "google_people", Config: map[string]any{"clientId": "client", "clientSecret": "secret"}}, true); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(&config.Config{PublicURL: "https://app.example"}, db, creds, log)
	for _, account := range []string{"account", "account", "other-account", ""} {
		subject.Store(account)
		authorization, err := mgr.Start(t.Context(), row.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := url.Parse(authorization)
		query := parsed.Query()
		if query.Get("access_type") != "offline" || query.Get("prompt") != "consent" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
			t.Fatal("Google consent settings missing", query)
		}
		if scopes := strings.Fields(query.Get("scope")); len(scopes) != 2 || !slices.Contains(scopes, "openid") || !slices.Contains(scopes, "https://www.googleapis.com/auth/contacts.readonly") {
			t.Fatal("incorrect People scopes", scopes)
		}
		_, err = mgr.Callback(t.Context(), query.Get("state"), "code")
		if account == "account" && err != nil {
			t.Fatal(err)
		}
		if account != "account" && err == nil {
			t.Fatal("unbound or different Google account accepted")
		}
		stored, err := creds.Load(t.Context(), row.ID)
		if err != nil || stored.OAuthSubject != "account" || stored.OAuth == nil || stored.OAuth.RefreshToken != "refresh" {
			t.Fatal("failed callback changed bound credential", err)
		}
		if _, err := mgr.Callback(t.Context(), query.Get("state"), "code"); err == nil {
			t.Fatal("callback state was reused")
		}
	}
}

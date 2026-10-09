package oauth

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	stdsync "sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Nyrest/hindsight-ingestion/internal/config"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

type recSched struct {
	mu        stdsync.Mutex
	scheduled map[string]time.Time
	cancelled []string
}

func TestProviderConfigUsesCredentialSettings(t *testing.T) {
	// Legacy process settings must not supply application settings to credentials.
	for _, name := range []string{"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "MICROSOFT_CLIENT_ID", "MICROSOFT_CLIENT_SECRET", "MICROSOFT_TENANT"} {
		t.Setenv(name, "legacy-env-value")
	}
	t.Setenv("DISABLE_AUTH", "true")
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("CREDENTIAL_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PUBLIC_URL", "https://ingestion.example")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{cfg: cfg}
	for _, typ := range []string{"google_drive", "onedrive"} {
		t.Run(typ, func(t *testing.T) {
			if _, err := mgr.ProviderConfig(connectors.Credential{Type: typ}); err == nil {
				t.Fatal("credential without client ID inherited environment settings")
			}
			for _, suffix := range []string{"a", "b"} {
				cred := connectors.Credential{Type: typ,
					Config:  map[string]any{"clientId": "client-" + suffix, "tenant": "tenant-" + suffix},
					Secrets: map[string]string{"clientSecret": "secret-" + suffix},
				}
				oc, err := mgr.ProviderConfig(cred)
				if err != nil {
					t.Fatal(err)
				}
				if oc.ClientID != "client-"+suffix || oc.ClientSecret != "secret-"+suffix || oc.RedirectURL != "https://ingestion.example/api/oauth/callback" {
					t.Fatal("OAuth configuration did not use this credential's settings")
				}
				if typ == "onedrive" && oc.Endpoint.AuthURL != "https://login.microsoftonline.com/tenant-"+suffix+"/oauth2/v2.0/authorize" {
					t.Fatal("OAuth configuration did not use this credential's tenant")
				}
			}
		})
	}
	oc, err := mgr.ProviderConfig(connectors.Credential{Type: "onedrive", Config: map[string]any{"clientId": "client"}})
	if err != nil || oc.Endpoint.AuthURL != "https://login.microsoftonline.com/common/oauth2/v2.0/authorize" {
		t.Fatal("empty tenant did not use the credential default common")
	}
}

func (r *recSched) ScheduleRefresh(id string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scheduled[id] = at
}
func (r *recSched) CancelRefresh(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelled = append(r.cancelled, id)
	delete(r.scheduled, id)
}

func TestRefreshRotationAndInvalidGrant(t *testing.T) {
	connectors.RegisterCredentialType(connectors.CredentialType{Type: "google_drive", OAuth: true, Fields: []connectors.FieldSpec{
		{Key: "clientId", Type: connectors.FieldString}, {Key: "clientSecret", Type: connectors.FieldPassword},
	}})
	var mode string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("redirect_uri") != "" {
			t.Error("refresh unexpectedly depends on a redirect URI")
		}
		w.Header().Set("Content-Type", "application/json")
		if mode == "invalid" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
			return
		}
		if r.Form.Get("refresh_token") != "rt-1" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		io.WriteString(w, `{"access_token":"at-2","refresh_token":"rt-2","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer tokenSrv.Close()
	old := providers["google_drive"]
	providers["google_drive"] = provider{
		endpoint: func(string) oauth2.Endpoint {
			return oauth2.Endpoint{AuthURL: tokenSrv.URL + "/auth", TokenURL: tokenSrv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}
		},
		scopes: old.scopes,
	}
	defer func() { providers["google_drive"] = old }()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "o.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s, _ := db.DB(); s.Close() }()
	cipher, _ := crypto.New(bytes.Repeat([]byte{2}, 32))
	creds := credentials.NewService(db, cipher)
	name := "g"
	m := models.Credential{ID: "c1"}
	if err := creds.Apply(&m, credentials.Input{Name: &name, Type: "google_drive", Config: map[string]any{"clientId": "cid", "clientSecret": "cs"}, HeadersSet: true}, true); err != nil {
		t.Fatal(err)
	}
	db.Create(&m)
	if err := creds.SaveOAuthToken(context.Background(), "c1", &connectors.OAuthToken{AccessToken: "at-1", RefreshToken: "rt-1", Expiry: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(&config.Config{ListenAddr: ":8080", PublicURL: ""}, db, creds, log)
	sched := &recSched{scheduled: map[string]time.Time{}}
	mgr.SetScheduler(sched)

	// Expiring soon → EnsureFresh refreshes and persists the rotated token.
	cred, _ := creds.Load(context.Background(), "c1")
	fresh, err := mgr.EnsureFresh(context.Background(), cred)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.OAuth.AccessToken != "at-2" {
		t.Fatalf("access token = %s", fresh.OAuth.AccessToken)
	}
	stored, _ := creds.Load(context.Background(), "c1")
	if stored.OAuth.RefreshToken != "rt-2" {
		t.Fatal("rotated refresh token not persisted")
	}
	at, ok := sched.scheduled["c1"]
	if !ok || at.Before(time.Now().Add(40*time.Minute)) || at.After(time.Now().Add(46*time.Minute)) {
		t.Fatalf("next refresh scheduled at %v (want ~expiry-15m)", at)
	}

	// Fresh token → no refresh needed.
	if _, err := mgr.EnsureFresh(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	// invalid_grant → reauth_required and refreshes stop.
	mode = "invalid"
	if _, err := mgr.Refresh(context.Background(), "c1"); err != ErrReauthRequired {
		t.Fatalf("err = %v", err)
	}
	var row models.Credential
	db.First(&row, "id = ?", "c1")
	if row.Status != models.CredentialReauthRequired {
		t.Fatalf("status = %s", row.Status)
	}
	if _, still := sched.scheduled["c1"]; still || len(sched.cancelled) == 0 {
		t.Fatal("refresh not cancelled after invalid_grant")
	}
}

package credentials

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestOAuthStateHasOnlyOneConcurrentConsumer(t *testing.T) {
	s := newService(t)
	if err := s.db.Create(&models.Credential{ID: "account"}).Error; err != nil {
		t.Fatal(err)
	}
	flow := OAuthFlow{State: "random-state", RedirectURI: "https://app.test/api/oauth/callback", Verifier: "pkce-secret", ExpiresAt: time.Now().UTC().Add(time.Minute)}
	if err := s.SaveOAuthFlow(t.Context(), "account", flow); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var consumers sync.WaitGroup
	for range 16 {
		consumers.Go(func() {
			row, verifier, err := s.ConsumeOAuthFlow(t.Context(), flow.State)
			if err == nil {
				successes.Add(1)
				if row.ID != "account" || row.OAuthRedirectURI != flow.RedirectURI || verifier != flow.Verifier {
					t.Error("consumer lost stored flow data")
				}
			} else if !errors.Is(err, ErrOAuthState) {
				t.Errorf("unexpected state consumption error: %v", err)
			}
		})
	}
	consumers.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful consumers=%d; expected one", successes.Load())
	}
}

func TestOAuthTokenSavePreservesUndecryptableSecrets(t *testing.T) {
	s := newService(t)
	row := models.Credential{ID: "account", EncryptedSecret: "corrupt-secret"}
	if err := s.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOAuthToken(t.Context(), row.ID, &connectors.OAuthToken{AccessToken: "new-token"}); err == nil {
		t.Fatal("token save accepted undecryptable credentials")
	}
	if err := s.db.First(&row, "id = ?", row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.EncryptedSecret != "corrupt-secret" {
		t.Fatal("failed token save overwrote existing secrets")
	}
}

func TestGoogleAccountBindingSurvivesRefreshAndRejectsSwitch(t *testing.T) {
	s := newService(t)
	encrypted, err := s.cipher.EncryptJSON(secretBlob{Secrets: map[string]string{"clientSecret": "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	row := models.Credential{ID: "people", Type: "google_people", EncryptedSecret: encrypted}
	if err := s.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	token := &connectors.OAuthToken{AccessToken: "first", RefreshToken: "refresh"}
	if err := s.SaveOAuthConnection(t.Context(), row.ID, token, "account"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOAuthToken(t.Context(), row.ID, &connectors.OAuthToken{AccessToken: "refreshed", RefreshToken: "rotated"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOAuthConnection(t.Context(), row.ID, &connectors.OAuthToken{AccessToken: "wrong"}, "other-account"); err == nil {
		t.Fatal("different Google account overwrote credential")
	}
	stored, err := s.Load(t.Context(), row.ID)
	if err != nil || stored.OAuthSubject != "account" || stored.OAuth.AccessToken != "refreshed" || stored.OAuth.RefreshToken != "rotated" || stored.Secrets["clientSecret"] != "secret" {
		t.Fatal("binding or secrets changed", err)
	}
	if err := s.SaveOAuthConnection(t.Context(), row.ID, token, "account"); err != nil {
		t.Fatal("same-account reconnect rejected", err)
	}
	if err := s.SaveOAuthConnection(t.Context(), row.ID, token, ""); err == nil {
		t.Fatal("empty account accepted")
	}
}

func TestGoogleAccountBindingIsAtomic(t *testing.T) {
	s := newService(t)
	encrypted, err := s.cipher.EncryptJSON(secretBlob{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&models.Credential{ID: "people", Type: "google_people", EncryptedSecret: encrypted}).Error; err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var callbacks sync.WaitGroup
	for _, subject := range []string{"account-a", "account-b"} {
		callbacks.Go(func() {
			if s.SaveOAuthConnection(t.Context(), "people", &connectors.OAuthToken{AccessToken: subject}, subject) == nil {
				successes.Add(1)
			}
		})
	}
	callbacks.Wait()
	if successes.Load() != 1 {
		t.Fatal("multiple accounts bound", successes.Load())
	}
}

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

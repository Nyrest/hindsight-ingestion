package credentials

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

var ErrOAuthState = errors.New("unknown or expired OAuth state")

type OAuthFlow struct {
	State       string
	RedirectURI string
	Verifier    string
	ExpiresAt   time.Time
}

func (s *Service) SaveOAuthFlow(ctx context.Context, id string, flow OAuthFlow) error {
	sealed, err := s.cipher.Encrypt([]byte(flow.Verifier))
	if err != nil {
		return err
	}
	updated := s.db.WithContext(ctx).Model(&models.Credential{}).Where("id = ?", id).
		Updates(map[string]any{"o_auth_state": flow.State, "o_auth_redirect_uri": flow.RedirectURI,
			"o_auth_state_expires_at": flow.ExpiresAt, "o_auth_verifier_encrypted": sealed})
	if updated.Error == nil && updated.RowsAffected == 0 {
		return ErrNotFound
	}
	return updated.Error
}

func (s *Service) ConsumeOAuthFlow(ctx context.Context, state string) (models.Credential, string, error) {
	var row models.Credential
	var verifier []byte
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("o_auth_state = ? AND o_auth_state_expires_at > ?", state, time.Now().UTC())
		if err := query.First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOAuthState
			}
			return err
		}
		var err error
		verifier, err = s.cipher.Decrypt(row.OAuthVerifierEncrypted)
		if err != nil {
			return err
		}
		// Consume before contacting the provider so concurrent callbacks cannot reuse state.
		return tx.Model(&models.Credential{}).Where("id = ?", row.ID).Updates(map[string]any{"o_auth_state": "", "o_auth_redirect_uri": "",
			"o_auth_state_expires_at": nil, "o_auth_verifier_encrypted": ""}).Error
	})
	return row, string(verifier), err
}

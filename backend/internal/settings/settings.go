// Package settings stores global settings.
package settings

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/observations"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

// Settings are the global, UI-editable settings.
type Settings struct {
	Proxy                      proxy.Config          `json:"proxy"`
	ObservationScope           observations.Scope    `json:"observationScope"`
	InlineMultimodalEnabled    bool                  `json:"inlineMultimodalEnabled"`
	IncrementalSyncEnabled     bool                  `json:"incrementalSyncEnabled"`
	FullReconcileIntervalHours int                   `json:"fullReconcileIntervalHours"`
	MaxFileSizeMB              int                   `json:"maxFileSizeMB"`
	FilePolicy                 connectors.FilePolicy `json:"filePolicy"`
}

// Defaults are applied for missing settings.
var Defaults = Settings{
	Proxy:                      proxy.Default,
	ObservationScope:           observations.Default,
	IncrementalSyncEnabled:     true,
	FullReconcileIntervalHours: 24,
	MaxFileSizeMB:              100,
	FilePolicy:                 connectors.DefaultFilePolicy,
}

const key = "global"

// Store reads and writes settings.
type Store struct {
	db     *gorm.DB
	cipher *crypto.Cipher
}

// NewStore creates a Store.
func NewStore(db *gorm.DB, cipher *crypto.Cipher) *Store { return &Store{db: db, cipher: cipher} }

func (s *Store) WithDB(db *gorm.DB) *Store { return NewStore(db, s.cipher) }

func (v Settings) Masked() Settings { v.Proxy = v.Proxy.Masked(); return v }

// Get returns the current settings.
func (s *Store) Get(ctx context.Context) (Settings, error) {
	out := Defaults
	var row models.Setting
	err := s.db.WithContext(ctx).Where(&models.Setting{Key: key}).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(row.Value), &out); err != nil {
		return Defaults, nil
	}
	var secret struct {
		ProxyPassword string `json:"proxyPassword"`
	}
	if err := s.cipher.DecryptJSON(row.EncryptedSecret, &secret); err != nil {
		return out, err
	}
	out.Proxy = out.Proxy.Normalize()
	out.Proxy.Password = secret.ProxyPassword
	return out, nil
}

// Validate checks settings bounds.
func (v Settings) Validate() error {
	if err := v.Proxy.Validate(); err != nil {
		return err
	}
	if _, err := v.ObservationScope.Normalize(); err != nil {
		return err
	}
	if v.FullReconcileIntervalHours < 1 || v.FullReconcileIntervalHours > 24*365 {
		return errors.New("fullReconcileIntervalHours must be between 1 and 8760")
	}
	if v.MaxFileSizeMB < 1 || v.MaxFileSizeMB > 10240 {
		return errors.New("maxFileSizeMB must be between 1 and 10240")
	}
	return nil
}

// Put saves settings.
func (s *Store) Put(ctx context.Context, v Settings) error {
	v.Proxy = v.Proxy.Normalize()
	if v.Proxy.Password == proxy.Mask {
		stored, err := s.Get(ctx)
		if err != nil {
			return err
		}
		v.Proxy.Password = stored.Proxy.Password
	}
	secret, err := s.cipher.EncryptJSON(struct {
		ProxyPassword string `json:"proxyPassword"`
	}{v.Proxy.Password})
	if err != nil {
		return err
	}
	v.Proxy.Password = ""
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).
		Create(&models.Setting{Key: key, Value: string(b), EncryptedSecret: secret}).Error
}

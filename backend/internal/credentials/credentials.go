// Package credentials manages encrypted credential storage.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
)

// Mask is returned in place of stored secret values.
const Mask = "********"

// ErrNotFound is returned for unknown credentials.
var ErrNotFound = errors.New("credential not found")

// ValidationError carries per-field messages.
type ValidationError struct {
	Message string
	Fields  map[string]string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(field, msg string) error {
	return &ValidationError{Message: msg, Fields: map[string]string{field: msg}}
}

// secretBlob is the encrypted payload of a credential.
type secretBlob struct {
	ProxyPassword string                 `json:"proxyPassword,omitempty"`
	Secrets       map[string]string      `json:"secrets,omitempty"`
	Headers       map[string]string      `json:"headers,omitempty"`
	OAuth         *connectors.OAuthToken `json:"oauth,omitempty"`
}

// Service stores and decrypts credentials.
type Service struct {
	db     *gorm.DB
	cipher *crypto.Cipher
}

// NewService creates a Service.
func NewService(db *gorm.DB, cipher *crypto.Cipher) *Service {
	return &Service{db: db, cipher: cipher}
}

// Get loads a credential row.
func (s *Service) Get(ctx context.Context, id string) (*models.Credential, error) {
	var m models.Credential
	err := s.db.WithContext(ctx).First(&m, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &m, err
}

// Load loads and decrypts a credential for connector use.
func (s *Service) Load(ctx context.Context, id string) (connectors.Credential, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return connectors.Credential{}, err
	}
	cred, err := s.Decrypt(m)
	if err != nil {
		return cred, err
	}
	if cred.ProxyMode == "global" {
		global, err := settings.NewStore(s.db, s.cipher).Get(ctx)
		if err != nil {
			return cred, err
		}
		cred.Proxy = global.Proxy
	}
	return cred, nil
}

// Decrypt converts a stored credential into its usable form.
func (s *Service) Decrypt(m *models.Credential) (connectors.Credential, error) {
	cred := connectors.Credential{ID: m.ID, Name: m.Name, Type: m.Type, Config: map[string]any{}}
	if m.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(m.ConfigJSON), &cred.Config); err != nil {
			return cred, fmt.Errorf("credential %s: invalid config: %w", m.ID, err)
		}
	}
	cred.Proxy = proxy.Default
	if m.ProxyJSON != "" {
		if err := json.Unmarshal([]byte(m.ProxyJSON), &cred.Proxy); err != nil {
			return cred, err
		}
	}
	cred.Proxy = cred.Proxy.Normalize()
	cred.ProxyMode = m.ProxyMode
	if cred.ProxyMode == "" {
		cred.ProxyMode = "global"
	}
	var blob secretBlob
	if err := s.cipher.DecryptJSON(m.EncryptedSecret, &blob); err != nil {
		return cred, fmt.Errorf("credential %s: %w", m.ID, err)
	}
	cred.Proxy.Password = blob.ProxyPassword
	cred.Secrets = blob.Secrets
	if cred.Secrets == nil {
		cred.Secrets = map[string]string{}
	}
	cred.Headers = blob.Headers
	cred.OAuth = blob.OAuth
	return cred, nil
}

// Input is a create/update request. Nil fields are left unchanged on update.
type Input struct {
	ProxyMode            *string
	ProxyPasswordOmitted bool
	Proxy                *proxy.Config
	Name                 *string
	Type                 string
	Config               map[string]any
	CustomHeaders        map[string]string
	HeadersSet           bool
}

// View is the masked representation returned by the API.
type View struct {
	ProxyMode     string
	Proxy         proxy.Config
	Config        map[string]any
	CustomHeaders map[string]string
	OAuthConnect  bool
}

// MaskedView returns config with secrets masked and headers masked.
func (s *Service) MaskedView(m *models.Credential) (View, error) {
	ct, _ := connectors.CredentialTypeOf(m.Type)
	cred, err := s.Decrypt(m)
	if err != nil {
		// Still render non-secret data so the user can fix/replace the secret.
		cred.Secrets = map[string]string{}
	}
	cfg := map[string]any{}
	for k, v := range cred.Config {
		cfg[k] = v
	}
	for _, f := range ct.Fields {
		if isSecret(f) {
			if cred.Secrets[f.Key] != "" {
				cfg[f.Key] = Mask
			} else {
				cfg[f.Key] = ""
			}
		}
	}
	headers := map[string]string{}
	var names []string
	_ = json.Unmarshal([]byte(m.HeaderNamesJSON), &names)
	for _, n := range names {
		headers[n] = Mask
	}
	return View{ProxyMode: cred.ProxyMode, Proxy: cred.Proxy.Masked(), Config: cfg, CustomHeaders: headers, OAuthConnect: cred.OAuth != nil && cred.OAuth.RefreshToken != ""}, err
}

// Apply validates input and writes it into m (encrypting secrets). On create
// m is a zero value with ID set.
func (s *Service) Apply(m *models.Credential, in Input, creating bool) error {
	if creating {
		ct, ok := connectors.CredentialTypeOf(in.Type)
		if !ok {
			return invalid("type", "unknown credential type")
		}
		m.Type = ct.Type
		m.Status = models.CredentialActive
		if ct.OAuth {
			m.Status = models.CredentialPendingOAuth
		}
	}
	ct, _ := connectors.CredentialTypeOf(m.Type)

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return invalid("name", "name is required")
		}
		m.Name = name
	} else if creating {
		return invalid("name", "name is required")
	}

	var current connectors.Credential
	if !creating {
		var err error
		current, err = s.Decrypt(m)
		if err != nil {
			// Undecryptable secrets (rotated key): allow replacing them.
			current = connectors.Credential{Config: map[string]any{}, Secrets: map[string]string{}}
		}
	} else {
		current = connectors.Credential{Config: map[string]any{}, Secrets: map[string]string{}}
	}

	blob := secretBlob{Secrets: current.Secrets, Headers: current.Headers, OAuth: current.OAuth, ProxyPassword: current.Proxy.Password}
	if blob.Secrets == nil {
		blob.Secrets = map[string]string{}
	}
	cfg := current.Config

	if in.Config != nil || creating {
		cfg = map[string]any{}
		for _, f := range ct.Fields {
			v, present := in.Config[f.Key]
			if isSecret(f) {
				sv := connectors.AsString(v)
				switch {
				case !present || sv == Mask:
					// keep stored value
				case sv == "":
					delete(blob.Secrets, f.Key)
				default:
					blob.Secrets[f.Key] = sv
				}
				if f.Required && blob.Secrets[f.Key] == "" {
					return invalid(f.Key, f.Label+" is required")
				}
				continue
			}
			if !present || v == nil {
				if old, ok := current.Config[f.Key]; ok {
					cfg[f.Key] = old
				} else if f.Default != nil {
					cfg[f.Key] = f.Default
				}
			} else {
				cfg[f.Key] = normalizeValue(f, v)
			}
			if f.Required && connectors.AsString(cfg[f.Key]) == "" {
				return invalid(f.Key, f.Label+" is required")
			}
		}
	}

	if in.HeadersSet {
		headers, names, err := mergeHeaders(blob.Headers, in.CustomHeaders)
		if err != nil {
			return err
		}
		blob.Headers = headers
		nb, _ := json.Marshal(names)
		m.HeaderNamesJSON = string(nb)
	} else if creating {
		m.HeaderNamesJSON = "[]"
	}

	if in.ProxyMode != nil {
		m.ProxyMode = *in.ProxyMode
	}
	if m.ProxyMode == "" {
		m.ProxyMode = "global"
	}
	if m.ProxyMode != "global" && m.ProxyMode != "override" {
		return invalid("proxyMode", "Must be global or override")
	}
	pc := current.Proxy.Normalize()
	if in.Proxy != nil {
		pc = in.Proxy.Normalize()
		if (in.ProxyPasswordOmitted || pc.Password == proxy.Mask) && pc.Type != "default" && pc.Type != "none" {
			pc.Password = current.Proxy.Password
		}
	}
	if err := pc.Validate(); err != nil {
		return invalid("proxy", err.Error())
	}
	blob.ProxyPassword = pc.Password
	pc.Password = ""
	pb, err := json.Marshal(pc)
	if err != nil {
		return err
	}
	m.ProxyJSON = string(pb)
	cb, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	m.ConfigJSON = string(cb)
	enc, err := s.cipher.EncryptJSON(blob)
	if err != nil {
		return err
	}
	m.EncryptedSecret = enc
	return nil
}

// SaveOAuthToken persists a token immediately (refresh token rotation safe).
func (s *Service) SaveOAuthToken(ctx context.Context, id string, tok *connectors.OAuthToken) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m models.Credential
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&m, "id = ?", id).Error; err != nil {
			return err
		}
		cred, err := s.Decrypt(&m)
		if err != nil {
			cred = connectors.Credential{Secrets: map[string]string{}}
		}
		blob := secretBlob{Secrets: cred.Secrets, Headers: cred.Headers, OAuth: tok, ProxyPassword: cred.Proxy.Password}
		enc, err := s.cipher.EncryptJSON(blob)
		if err != nil {
			return err
		}
		exp := tok.Expiry
		updates := map[string]any{
			"encrypted_secret": enc,
			"status":           models.CredentialActive,
			"status_message":   "",
			"o_auth_state":     "",
		}
		if exp.IsZero() {
			updates["o_auth_expires_at"] = nil
		} else {
			updates["o_auth_expires_at"] = exp.UTC()
		}
		return tx.Model(&models.Credential{}).Where("id = ?", id).Updates(updates).Error
	})
}

// SetStatus updates a credential's status.
func (s *Service) SetStatus(ctx context.Context, id, status, message string) error {
	return s.db.WithContext(ctx).Model(&models.Credential{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "status_message": message}).Error
}

func isSecret(f connectors.FieldSpec) bool {
	return f.Secret || f.Type == connectors.FieldPassword
}

func normalizeValue(f connectors.FieldSpec, v any) any {
	switch f.Type {
	case connectors.FieldBoolean:
		return connectors.AsBool(v)
	case connectors.FieldNumber:
		if n, ok := connectors.AsFloat(v); ok {
			return n
		}
		return nil
	case connectors.FieldURL:
		return strings.TrimRight(connectors.AsString(v), "/")
	}
	return connectors.AsString(v)
}

// mergeHeaders validates the incoming header set and resolves masked values
// against the stored ones.
func mergeHeaders(stored, incoming map[string]string) (map[string]string, []string, error) {
	out := map[string]string{}
	var names []string
	seen := map[string]string{}
	keys := make([]string, 0, len(incoming))
	for k := range incoming {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, rawName := range keys {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, nil, invalid("customHeaders", "header names must not be empty")
		}
		if !validHeaderName(name) {
			return nil, nil, invalid("customHeaders", fmt.Sprintf("invalid header name %q", name))
		}
		lower := strings.ToLower(name)
		if prev, dup := seen[lower]; dup {
			return nil, nil, invalid("customHeaders", fmt.Sprintf("duplicate header %q (conflicts with %q)", name, prev))
		}
		seen[lower] = name
		value := incoming[rawName]
		if value == Mask {
			found := false
			for sk, sv := range stored {
				if strings.EqualFold(sk, name) {
					value, found = sv, true
					break
				}
			}
			if !found {
				return nil, nil, invalid("customHeaders", fmt.Sprintf("header %q has no stored value", name))
			}
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, nil, invalid("customHeaders", fmt.Sprintf("header %q contains a newline", name))
		}
		out[name] = value
		names = append(names, name)
	}
	if names == nil {
		names = []string{}
	}
	slices.Sort(names)
	return out, names, nil
}

func validHeaderName(s string) bool {
	for _, r := range s {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune(`()<>@,;:\"/[]?={}`, r) {
			return false
		}
	}
	return true
}

// Package oauth implements OAuth authorization and token refresh for Google
// Drive and OneDrive credentials.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/config"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

// SafetyMargin is how long before expiry a token is refreshed.
const SafetyMargin = 15 * time.Minute

// ErrReauthRequired means the refresh token is no longer valid.
var ErrReauthRequired = errors.New("OAuth reauthorization required")

// Scheduler schedules one-time refresh jobs. Implemented by the scheduler
// package; nil-safe.
type Scheduler interface {
	ScheduleRefresh(credentialID string, at time.Time)
	CancelRefresh(credentialID string)
}

// Manager owns OAuth flows and refreshes.
type Manager struct {
	cfg   *config.Config
	db    *gorm.DB
	creds *credentials.Service
	log   *slog.Logger

	mu    sync.Mutex
	sched Scheduler
	group singleflight.Group
}

// NewManager creates a Manager.
func NewManager(cfg *config.Config, db *gorm.DB, creds *credentials.Service, log *slog.Logger) *Manager {
	return &Manager{cfg: cfg, db: db, creds: creds, log: log}
}

// SetScheduler wires the refresh scheduler.
func (m *Manager) SetScheduler(s Scheduler) {
	m.mu.Lock()
	m.sched = s
	m.mu.Unlock()
}

func (m *Manager) scheduler() Scheduler {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sched
}

// ProviderConfig uses the OAuth application settings stored in the credential.
func (m *Manager) ProviderConfig(cred connectors.Credential) (*oauth2.Config, error) {
	p, ok := providers[cred.Type]
	if !ok {
		return nil, fmt.Errorf("credential type %s does not use OAuth", cred.Type)
	}
	clientID := cred.String("clientId")
	if clientID == "" {
		return nil, fmt.Errorf("OAuth client ID is not configured for this credential")
	}
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: cred.String("clientSecret"),
		Endpoint:     p.endpoint(cred.String("tenant")),
		RedirectURL:  m.cfg.OAuthRedirectURI(),
		Scopes:       p.scopes,
	}, nil
}

// Start begins an authorization flow, returning the provider URL.
func (m *Manager) Start(ctx context.Context, credentialID string) (string, error) {
	cred, err := m.creds.Load(ctx, credentialID)
	if err != nil {
		return "", err
	}
	oc, err := m.ProviderConfig(cred)
	if err != nil {
		return "", err
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	state := hex.EncodeToString(b)
	if err := m.db.WithContext(ctx).Model(&models.Credential{}).Where("id = ?", credentialID).
		Update("o_auth_state", state).Error; err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oauth2.AccessTypeOffline}
	if cred.Type == "google_drive" {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "consent"))
	} else {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "select_account"))
	}
	return oc.AuthCodeURL(state, opts...), nil
}

// Callback completes an authorization flow and returns the credential ID.
func (m *Manager) Callback(ctx context.Context, state, code string) (string, error) {
	if state == "" || code == "" {
		return "", errors.New("missing state or code")
	}
	var row models.Credential
	if err := m.db.WithContext(ctx).First(&row, "o_auth_state = ?", state).Error; err != nil {
		return "", errors.New("unknown or expired OAuth state")
	}
	cred, err := m.creds.Load(ctx, row.ID)
	if err != nil {
		return row.ID, err
	}
	oc, err := m.ProviderConfig(cred)
	if err != nil {
		return row.ID, err
	}
	tok, err := oc.Exchange(oauthContext(ctx, cred.Proxy), code)
	if err != nil {
		return row.ID, fmt.Errorf("token exchange failed: %s", cred.Proxy.Redact(sanitize(err)))
	}
	if tok.RefreshToken == "" && cred.OAuth != nil {
		tok.RefreshToken = cred.OAuth.RefreshToken
	}
	stored := fromToken(tok)
	if err := m.creds.SaveOAuthToken(ctx, row.ID, stored); err != nil {
		return row.ID, err
	}
	m.log.Info("oauth connected", "credential", row.ID, "type", row.Type)
	m.scheduleNext(row.ID, stored)
	return row.ID, nil
}

// Refresh refreshes a credential's token now, persists it immediately and
// schedules the next one-time refresh. Concurrent refreshes of the same
// credential are coalesced.
func (m *Manager) Refresh(ctx context.Context, credentialID string) (*connectors.OAuthToken, error) {
	v, err, _ := m.group.Do(credentialID, func() (any, error) {
		return m.refresh(context.WithoutCancel(ctx), credentialID)
	})
	if err != nil {
		return nil, err
	}
	return v.(*connectors.OAuthToken), nil
}

func (m *Manager) refresh(ctx context.Context, credentialID string) (*connectors.OAuthToken, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cred, err := m.creds.Load(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	if cred.OAuth == nil || cred.OAuth.RefreshToken == "" {
		return nil, ErrReauthRequired
	}
	oc, err := m.ProviderConfig(cred)
	if err != nil {
		return nil, err
	}
	src := oc.TokenSource(oauthContext(ctx, cred.Proxy), &oauth2.Token{RefreshToken: cred.OAuth.RefreshToken, Expiry: time.Unix(1, 0)})
	tok, err := src.Token()
	if err != nil {
		if isInvalidGrant(err) {
			_ = m.creds.SetStatus(ctx, credentialID, models.CredentialReauthRequired, "Refresh token rejected (invalid_grant). Reconnect the account.")
			if s := m.scheduler(); s != nil {
				s.CancelRefresh(credentialID)
			}
			m.log.Warn("oauth refresh rejected; reauthorization required", "credential", credentialID)
			return nil, ErrReauthRequired
		}
		m.log.Warn("oauth refresh failed", "credential", credentialID, "error", cred.Proxy.Redact(sanitize(err)))
		// Transient: retry in a few minutes.
		if s := m.scheduler(); s != nil {
			s.ScheduleRefresh(credentialID, time.Now().Add(5*time.Minute))
		}
		return nil, fmt.Errorf("token refresh failed: %s", cred.Proxy.Redact(sanitize(err)))
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = cred.OAuth.RefreshToken // provider did not rotate
	}
	stored := fromToken(tok)
	// Persist immediately: rotated refresh tokens invalidate the old one.
	if err := m.creds.SaveOAuthToken(ctx, credentialID, stored); err != nil {
		return nil, err
	}
	m.log.Info("oauth token refreshed", "credential", credentialID, "expires", stored.Expiry.Format(time.RFC3339))
	m.scheduleNext(credentialID, stored)
	return stored, nil
}

// EnsureFresh returns a credential whose access token is valid for at least
// the safety margin, refreshing if needed.
func (m *Manager) EnsureFresh(ctx context.Context, cred connectors.Credential) (connectors.Credential, error) {
	if _, ok := providers[cred.Type]; !ok {
		return cred, nil
	}
	if cred.OAuth == nil || cred.OAuth.RefreshToken == "" {
		return cred, fmt.Errorf("credential %q is not connected; complete OAuth connect first", cred.Name)
	}
	if cred.OAuth.AccessToken != "" && !cred.OAuth.Expiry.IsZero() && time.Until(cred.OAuth.Expiry) > SafetyMargin {
		return cred, nil
	}
	tok, err := m.Refresh(ctx, cred.ID)
	if err != nil {
		if errors.Is(err, ErrReauthRequired) {
			return cred, fmt.Errorf("credential %q requires OAuth reconnect", cred.Name)
		}
		return cred, err
	}
	cred.OAuth = tok
	return cred, nil
}

// ScheduleAll schedules refresh jobs for every connected OAuth credential;
// called on startup.
func (m *Manager) ScheduleAll(ctx context.Context) error {
	var rows []models.Credential
	if err := m.db.WithContext(ctx).Where("type IN ? AND status = ?", OAuthTypes(), models.CredentialActive).Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		cred, err := m.creds.Decrypt(&rows[i])
		if err != nil || cred.OAuth == nil || cred.OAuth.RefreshToken == "" {
			continue
		}
		m.scheduleNext(rows[i].ID, cred.OAuth)
	}
	return nil
}

func (m *Manager) scheduleNext(id string, tok *connectors.OAuthToken) {
	s := m.scheduler()
	if s == nil || tok.Expiry.IsZero() {
		return
	}
	at := tok.Expiry.Add(-SafetyMargin)
	if at.Before(time.Now().Add(5 * time.Second)) {
		at = time.Now().Add(5 * time.Second)
	}
	s.ScheduleRefresh(id, at)
}

// OAuthTypes lists credential types using OAuth.
func OAuthTypes() []string {
	out := make([]string, 0, len(providers))
	for k := range providers {
		out = append(out, k)
	}
	return out
}

// TokenSource returns a static token source for API calls.
func TokenSource(cred connectors.Credential) oauth2.TokenSource {
	t := &oauth2.Token{TokenType: "Bearer"}
	if cred.OAuth != nil {
		t.AccessToken = cred.OAuth.AccessToken
		t.Expiry = cred.OAuth.Expiry
	}
	return oauth2.StaticTokenSource(t)
}

type provider struct {
	endpoint func(tenant string) oauth2.Endpoint
	scopes   []string
}

var providers = map[string]provider{
	"google_drive": {
		endpoint: func(string) oauth2.Endpoint {
			return oauth2.Endpoint{
				AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL:  "https://oauth2.googleapis.com/token",
				AuthStyle: oauth2.AuthStyleInParams,
			}
		},
		scopes: []string{"https://www.googleapis.com/auth/drive.readonly"},
	},
	"onedrive": {
		endpoint: func(tenant string) oauth2.Endpoint {
			if tenant == "" {
				tenant = "common"
			}
			return oauth2.Endpoint{
				AuthURL:   "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/authorize",
				TokenURL:  "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token",
				AuthStyle: oauth2.AuthStyleInParams,
			}
		},
		scopes: []string{"offline_access", "Files.Read.All", "Sites.Read.All", "User.Read"},
	},
}

func fromToken(t *oauth2.Token) *connectors.OAuthToken {
	return &connectors.OAuthToken{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		TokenType:    t.TokenType,
		Expiry:       t.Expiry.UTC(),
	}
}

func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return re.ErrorCode == "invalid_grant" || re.ErrorCode == "unauthorized_client"
	}
	return strings.Contains(err.Error(), "invalid_grant")
}

// sanitize renders an oauth error without response bodies that may echo
// tokens.
func sanitize(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		msg := re.ErrorCode
		if re.ErrorDescription != "" {
			msg += ": " + re.ErrorDescription
		}
		if msg == "" && re.Response != nil {
			msg = re.Response.Status
		}
		return msg
	}
	return err.Error()
}

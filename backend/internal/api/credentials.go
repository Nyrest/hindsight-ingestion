package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/oauth"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

type credentialDTO struct {
	ProxyMode      string            `json:"proxyMode" enum:"global,override"`
	Proxy          proxy.Config      `json:"proxy"`
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	Config         map[string]any    `json:"config"`
	CustomHeaders  map[string]string `json:"customHeaders"`
	Status         string            `json:"status" enum:"active,reauth_required,pending_oauth,error"`
	StatusMessage  string            `json:"statusMessage"`
	OAuthConnected bool              `json:"oauthConnected"`
	OAuthExpiresAt *string           `json:"oauthExpiresAt"`
	UsedByTasks    int64             `json:"usedByTasks"`
	CreatedAt      string            `json:"createdAt"`
	UpdatedAt      string            `json:"updatedAt"`
}

func (s *Server) credentialView(ctx context.Context, m *models.Credential) credentialDTO {
	view, err := s.Creds.MaskedView(m)
	status, msg := m.Status, m.StatusMessage
	if err != nil {
		status, msg = models.CredentialError, "Stored secrets cannot be decrypted (was CREDENTIAL_ENCRYPTION_KEY changed?). Re-enter them."
	}
	var used int64
	s.DB.WithContext(ctx).Model(&models.Task{}).
		Where("source_credential_id = ? OR destination_credential_id = ?", m.ID, m.ID).Count(&used)
	return credentialDTO{
		ID: m.ID, Name: m.Name, Type: m.Type, ProxyMode: view.ProxyMode, Proxy: view.Proxy, Config: view.Config, CustomHeaders: view.CustomHeaders,
		Status: status, StatusMessage: msg, OAuthConnected: view.OAuthConnect,
		OAuthExpiresAt: timePtr(m.OAuthExpiresAt), UsedByTasks: used,
		CreatedAt: timeStr(m.CreatedAt), UpdatedAt: timeStr(m.UpdatedAt),
	}
}

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	var rows []models.Credential
	if err := s.DB.WithContext(r.Context()).Order("name").Find(&rows).Error; err != nil {
		s.fail(w, err)
		return
	}
	out := make([]credentialDTO, 0, len(rows))
	for i := range rows {
		out = append(out, s.credentialView(r.Context(), &rows[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

type credentialProxyBody struct {
	Type     string  `json:"type" enum:"default,none,http,https,socks5"`
	Address  string  `json:"address"`
	Username string  `json:"username"`
	Password *string `json:"password"`
}

type credentialBody struct {
	ProxyMode     *string              `json:"proxyMode" enum:"global,override"`
	Proxy         *credentialProxyBody `json:"proxy"`
	Name          *string              `json:"name"`
	Type          string               `json:"type"`
	Config        map[string]any       `json:"config"`
	CustomHeaders *map[string]string   `json:"customHeaders"`
}

func (b credentialBody) input() credentials.Input {
	in := credentials.Input{ProxyMode: b.ProxyMode, Name: b.Name, Type: b.Type, Config: b.Config}
	if b.Proxy != nil {
		in.Proxy = &proxy.Config{Type: b.Proxy.Type, Address: b.Proxy.Address, Username: b.Proxy.Username}
		in.ProxyPasswordOmitted = b.Proxy.Password == nil
		if b.Proxy.Password != nil {
			in.Proxy.Password = *b.Proxy.Password
		}
	}
	if b.CustomHeaders != nil {
		in.HeadersSet = true
		in.CustomHeaders = *b.CustomHeaders
	}
	return in
}

func (s *Server) createCredential(w http.ResponseWriter, r *http.Request) {
	var body credentialBody
	if !decode(w, r, &body) {
		return
	}
	m := models.Credential{ID: uuid.NewString()}
	in := body.input()
	in.HeadersSet = true
	if err := s.Creds.Apply(&m, in, true); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.DB.WithContext(r.Context()).Create(&m).Error; err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("credential created", "credential", m.ID, "type", m.Type)
	writeJSON(w, http.StatusCreated, s.credentialView(r.Context(), &m))
}

func (s *Server) getCredential(w http.ResponseWriter, r *http.Request) {
	m, err := s.Creds.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.credentialView(r.Context(), m))
}

func (s *Server) patchCredential(w http.ResponseWriter, r *http.Request) {
	var body credentialBody
	if !decode(w, r, &body) {
		return
	}
	var m models.Credential
	// Read-modify-write under a row lock so a concurrent OAuth refresh
	// (which rotates the refresh token) is never overwritten. Apply touches
	// no database state, so the transaction holds no other queries.
	err := s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&m, "id = ?", r.PathValue("id")).Error; err != nil {
			return err
		}
		if body.Type != "" && body.Type != m.Type {
			return &credentials.ValidationError{Message: "credential type cannot be changed", Fields: map[string]string{"type": "cannot be changed"}}
		}
		// Secret rotation never touches task state.
		if err := s.Creds.Apply(&m, body.input(), false); err != nil {
			return err
		}
		if m.Status == models.CredentialError {
			m.Status, m.StatusMessage = models.CredentialActive, ""
		}
		return tx.Save(&m).Error
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.credentialView(r.Context(), &m))
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var used int64
	s.DB.WithContext(r.Context()).Model(&models.Task{}).
		Where("source_credential_id = ? OR destination_credential_id = ?", id, id).Count(&used)
	if used > 0 {
		writeError(w, http.StatusConflict, "conflict", "credential is used by one or more tasks")
		return
	}
	res := s.DB.WithContext(r.Context()).Delete(&models.Credential{}, "id = ?", id)
	if res.Error != nil {
		s.fail(w, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		writeError(w, http.StatusNotFound, "not_found", "credential not found")
		return
	}
	s.Sched.CancelRefresh(id)
	w.WriteHeader(http.StatusNoContent)
}

// loadFresh loads a credential and refreshes OAuth tokens if needed.
func (s *Server) loadFresh(ctx context.Context, id string) (connectors.Credential, error) {
	cred, err := s.Creds.Load(ctx, id)
	if err != nil {
		return cred, err
	}
	return s.OAuth.EnsureFresh(ctx, cred)
}

func (s *Server) testCredential(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	id := r.PathValue("id")
	cred, err := s.loadFresh(ctx, id)
	if errors.Is(err, credentials.ErrNotFound) {
		s.fail(w, err)
		return
	}
	var msg string
	if err == nil {
		msg, err = validateCredential(ctx, cred)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, testDTO{false, err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, testDTO{true, msg})
}

func validateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	if cred.Type == hindsight.CredentialType {
		c, err := hindsight.NewClient(cred)
		if err != nil {
			return "", err
		}
		banks, err := c.ListBanks(ctx)
		if err != nil {
			return "", err
		}
		v, _ := c.Version(ctx)
		msg := "Connected"
		if v != "" {
			msg += " to Hindsight " + v
		}
		return msg + " (" + itoa(len(banks)) + " banks visible)", nil
	}
	for _, src := range connectors.Sources() {
		if src.CredentialType == cred.Type {
			conn, _ := connectors.Source(src.Type)
			return conn.ValidateCredential(ctx, cred)
		}
	}
	return "", errors.New("no connector for credential type " + cred.Type)
}

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

func (s *Server) refreshCredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.Creds.Get(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.OAuth.Refresh(r.Context(), id); err != nil {
		msg := err.Error()
		if errors.Is(err, oauth.ErrReauthRequired) {
			msg = "The account must be reconnected (refresh token rejected or missing)"
		}
		writeError(w, http.StatusBadRequest, "upstream", msg)
		return
	}
	m, _ = s.Creds.Get(r.Context(), id)
	writeJSON(w, http.StatusOK, s.credentialView(r.Context(), m))
}

func (s *Server) browseCredential(w http.ResponseWriter, r *http.Request) {
	var req connectors.BrowseRequest
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cred, err := s.loadFresh(ctx, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			s.fail(w, err)
			return
		}
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	var res connectors.BrowseResult
	if cred.Type == hindsight.CredentialType {
		res, err = browseBanks(ctx, cred)
	} else {
		var conn connectors.SourceConnector
		for _, src := range connectors.Sources() {
			if src.CredentialType == cred.Type {
				conn, _ = connectors.Source(src.Type)
			}
		}
		if conn == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "credential type cannot be browsed")
			return
		}
		res, err = conn.Browse(ctx, cred, req)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	if res.Items == nil {
		res.Items = []connectors.BrowseItem{}
	}
	res.ParentID = req.ParentID
	writeJSON(w, http.StatusOK, res)
}

func browseBanks(ctx context.Context, cred connectors.Credential) (connectors.BrowseResult, error) {
	c, err := hindsight.NewClient(cred)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	banks, err := c.ListBanks(ctx)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	res := connectors.BrowseResult{Breadcrumbs: []connectors.Breadcrumb{{ID: "", Name: "Banks"}}}
	for _, b := range banks {
		name := b.BankID
		if b.Name != "" && b.Name != b.BankID {
			name = b.Name + " (" + b.BankID + ")"
		}
		res.Items = append(res.Items, connectors.BrowseItem{ID: b.BankID, Name: name, Kind: connectors.KindBank, Path: b.BankID, Selectable: true})
	}
	sort.Slice(res.Items, func(i, j int) bool { return res.Items[i].ID < res.Items[j].ID })
	return res, nil
}

func (s *Server) credentialStrategies(w http.ResponseWriter, r *http.Request) {
	bankID := r.URL.Query().Get("bankId")
	if bankID == "" {
		writeValidation(w, "bankId is required", map[string]string{"bankId": "required"})
		return
	}
	cred, err := s.Creds.Load(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if cred.Type != hindsight.CredentialType {
		writeError(w, http.StatusBadRequest, "bad_request", "not a Hindsight credential")
		return
	}
	c, err := hindsight.NewClient(cred)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	def, names, err := c.Strategies(r.Context(), bankID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	sort.Strings(names)
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, strategiesDTO{def, names})
}

func (s *Server) listConnectors(w http.ResponseWriter, r *http.Request) {
	types := append([]connectors.CredentialType{}, connectors.CredentialTypes()...)
	for i := range types {
		if types[i].Fields == nil {
			types[i].Fields = []connectors.FieldSpec{}
		}
	}
	sources := connectors.Sources()
	for i := range sources {
		if sources[i].FilterFields == nil {
			sources[i].FilterFields = []connectors.FilterFieldSpec{}
		}
	}
	writeJSON(w, http.StatusOK, connectorsDTO{types, sources})
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

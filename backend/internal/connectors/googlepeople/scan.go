package googlepeople

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxBaselineAge = 6 * 24 * time.Hour

var errRebaseline = errors.New("Google Contacts requires a new full baseline")

type cursorState struct {
	Version    int       `json:"version"`
	Subject    string    `json:"subject"`
	SyncToken  string    `json:"syncToken"`
	FullSyncAt time.Time `json:"fullSyncAt"`
	QueryHash  string    `json:"queryHash"`
}

type connectionsPage struct {
	Connections   []person `json:"connections"`
	NextPageToken string   `json:"nextPageToken"`
	NextSyncToken string   `json:"nextSyncToken"`
}

type scanner struct {
	connector *Connector
	request   connectors.ScanRequest
	client    *httpx.Client
	query     url.Values
}

func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (scannedResult connectors.ScanResult, scanErr error) {
	if err := c.ValidateConfig(req.Config, req.Filter); err != nil {
		return connectors.ScanResult{}, err
	}
	if c.DB == nil || req.TaskID == "" {
		return connectors.ScanResult{}, errors.New("Google People scan requires task storage")
	}
	if req.Credential.OAuthSubject == "" {
		return connectors.ScanResult{}, errors.New("Google account is not bound; reconnect the credential")
	}
	if req.RunID == "" {
		req.RunID = uuid.NewString()
	}
	if req.Preview {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.AbortScan(cleanup, req.RunID); err != nil {
				cleanupErr := fmt.Errorf("clear Google People preview staging: %w", err)
				if scanErr != nil {
					scanErr = errors.Join(scanErr, cleanupErr)
				} else {
					scannedResult.Error = errors.Join(scannedResult.Error, cleanupErr)
				}
			}
		}()
	}
	if err := c.checkAccount(ctx, req); err != nil {
		return connectors.ScanResult{}, err
	}
	if !req.Preview {
		if err := c.clearAbandoned(ctx, req); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	scan := scanner{connector: c, request: req, client: client(req.Credential), query: url.Values{
		"personFields": {personFields}, "pageSize": {"1000"}, "requestSyncToken": {"true"}, "sources": {"READ_SOURCE_TYPE_CONTACT"},
	}}
	scanned, err := scan.run(ctx)
	if err != nil {
		return connectors.ScanResult{Error: err}, nil
	}
	return scanned, nil
}

func (c *Connector) checkAccount(ctx context.Context, req connectors.ScanRequest) error {
	var state models.TaskState
	err := c.DB.WithContext(ctx).First(&state, "task_id = ?", req.TaskID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if state.CommittedCursorJSON != "" {
		var committed cursorState
		if err := json.Unmarshal([]byte(state.CommittedCursorJSON), &committed); err != nil {
			return fmt.Errorf("invalid Google People cursor: %w", err)
		}
		if committed.Subject != req.Credential.OAuthSubject {
			return errors.New("Google account changed; create a new credential and task")
		}
	}
	var conflicts int64
	if err := c.DB.WithContext(ctx).Model(&models.GooglePeopleIdentity{}).
		Where("task_id = ? AND subject <> ?", req.TaskID, req.Credential.OAuthSubject).Count(&conflicts).Error; err != nil {
		return err
	}
	if conflicts != 0 {
		return errors.New("Google account changed; create a new credential and task")
	}
	return nil
}

func (s *scanner) run(ctx context.Context) (connectors.ScanResult, error) {
	var previous cursorState
	if len(s.request.Cursor) > 0 {
		if err := json.Unmarshal(s.request.Cursor, &previous); err != nil {
			return connectors.ScanResult{}, fmt.Errorf("invalid Google People cursor: %w", err)
		}
		if previous.Subject != s.request.Credential.OAuthSubject {
			return connectors.ScanResult{}, errors.New("Google account changed; create a new credential and task")
		}
	}
	digest := sha256.Sum256([]byte(s.query.Encode()))
	queryHash := hex.EncodeToString(digest[:])
	full := s.request.Full || needsBaseline(previous, queryHash)
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.connector.AbortScan(ctx, s.request.RunID); err != nil {
			return connectors.ScanResult{}, err
		}
		baseline := previous.FullSyncAt
		token := previous.SyncToken
		if full {
			token, baseline = "", time.Now().UTC()
		}
		nextToken, err := s.fetch(ctx, token)
		if !full && errors.Is(err, errRebaseline) {
			full = true
			if s.request.Log != nil {
				s.request.Log("info", "Google Contacts sync state expired or a deletion identity is unknown; performing full reconciliation")
			}
			continue
		}
		if err != nil {
			return connectors.ScanResult{}, err
		}
		if err := s.emit(ctx); err != nil {
			return connectors.ScanResult{}, err
		}
		cursor, err := json.Marshal(cursorState{Version: 1, Subject: s.request.Credential.OAuthSubject, SyncToken: nextToken, FullSyncAt: baseline, QueryHash: queryHash})
		return connectors.ScanResult{Cursor: cursor, Complete: full}, err
	}
	return connectors.ScanResult{}, errRebaseline
}

func needsBaseline(previous cursorState, queryHash string) bool {
	return previous.Version != 1 || previous.SyncToken == "" || previous.QueryHash != queryHash ||
		previous.FullSyncAt.IsZero() || time.Since(previous.FullSyncAt) >= maxBaselineAge
}

func (s *scanner) fetch(ctx context.Context, syncToken string) (string, error) {
	query := make(url.Values, len(s.query))
	for key, values := range s.query {
		query[key] = append([]string(nil), values...)
	}
	if syncToken != "" {
		query.Set("syncToken", syncToken)
	}
	seenPages := map[string]bool{}
	for {
		var page connectionsPage
		if err := s.client.JSON(ctx, http.MethodGet, apiBase+"/people/me/connections?"+query.Encode(), nil, &page); err != nil {
			if syncToken != "" && expiredSyncToken(err) {
				return "", errRebaseline
			}
			return "", err
		}
		if err := s.stagePage(ctx, page.Connections); err != nil {
			return "", err
		}
		if page.NextPageToken == "" {
			if page.NextSyncToken == "" {
				return "", errors.New("Google Contacts final page did not return a sync token")
			}
			return page.NextSyncToken, nil
		}
		if seenPages[page.NextPageToken] {
			return "", errors.New("Google Contacts repeated a page token")
		}
		seenPages[page.NextPageToken] = true
		query.Set("pageToken", page.NextPageToken)
	}
}

func expiredSyncToken(err error) bool {
	var response *httpx.HTTPError
	if !errors.As(err, &response) {
		return false
	}
	if response.Status == http.StatusGone {
		return true
	}
	var failure struct {
		Error struct {
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(response.Body), &failure) != nil {
		return false
	}
	for _, detail := range failure.Error.Details {
		if detail.Reason == "EXPIRED_SYNC_TOKEN" {
			return true
		}
	}
	return false
}

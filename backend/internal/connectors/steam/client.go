package steam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const dailyBudget = 80000
const apiBase = "https://api.steampowered.com/"

type apiClient struct{ http *httpx.Client }

var pacing = struct {
	sync.Mutex
	next map[string]time.Time
}{next: map[string]time.Time{}}

func CredentialKey(cred connectors.Credential) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(cred.String("apiKey")))))
	return hex.EncodeToString(sum[:])
}

func pace(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pacing.Lock()
	now := time.Now()
	next := pacing.next[key]
	if next.Before(now) {
		next = now
	}
	pacing.next[key] = next.Add(time.Second)
	pacing.Unlock()
	timer := time.NewTimer(time.Until(next))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func reserve(ctx context.Context, db *gorm.DB, key string, now time.Time) error {
	if db == nil {
		return errors.New("steam: database is unavailable")
	}
	day := now.UTC().Format("2006-01-02")
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := models.SteamAPIUsage{KeyHash: key, Day: day}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		updated := tx.Model(&models.SteamAPIUsage{}).Where("key_hash = ? AND day = ? AND requests < ?", key, day, dailyBudget).UpdateColumn("requests", gorm.Expr("requests + 1"))
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("steam: daily API request budget exhausted")
		}
		return nil
	})
}

func (c *Connector) client(cred connectors.Credential) (*apiClient, error) {
	if err := validateKey(cred); err != nil {
		return nil, err
	}
	h := httpx.New(cred.Headers, map[string]string{"x-webapi-key": strings.TrimSpace(cred.String("apiKey"))}, cred.Proxy)
	// All endpoints are fixed; redirects cannot send authentication elsewhere.
	h.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	key := CredentialKey(cred)
	h.Pace = func(ctx context.Context) error {
		if err := pace(ctx, key); err != nil {
			return err
		}
		return reserve(ctx, c.DB, key, time.Now())
	}
	return &apiClient{http: h}, nil
}

func (c *apiClient) get(ctx context.Context, path string, params map[string]string, out any) error {
	values := url.Values{}
	values.Set("format", "json")
	for key, value := range params {
		values.Set(key, value)
	}
	resp, err := c.http.Do(ctx, httpx.Request{Method: http.MethodGet, URL: apiBase + path + "?" + values.Encode(), Header: http.Header{"Accept": []string{"application/json"}}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	const maxResponse = 32 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("steam: read %s response: %w", path, err)
	}
	if len(body) > maxResponse {
		return fmt.Errorf("steam: %s response exceeds 32 MiB", path)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("steam: invalid %s response", path)
	}
	return nil
}

func achievementError(err error) string {
	var httpErr *httpx.HTTPError
	if !errors.As(err, &httpErr) {
		return ""
	}
	var body achievementResponse
	if json.Unmarshal([]byte(httpErr.Body), &body) != nil || body.PlayerStats == nil {
		return ""
	}
	return body.PlayerStats.Error
}

package api_test

import (
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/oauth"
)

func TestDashboardRecentFailuresOnlyIncludesLastSevenDays(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	withinWeek := now.Add(-6 * 24 * time.Hour)
	for _, row := range []models.TaskRun{
		{ID: "old", TaskID: "task", Status: models.RunFailed, StartedAt: &old, FinishedAt: &old},
		{ID: "long-run", TaskID: "task", Status: models.RunFailed, StartedAt: &old, FinishedAt: &recent},
		{ID: "interrupted", TaskID: "task", Status: models.RunInterrupted, StartedAt: &recent},
		{ID: "six-days-ago", TaskID: "task", Status: models.RunFailed, StartedAt: &withinWeek, FinishedAt: &withinWeek},
		{ID: "success", TaskID: "task", Status: models.RunSucceeded, StartedAt: &recent, FinishedAt: &now},
		{ID: "cancelled", TaskID: "task", Status: models.RunCancelled, StartedAt: &recent, FinishedAt: &now},
	} {
		if err := e.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	var dashboard struct{ RecentFailures []struct{ ID string } }
	if status := e.do("GET", "/api/dashboard", nil, &dashboard); status != 200 {
		t.Fatalf("status=%d", status)
	}
	if len(dashboard.RecentFailures) != 3 || dashboard.RecentFailures[0].ID != "long-run" || dashboard.RecentFailures[1].ID != "interrupted" || dashboard.RecentFailures[2].ID != "six-days-ago" {
		t.Fatalf("unexpected recent failures: %+v", dashboard.RecentFailures)
	}
}

func TestCredentialShowsRefreshTimeBeforeExpiry(t *testing.T) {
	e := newEnv(t)
	name := "Google"
	row := models.Credential{ID: "oauth-refresh"}
	if err := e.creds.Apply(&row, credentials.Input{Name: &name, Type: "google_drive", Config: map[string]any{"clientId": "client", "clientSecret": "secret"}}, true); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if err := e.creds.SaveOAuthToken(t.Context(), row.ID, &connectors.OAuthToken{AccessToken: "access", RefreshToken: "refresh", Expiry: expiry}); err != nil {
		t.Fatal(err)
	}
	var credential struct{ OAuthRefreshAt, OAuthExpiresAt *string }
	if status := e.do("GET", "/api/credentials/"+row.ID, nil, &credential); status != 200 {
		t.Fatalf("status=%d", status)
	}
	want := expiry.Add(-oauth.SafetyMargin).Format(time.RFC3339)
	if credential.OAuthRefreshAt == nil || *credential.OAuthRefreshAt != want || credential.OAuthExpiresAt == nil || *credential.OAuthExpiresAt != expiry.Format(time.RFC3339) {
		t.Fatalf("unexpected OAuth times: %+v", credential)
	}
}

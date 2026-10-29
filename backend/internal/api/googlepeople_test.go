package api_test

import (
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/googlepeople"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestGooglePeopleAccountAndTaskLifecycle(t *testing.T) {
	e := newEnv(t)
	makeCredential := func(subject string) string {
		t.Helper()
		id := webcalAPICredential(t, e, "google_people", map[string]any{"clientId": "client", "clientSecret": "secret"})
		if err := e.creds.SaveOAuthConnection(t.Context(), id, &connectors.OAuthToken{AccessToken: "access", RefreshToken: "refresh"}, subject); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first, same, other := makeCredential("account"), makeCredential("account"), makeCredential("other")
	destination := webcalAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	var response map[string]any
	if code := e.do("POST", "/api/tasks", map[string]any{"name": "Contacts", "sourceType": "google_people", "sourceCredentialId": first, "destinationCredentialId": destination, "destinationBankId": "dest"}, &response); code != 201 {
		t.Fatal(code, response)
	}
	taskID := response["id"].(string)
	if response["cronExpression"] != "0 * * * *" {
		t.Fatal("missing hourly default", response)
	}
	if err := e.db.Model(&models.Task{}).Where("id = ?", taskID).Update("destination_locked", true).Error; err != nil {
		t.Fatal(err)
	}
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"sourceCredentialId": other}, nil); code != 422 {
		t.Fatal("account switch accepted", code)
	}
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"sourceCredentialId": same}, nil); code != 200 {
		t.Fatal("same account credential rejected", code)
	}
	if code := e.do("GET", "/api/credentials/"+first, nil, &response); code != 200 {
		t.Fatal(code)
	}
	if _, exposed := response["oauthSubject"]; exposed {
		t.Fatal("internal account subject exposed")
	}
	for _, row := range []any{
		&models.GooglePeopleIdentity{TaskID: taskID, ResourceName: "people/1", ItemID: "contact:1", Subject: "account"},
		&models.GooglePeopleScanAlias{RunID: "staging", TaskID: taskID, ResourceName: "people/1", ItemID: "contact:1", Subject: "account"},
		&models.GooglePeopleScanContact{RunID: "staging", TaskID: taskID, ItemID: "contact:1", SnapshotJSON: "{}"},
	} {
		if err := e.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if code := e.do("DELETE", "/api/tasks/"+taskID, nil, nil); code != 204 {
		t.Fatal("task deletion failed", code)
	}
	for _, model := range []any{&models.GooglePeopleIdentity{}, &models.GooglePeopleScanAlias{}, &models.GooglePeopleScanContact{}} {
		var count int64
		if err := e.db.Model(model).Where("task_id = ?", taskID).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("task deletion retained People state", count, err)
		}
	}
}

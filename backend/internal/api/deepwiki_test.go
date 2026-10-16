package api_test

import (
	"testing"

	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/deepwiki"
)

func TestDeepWikiTaskWithoutSourceCredential(t *testing.T) {
	e := newEnv(t)
	dest := sqlAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	body := map[string]any{
		"name": "Wiki", "sourceType": "deepwiki", "sourceConfig": map[string]any{"repoName": "Owner/Repo"},
		"destinationCredentialId": dest, "destinationBankId": "bank",
	}
	var out map[string]any
	if code := e.do("POST", "/api/tasks", body, &out); code != 201 {
		t.Fatalf("create without source credential: %d %+v", code, out)
	}
	if out["sourceCredentialId"] != "" || out["sourceConfig"].(map[string]any)["repoName"] != "Owner/Repo" {
		t.Fatal(out)
	}
	id := out["id"].(string)
	if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"name": "Updated Wiki"}, &out); code != 200 {
		t.Fatalf("update without source credential: %d %+v", code, out)
	}
	if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"sourceConfig": map[string]any{"repoName": "invalid"}}, &out); code != 422 {
		t.Fatalf("invalid repository: %d %+v", code, out)
	}
	// Sources that require a credential must still reject an omitted one.
	body["sourceType"] = "filesystem"
	body["sourceConfig"] = map[string]any{}
	if code := e.do("POST", "/api/tasks", body, &out); code != 422 {
		t.Fatalf("missing filesystem credential accepted: %d %+v", code, out)
	}
}

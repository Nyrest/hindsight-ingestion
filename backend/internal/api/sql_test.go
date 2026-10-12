package api_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors/sqlsource"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func sqlAPICredential(t *testing.T, e *env, typ string, cfg map[string]any) string {
	t.Helper()
	var out map[string]any
	if code := e.do("POST", "/api/credentials", map[string]any{"name": typ, "type": typ, "config": cfg}, &out); code != 201 {
		t.Fatalf("%d %+v", code, out)
	}
	return out["id"].(string)
}

func sqlAPITask(t *testing.T, e *env, source string) string {
	t.Helper()
	dest := sqlAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	var out map[string]any
	if code := e.do("POST", "/api/tasks", map[string]any{"name": "SQL", "sourceType": "sql", "sourceCredentialId": source, "sourceConfig": map[string]any{"query": `SELECT 1 AS __id,'body' AS __content`}, "destinationCredentialId": dest, "destinationBankId": "bank", "cronExpression": "0 3 * * *"}, &out); code != 201 {
		t.Fatalf("%d %+v", code, out)
	}
	if out["sourceConfig"].(map[string]any)["documentMode"] != "rows" {
		t.Fatal("row mode is not default", out)
	}
	return out["id"].(string)
}

func TestSQLPreviewIsReadOnlyAndBounded(t *testing.T) {
	e := newEnv(t)
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE articles(id INTEGER,body TEXT); INSERT INTO articles VALUES(1,'你好'),(2,'second')`); err != nil {
		t.Fatal(err)
	}
	id := sqlAPICredential(t, e, sqlsource.SQLite, map[string]any{"path": path})
	var result sqlsource.PreviewResult
	endpoint := "/api/credentials/" + id + "/sql/preview"
	if code := e.do("POST", endpoint, sqlsource.PreviewRequest{Query: `SELECT id AS __id,body AS __content FROM articles ORDER BY id`, DocumentMode: "rows"}, &result); code != 200 || result.Truncated || len(result.Documents) != 2 || result.Documents[0].Content != "你好" {
		t.Fatalf("%d %+v", code, result)
	}
	if code := e.do("POST", endpoint, sqlsource.PreviewRequest{Query: `SELECT id,body FROM articles WHERE 0`, DocumentMode: "query"}, &result); code != 200 || len(result.Documents) != 1 || !strings.Contains(result.Documents[0].Content, `"rows":[]`) {
		t.Fatalf("%d %+v", code, result)
	}
	var errResult map[string]any
	for _, q := range []string{`DELETE FROM articles`, `SELECT 1; SELECT 2`, `SELECT 1 AS __id,NULL AS __content`} {
		if code := e.do("POST", endpoint, sqlsource.PreviewRequest{Query: q, DocumentMode: "rows"}, &errResult); code != 422 {
			t.Fatalf("%s: %d %+v", q, code, errResult)
		}
	}
	q := `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<101) SELECT x AS __id,'x' AS __content FROM n`
	if code := e.do("POST", endpoint, sqlsource.PreviewRequest{Query: q, DocumentMode: "rows"}, &result); code != 200 || !result.Truncated || len(result.Documents) != 100 {
		t.Fatalf("%d %+v", code, result)
	}
	for _, m := range []any{&models.TaskItem{}, &models.TaskRun{}, &models.TaskState{}, &models.HindsightOperation{}} {
		var count int64
		e.db.Model(m).Count(&count)
		if count != 0 {
			t.Fatalf("preview wrote %T: %d", m, count)
		}
	}
	if len(e.hs.docs) != 0 {
		t.Fatal("preview wrote to Hindsight")
	}
	if code := e.do("POST", "/api/credentials/missing/sql/preview", sqlsource.PreviewRequest{Query: "SELECT 1", DocumentMode: "query"}, nil); code != 404 {
		t.Fatal(code)
	}
	other := sqlAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	if code := e.do("POST", "/api/credentials/"+other+"/sql/preview", sqlsource.PreviewRequest{Query: "SELECT 1", DocumentMode: "query"}, nil); code != 422 {
		t.Fatal(code)
	}
}

func TestSQLCredentialTargetResetsBaselineButKeepsLedger(t *testing.T) {
	for _, typ := range []string{sqlsource.MySQL, sqlsource.Postgres, sqlsource.SQLite} {
		t.Run(typ, func(t *testing.T) {
			e := newEnv(t)
			cfg := map[string]any{"host": "localhost", "port": 5432, "database": "source", "username": "reader", "password": "old", "tlsMode": "disable"}
			if typ == sqlsource.SQLite {
				cfg = map[string]any{"path": filepath.Join(t.TempDir(), "source.db")}
			}
			credID := sqlAPICredential(t, e, typ, cfg)
			taskID := sqlAPITask(t, e, credID)
			var before models.Task
			e.db.First(&before, "id = ?", taskID)
			e.db.Model(&models.TaskState{}).Where("task_id = ?", taskID).Update("committed_cursor_json", `{"inventory":true}`)
			ledger := models.TaskItem{TaskID: taskID, SourceItemID: "row:keep", DestinationDocumentID: "sql:keep", DestinationPresent: true}
			if err := e.db.Create(&ledger).Error; err != nil {
				t.Fatal(err)
			}
			if typ != sqlsource.SQLite {
				if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"config": map[string]any{"password": "new"}}, nil); code != 200 {
					t.Fatal(code)
				}
				var after models.Task
				var state models.TaskState
				e.db.First(&after, "id = ?", taskID)
				e.db.First(&state, "task_id = ?", taskID)
				if after.ConfigRevision != before.ConfigRevision || state.CommittedCursorJSON == "" {
					t.Fatal("password reset baseline")
				}
			}
			targetKeys := []string{"host", "port", "database", "username"}
			if typ == sqlsource.SQLite {
				targetKeys = []string{"path"}
			}
			for i, key := range targetKeys {
				value := any(fmt.Sprintf("changed-%d", i))
				if key == "port" {
					value = 3307
				}
				if key == "path" {
					value = filepath.Join(t.TempDir(), "other.db")
				}
				if code := e.do("PATCH", "/api/credentials/"+credID, map[string]any{"config": map[string]any{key: value}}, nil); code != 200 {
					t.Fatal(code)
				}
				var after models.Task
				var state models.TaskState
				e.db.First(&after, "id = ?", taskID)
				e.db.First(&state, "task_id = ?", taskID)
				if after.ConfigRevision != before.ConfigRevision+int64(i+1) || !after.ReconcileRequired || state.CommittedCursorJSON != "" {
					t.Fatal("connection target did not reset baseline", after, state)
				}
				var count int64
				e.db.Model(&models.TaskItem{}).Where("task_id = ?", taskID).Count(&count)
				if count != 1 {
					t.Fatal("baseline reset removed ledger")
				}
				e.db.Model(&models.TaskState{}).Where("task_id = ?", taskID).Update("committed_cursor_json", `{"inventory":true}`)
			}
		})
	}
}

func TestSQLTaskConfigAndCredentialContract(t *testing.T) {
	e := newEnv(t)
	id := sqlAPICredential(t, e, sqlsource.SQLite, map[string]any{"path": filepath.Join(t.TempDir(), "source.db")})
	taskID := sqlAPITask(t, e, id)
	var sources map[string]any
	if code := e.do("GET", "/api/connectors", nil, &sources); code != 200 {
		t.Fatal(code)
	}
	for _, entry := range sources["sources"].([]any) {
		s := entry.(map[string]any)
		if _, old := s["credentialType"]; old {
			t.Fatal("legacy credentialType exposed")
		}
		if len(s["credentialTypes"].([]any)) == 0 {
			t.Fatal(s)
		}
	}
	e.db.Model(&models.TaskState{}).Where("task_id = ?", taskID).Update("committed_cursor_json", `{"inventory":true}`)
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"sourceConfig": map[string]any{"query": "SELECT 1", "documentMode": "query"}}, nil); code != 200 {
		t.Fatal(code)
	}
	var state models.TaskState
	e.db.First(&state, "task_id = ?", taskID)
	if state.CommittedCursorJSON != "" {
		t.Fatal("mode/query change did not reset baseline")
	}
	bad := sqlAPICredential(t, e, "hindsight", map[string]any{"baseUrl": e.hsrv.URL})
	if code := e.do("PATCH", "/api/tasks/"+taskID, map[string]any{"sourceCredentialId": bad}, nil); code != 422 {
		t.Fatal(code)
	}
}

package sync_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/sqlsource"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const sqlRowsQuery = `SELECT id AS __id,body AS __content,context AS __context,extra FROM articles ORDER BY id`

func newSQLHarness(t *testing.T) (*harness, *sql.DB) {
	t.Helper()
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sqlExec(t, db, `CREATE TABLE articles(id INTEGER PRIMARY KEY,body TEXT,context TEXT,extra TEXT); INSERT INTO articles VALUES(1,'one','first','ignored'),(2,'two',NULL,'ignored')`)
	m := models.Credential{ID: uuid.NewString()}
	name := "SQLite source"
	if err = h.engine.Creds.Apply(&m, credentials.Input{Name: &name, Type: sqlsource.SQLite, Config: map[string]any{"path": path}}, true); err != nil {
		t.Fatal(err)
	}
	if err = h.db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	h.task.SourceType = sqlsource.SourceType
	h.task.SourceCredentialID = m.ID
	h.task.SourceConfigJSON = `{"documentMode":"rows","query":"` + sqlRowsQuery + `"}`
	if err = h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	return h, db
}

func sqlExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}
func sqlConfig(t *testing.T, h *harness, q, mode string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"query": q, "documentMode": mode})
	h.task.SourceConfigJSON = string(b)
	h.task.ConfigRevision++
	h.task.ReconcileRequired = true
	if err := h.db.Save(&h.task).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Model(&models.TaskState{}).Where("task_id = ?", h.task.ID).Updates(map[string]any{"committed_cursor_json": "", "last_full_reconcile_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestSQLDiffAndModes(t *testing.T) {
	h, db := newSQLHarness(t)
	r := h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.CreatedCount != 2 {
		t.Fatal(r)
	}
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.UnchangedCount != 2 || r.SyncMode != models.SyncFull {
		t.Fatal(r)
	}
	sqlExec(t, db, `UPDATE articles SET extra='changed'`)
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.UnchangedCount != 2 {
		t.Fatal(r)
	}
	sqlExec(t, db, `UPDATE articles SET context='changed context' WHERE id=1; DELETE FROM articles WHERE id=2; INSERT INTO articles VALUES(3,'three',NULL,'x')`)
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.CreatedCount != 1 || r.UpdatedCount != 1 || r.DeletedCount != 1 {
		t.Fatal(r)
	}
	found := false
	for _, doc := range h.dest.docs {
		if doc.Content == "one" {
			found = doc.Context == "changed context"
		}
	}
	if !found {
		t.Fatal("context not retained")
	}
	sqlConfig(t, h, `SELECT id,body FROM articles ORDER BY id`, sqlsource.QueryMode)
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.CreatedCount != 1 || r.DeletedCount != 2 || len(h.dest.docs) != 1 {
		t.Fatal(r)
	}
	queryID := sync.DocumentID("sql", h.task.ID, "query")
	if !strings.Contains(h.dest.docs[queryID].Content, `"rows":[["1","one"],["3","three"]]`) {
		t.Fatal(h.dest.docs)
	}
	sqlExec(t, db, `DELETE FROM articles`)
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if len(h.dest.docs) != 1 || !strings.Contains(h.dest.docs[queryID].Content, `"rows":[]`) {
		t.Fatal(r, h.dest.docs)
	}
	sqlExec(t, db, `INSERT INTO articles VALUES(4,'four',NULL,'x')`)
	sqlConfig(t, h, sqlRowsQuery, sqlsource.RowsMode)
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.CreatedCount != 1 || r.DeletedCount != 1 {
		t.Fatal(r)
	}
	sqlExec(t, db, `DELETE FROM articles`)
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.DeletedCount != 1 || len(h.dest.docs) != 0 {
		t.Fatal(r)
	}
}

func TestSQLInvalidInventoryPreservesOldDocuments(t *testing.T) {
	h, db := newSQLHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	success := h.state().LastSuccessAt
	sqlExec(t, db, `DELETE FROM articles WHERE id=2; UPDATE articles SET body='changed' WHERE id=1`)
	// Use a valid compound query to fail result validation after the first row.
	sqlConfig(t, h, `SELECT 1 AS __id,'changed' AS __content UNION ALL SELECT 1,'duplicate'`, sqlsource.RowsMode)
	r := h.run("")
	expectStatus(t, r, models.RunFailed)
	if len(h.dest.docs) != 2 || len(h.dest.deleted) != 0 || !h.state().LastSuccessAt.Equal(*success) {
		t.Fatal("failed inventory deleted documents or committed success", r)
	}
	if r.UpdatedCount != 1 {
		t.Fatal("submitted valid write was not settled", r)
	}
	if dry, err := h.engine.DryRun(t.Context(), h.task.ID); err != nil || dry.Complete || dry.DeletedCount != 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	sqlConfig(t, h, sqlRowsQuery, sqlsource.RowsMode)
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.DeletedCount != 1 {
		t.Fatal(r)
	}
}

func TestSQLHindsightFailureBlocksModeDeletion(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "submission", true: "operation"}[async], func(t *testing.T) {
			h, _ := newSQLHarness(t)
			expectStatus(t, h.run(""), models.RunSucceeded)
			sqlConfig(t, h, `SELECT id,body FROM articles ORDER BY id`, sqlsource.QueryMode)
			h.dest.failNext = !async
			h.dest.failOps = async
			r := h.run("")
			expectStatus(t, r, models.RunFailed)
			if len(h.dest.deleted) != 0 {
				t.Fatal("mode switch deleted old documents after failed write")
			}
			h.dest.failOps = false
			r = h.run("")
			expectStatus(t, r, models.RunSucceeded)
			if r.DeletedCount != 2 || len(h.dest.docs) != 1 {
				t.Fatal(r)
			}
		})
	}
}

type editingDestination struct {
	*fakeDest
	db     *gorm.DB
	taskID string
	edit   bool
}

func (d *editingDestination) GetOperation(ctx context.Context, bank, op string) (hindsight.OperationStatus, error) {
	if d.edit {
		d.edit = false
		if err := d.db.Model(&models.Task{}).Where("id = ?", d.taskID).Updates(map[string]any{"config_revision": gorm.Expr("config_revision + 1"), "reconcile_required": true}).Error; err != nil {
			return hindsight.OperationStatus{}, err
		}
		if err := d.db.Model(&models.TaskState{}).Where("task_id = ?", d.taskID).Update("committed_cursor_json", "").Error; err != nil {
			return hindsight.OperationStatus{}, err
		}
	}
	return d.fakeDest.GetOperation(ctx, bank, op)
}

func TestSQLOldRunCannotDeleteOrCommitNewConfiguration(t *testing.T) {
	h, db := newSQLHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state().LastSuccessAt
	sqlExec(t, db, `DELETE FROM articles WHERE id=2; UPDATE articles SET body='changed' WHERE id=1`)
	d := &editingDestination{fakeDest: h.dest, db: h.db, taskID: h.task.ID, edit: true}
	h.engine.NewDestination = func(connectors.Credential) (sync.Destination, error) { return d, nil }
	r := h.run("")
	expectStatus(t, r, models.RunFailed)
	if len(h.dest.deleted) != 0 || len(h.dest.docs) != 2 || h.state().CommittedCursorJSON != "" || !h.state().LastSuccessAt.Equal(*before) {
		t.Fatal("old run overwrote new configuration", r)
	}
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.DeletedCount != 1 {
		t.Fatal(r)
	}
}

func TestSQLQueryInterruptionKeepsSnapshot(t *testing.T) {
	h, db := newSQLHarness(t)
	sqlConfig(t, h, `SELECT id,body FROM articles ORDER BY id`, sqlsource.QueryMode)
	expectStatus(t, h.run(""), models.RunSucceeded)
	key := sync.DocumentID("sql", h.task.ID, "query")
	before := h.dest.docs[key].Content
	retains := h.dest.retains
	sqlExec(t, db, `UPDATE articles SET body='changed' WHERE id=1`)
	sqlConfig(t, h, `SELECT id,CASE WHEN id=2 THEN json('invalid JSON') ELSE body END AS body FROM articles ORDER BY id`, sqlsource.QueryMode)
	r := h.run("")
	expectStatus(t, r, models.RunFailed)
	if h.dest.docs[key].Content != before || h.dest.retains != retains || len(h.dest.deleted) != 0 {
		t.Fatal("incomplete snapshot was emitted", r)
	}
}

func TestSQLTasksAreIsolatedInSameBank(t *testing.T) {
	h, _ := newSQLHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	first := h.task
	second := first
	second.ID = uuid.NewString()
	second.DestinationLocked = false
	if err := h.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	h.task = second
	expectStatus(t, h.run(""), models.RunSucceeded)
	if len(h.dest.docs) != 4 {
		t.Fatal("same records across tasks collided", h.dest.docs)
	}
	h.task = first
	sqlConfig(t, h, `SELECT id AS __id,body AS __content FROM articles WHERE 0`, sqlsource.RowsMode)
	r := h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.DeletedCount != 2 || len(h.dest.docs) != 2 {
		t.Fatal(r)
	}
	var rows []models.TaskItem
	if err := h.db.Where("task_id = ?", second.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, exists := h.dest.docs[row.DestinationDocumentID]; !exists {
			t.Fatal("first task deleted second task document")
		}
	}
}

func TestSQL100000RowInventory(t *testing.T) {
	if testing.Short() {
		t.Skip("100,000-row synchronization stress test")
	}
	h, _ := newSQLHarness(t)
	sqlConfig(t, h, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100000) SELECT x AS __id,'short body' AS __content FROM n`, sqlsource.RowsMode)
	stop := make(chan struct{})
	peakResult := make(chan uint64, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		var peak uint64
		for {
			select {
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			case <-stop:
				peakResult <- peak
				return
			}
		}
	}()
	start := time.Now()
	r := h.run("")
	close(stop)
	peak := <-peakResult
	expectStatus(t, r, models.RunSucceeded)
	if r.CreatedCount != 100000 || r.DiscoveredCount != 100000 {
		t.Fatal(r)
	}
	var count int64
	if err := h.db.Model(&models.TaskItem{}).Where("task_id = ?", h.task.ID).Count(&count).Error; err != nil || count != 100000 {
		t.Fatal(count, err)
	}
	t.Logf("100,000-row full synchronization: %s, peak Go heap %.1f MiB (in-memory Hindsight test destination)", time.Since(start), float64(peak)/(1<<20))
	r = h.run("")
	expectStatus(t, r, models.RunSucceeded)
	if r.UnchangedCount != 100000 || r.UpdatedCount != 0 || r.CreatedCount != 0 {
		t.Fatal(r)
	}
}

type failSecondSQLBatch struct {
	*fakeDest
	batches int
}

func (d *failSecondSQLBatch) RetainBatch(ctx context.Context, bank string, items []hindsight.MemoryItem, op string) ([]string, error) {
	d.batches++
	if d.batches == 2 {
		d.fakeDest.failNext = true
	}
	return d.fakeDest.RetainBatch(ctx, bank, items, op)
}

func TestSQLSettlesEarlierWritesWhenFinalBatchAlsoFails(t *testing.T) {
	h, _ := newSQLHarness(t)
	expectStatus(t, h.run(""), models.RunSucceeded)
	before := h.state().LastSuccessAt
	sqlConfig(t, h, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<30) SELECT CASE WHEN x=30 THEN 3 ELSE x+2 END AS __id,'new body' AS __content FROM n`, sqlsource.RowsMode)
	dest := &failSecondSQLBatch{fakeDest: h.dest}
	h.engine.NewDestination = func(connectors.Credential) (sync.Destination, error) { return dest, nil }
	r := h.run("")
	expectStatus(t, r, models.RunFailed)
	if r.CreatedCount != 25 || len(h.dest.deleted) != 0 || !h.state().LastSuccessAt.Equal(*before) {
		t.Fatal("earlier accepted writes were not settled", r)
	}
	var confirmed int64
	if err := h.db.Model(&models.TaskItem{}).Where("task_id = ? AND destination_present = ?", h.task.ID, true).Count(&confirmed).Error; err != nil || confirmed != 27 {
		t.Fatal(confirmed, err)
	}
	if !strings.Contains(r.ErrorMessage, "duplicate") || !strings.Contains(r.ErrorMessage, "hindsight down") {
		t.Fatal("source/submission failures were lost", r.ErrorMessage)
	}
}

package database_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
)

// TestDialects verifies migrations and the query patterns used by the
// engine behave identically on every supported database. PostgreSQL and
// MySQL run when TEST_POSTGRES_DSN / TEST_MYSQL_DSN are set.
func TestDialects(t *testing.T) {
	cases := map[string]string{"sqlite": filepath.Join(t.TempDir(), "d.db")}
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		cases["postgres"] = dsn
	}
	if dsn := os.Getenv("TEST_MYSQL_DSN"); dsn != "" {
		cases["mysql"] = dsn
	}
	for typ, dsn := range cases {
		t.Run(typ, func(t *testing.T) {
			db, err := database.Open(typ, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s, _ := db.DB(); s.Close() }()
			// Migrations are idempotent.
			if err := database.Migrate(db); err != nil {
				t.Fatal(err)
			}
			exercise(t, db)
		})
	}
}

func exercise(t *testing.T, db *gorm.DB) {
	taskID := "task-" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		db.Where("task_id = ?", taskID).Delete(&models.TaskItem{})
		db.Where("task_id = ?", taskID).Delete(&models.TaskState{})
		db.Where("task_id = ?", taskID).Delete(&models.TaskRun{})
	})

	// Upsert on composite key (ledger batching).
	rows := []models.TaskItem{
		{TaskID: taskID, SourceItemID: "a", SourceRevision: "1", LastSeenGeneration: 1, DestinationPresent: true},
		{TaskID: taskID, SourceItemID: "b", SourceRevision: "1", LastSeenGeneration: 1},
	}
	upsert := func(rows []models.TaskItem) {
		if err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "task_id"}, {Name: "source_item_id"}}, UpdateAll: true,
		}).CreateInBatches(rows, 100).Error; err != nil {
			t.Fatal(err)
		}
	}
	upsert(rows)
	rows[0].SourceRevision = "2"
	rows[0].LastSeenGeneration = 2
	upsert(rows[:1])

	var a models.TaskItem
	if err := db.First(&a, "task_id = ? AND source_item_id = ?", taskID, "a").Error; err != nil {
		t.Fatal(err)
	}
	if a.SourceRevision != "2" || a.LastSeenGeneration != 2 || !a.DestinationPresent {
		t.Fatalf("upsert result: %+v", a)
	}
	var stale []models.TaskItem
	db.Where("task_id = ? AND last_seen_generation < ? AND destination_present = ?", taskID, 2, false).Find(&stale)
	if len(stale) != 1 || stale[0].SourceItemID != "b" {
		t.Fatalf("generation query: %+v", stale)
	}

	// FirstOrCreate + map updates incl. NULL times.
	var st models.TaskState
	if err := db.Where(models.TaskState{TaskID: taskID}).FirstOrCreate(&st).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	db.Model(&models.TaskState{}).Where("task_id = ?", taskID).Updates(map[string]any{"committed_cursor_json": `{"x":1}`, "last_success_at": now})
	db.Model(&models.TaskState{}).Where("task_id = ?", taskID).Updates(map[string]any{"last_full_reconcile_at": nil})
	db.First(&st, "task_id = ?", taskID)
	if st.CommittedCursorJSON != `{"x":1}` || st.LastSuccessAt == nil || !st.LastSuccessAt.Equal(now) {
		t.Fatalf("state: %+v", st)
	}

	// IN queries and counting (run guard).
	db.Create(&models.TaskRun{ID: taskID + "-r", TaskID: taskID, TriggerType: "manual", Status: models.RunRunning, SyncMode: "full", StartedAt: &now})
	var n int64
	db.Model(&models.TaskRun{}).Where("task_id = ? AND status IN ?", taskID, []string{models.RunRunning, models.RunPending}).Count(&n)
	if n != 1 {
		t.Fatalf("count = %d", n)
	}

	// Settings upsert (reserved word "key").
	cipher, _ := crypto.New(make([]byte, 32))
	store := settings.NewStore(db, cipher)
	v := settings.Defaults
	v.MaxFileSizeMB = 7
	if err := store.Put(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	v.MaxFileSizeMB = 9
	if err := store.Put(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context())
	if err != nil || got.MaxFileSizeMB != 9 {
		t.Fatalf("settings: %+v %v", got, err)
	}
}

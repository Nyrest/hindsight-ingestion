package database_test

import (
	"fmt"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/email"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestEmailSchemaUpgradeAndStateCommit(t *testing.T) {
	cases := map[string]string{"sqlite": filepath.Join(t.TempDir(), "email-upgrade.db")}
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		cases["postgres"] = dsn
	}
	if dsn := os.Getenv("TEST_MYSQL_DSN"); dsn != "" {
		cases["mysql"] = dsn
	}
	for dialect, dsn := range cases {
		t.Run(dialect, func(t *testing.T) {
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			db, err := database.Open(dialect, dsn, log)
			if err != nil {
				t.Fatal(err)
			}
			task, run := uuid.NewString(), uuid.NewString()
			if err := db.Create(&models.TaskState{TaskID: task, CommittedCursorJSON: "old"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&models.TaskItem{TaskID: task, SourceItemID: "existing", DestinationPresent: true, SyncedFingerprint: "confirmed"}).Error; err != nil {
				t.Fatal(err)
			}
			// Recreate the pre-email schema in this disposable database.
			for _, model := range []any{&models.EmailScanOccurrence{}, &models.EmailScanIdentity{}, &models.EmailOccurrence{}, &models.EmailIdentity{}} {
				if err := db.Migrator().DropTable(model); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Migrator().DropColumn(&models.TaskState{}, "PendingScanRunID"); err != nil {
				t.Fatal(err)
			}
			sql, _ := db.DB()
			sql.Close()
			db, err = database.Open(dialect, dsn, log)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { sql, _ := db.DB(); sql.Close() }()
			if err := database.Migrate(db); err != nil {
				t.Fatal(err)
			}
			var state models.TaskState
			var item models.TaskItem
			if err := db.First(&state, "task_id = ?", task).Error; err != nil || state.CommittedCursorJSON != "old" || state.PendingScanRunID != "" {
				t.Fatal("migration changed prior cursor", state, err)
			}
			if err := db.First(&item, "task_id = ?", task).Error; err != nil || item.SyncedFingerprint != "confirmed" || !item.DestinationPresent {
				t.Fatal("migration changed prior ledger", err)
			}
			if err := db.Create(&models.TaskRun{ID: run, TaskID: task, SyncMode: models.SyncFull}).Error; err != nil {
				t.Fatal(err)
			}
			identities := []models.EmailScanIdentity{}
			occurrences := []models.EmailScanOccurrence{}
			for i := 0; i < 205; i++ {
				id := fmt.Sprintf("mail-%03d", i)
				identities = append(identities, models.EmailScanIdentity{RunID: run, TaskID: task, ItemID: id, AccountKey: "account", SnapshotJSON: "{}"})
				occurrences = append(occurrences, models.EmailScanOccurrence{RunID: run, TaskID: task, ItemID: id, AccountKey: "account", FolderKey: "folder", UIDValidity: 1, UID: uint64(i + 1), FolderName: "INBOX", PartsJSON: "[]"})
			}
			if err := db.CreateInBatches(identities, 100).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.CreateInBatches(occurrences, 100).Error; err != nil {
				t.Fatal(err)
			}
			connector := email.Connector{DB: db}
			if err := db.Transaction(func(tx *gorm.DB) error {
				if err := connector.CommitScan(t.Context(), run, tx); err != nil {
					return err
				}
				return tx.Model(&models.TaskState{}).Where("task_id = ?", task).Updates(map[string]any{"committed_cursor_json": "new", "pending_scan_run_id": ""}).Error
			}); err != nil {
				t.Fatal(err)
			}
			for _, model := range []any{&models.EmailIdentity{}, &models.EmailOccurrence{}} {
				var count int64
				if err := db.Model(model).Where("task_id = ?", task).Count(&count).Error; err != nil || count != 205 {
					t.Fatal("commit lost composite-key page", count, err)
				}
			}
			for _, model := range []any{&models.EmailScanIdentity{}, &models.EmailScanOccurrence{}} {
				var count int64
				db.Model(model).Where("run_id = ?", run).Count(&count)
				if count != 0 {
					t.Fatal("commit retained staging")
				}
			}
			// Empty full inventories remove all mappings without NOT IN NULL mistakes.
			if err := db.Transaction(func(tx *gorm.DB) error { return connector.CommitScan(t.Context(), run, tx) }); err != nil {
				t.Fatal(err)
			}
			var count int64
			db.Model(&models.EmailIdentity{}).Where("task_id = ?", task).Count(&count)
			if count != 0 {
				t.Fatal("empty inventory retained identities")
			}
			for _, model := range []any{&models.TaskItem{}, &models.TaskState{}, &models.TaskRun{}} {
				db.Where("task_id = ?", task).Delete(model)
			}
		})
	}
}

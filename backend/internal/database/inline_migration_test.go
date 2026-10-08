package database_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
)

// These DSNs must point to disposable test databases, as in TestDialects.
func TestInlineSchemaUpgradeAndRestart(t *testing.T) {
	cases := map[string]string{"sqlite": filepath.Join(t.TempDir(), "upgrade.db")}
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		cases["postgres"] = dsn
	}
	if dsn := os.Getenv("TEST_MYSQL_DSN"); dsn != "" {
		cases["mysql"] = dsn
	}
	for dialect, dsn := range cases {
		t.Run(dialect, func(t *testing.T) {
			cipher, _ := crypto.New(make([]byte, 32))
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			db, err := database.Open(dialect, dsn, log)
			if err != nil {
				t.Fatal(err)
			}
			task := models.Task{ID: uuid.NewString(), Name: "existing", FilePolicyMode: "override", FilePolicyJSON: `{"images":true,"audios":true}`}
			if err = db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
			row := models.TaskItem{TaskID: task.ID, SourceItemID: "note", SyncedFingerprint: "existing", DestinationPresent: true}
			if err = db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			// Reconstruct the previous schema without altering the stored task/ledger data.
			for _, column := range []string{"InlineMultimodalMode", "InlineMultimodalEnabled"} {
				if err = db.Migrator().DropColumn(&models.Task{}, column); err != nil {
					t.Fatal(err)
				}
			}
			if err = db.Migrator().DropColumn(&models.TaskItem{}, "NeedsImageRetry"); err != nil {
				t.Fatal(err)
			}
			if err = db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&models.Setting{Key: "global", Value: `{"maxFileSizeMB":10,"filePolicy":{"images":true}}`}).Error; err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			sqlDB.Close()
			for i := 0; i < 2; i++ {
				db, err = database.Open(dialect, dsn, log)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.First(&task, "id = ?", task.ID).Error; err != nil {
					t.Fatal(err)
				}
				if err = db.First(&row, "task_id = ? AND source_item_id = ?", task.ID, "note").Error; err != nil {
					t.Fatal(err)
				}
				if task.InlineMultimodalMode != "global" || task.InlineMultimodalEnabled || task.FilePolicyJSON != `{"images":true,"audios":true}` || row.NeedsImageRetry || row.SyncedFingerprint != "existing" || !row.DestinationPresent {
					t.Fatalf("migration data: %+v %+v", task, row)
				}
				v, err := settings.NewStore(db, cipher).Get(t.Context())
				if err != nil || v.InlineMultimodalEnabled {
					t.Fatalf("legacy global defaults: %+v %v", v, err)
				}
				if i == 1 {
					db.Where("task_id = ?", task.ID).Delete(&models.TaskItem{})
					db.Delete(&task)
				}
				sqlDB, _ = db.DB()
				sqlDB.Close()
			}
		})
	}
}

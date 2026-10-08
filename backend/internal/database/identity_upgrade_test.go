package database_test

import (
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestLedgerIdentitySchemaUpgrade(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.Exec(`CREATE TABLE task_items (
		task_id TEXT, source_item_id TEXT, destination_document_id VARCHAR(128),
		PRIMARY KEY (task_id, source_item_id))`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO task_items VALUES (?, ?, ?)`, "task", "item", "hi_legacy").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := database.Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	var row models.TaskItem
	if err := db.First(&row, "task_id = ? AND source_item_id = ?", "task", "item").Error; err != nil || row.DestinationDocumentID != "hi_legacy" {
		t.Fatalf("upgrade lost legacy identity: %+v %v", row, err)
	}
	row.PreviousDocumentID = row.DestinationDocumentID
	row.DestinationDocumentID = "s3:storage.example:bucket:" + strings.Repeat("long/path/", 100) + "file.txt"
	if err := db.Save(&row).Error; err != nil {
		t.Fatal(err)
	}
	var loaded models.TaskItem
	if err := db.First(&loaded, "task_id = ? AND source_item_id = ?", "task", "item").Error; err != nil || loaded.DestinationDocumentID != row.DestinationDocumentID || loaded.PreviousDocumentID != "hi_legacy" {
		t.Fatalf("upgrade cannot store canonical identities: %+v %v", loaded, err)
	}
}

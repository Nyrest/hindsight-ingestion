package email

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"gorm.io/gorm"
)

func TestCommitScanCompositeKeyPages(t *testing.T) {
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "staging.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.Create(&models.TaskRun{ID: "run", TaskID: "task", SyncMode: models.SyncFull}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 301; i++ {
		id := fmt.Sprintf("item-%04d", i)
		identity := models.EmailScanIdentity{RunID: "run", TaskID: "task", ItemID: id, AccountKey: "account", SnapshotJSON: "{}"}
		occurrence := models.EmailScanOccurrence{RunID: "run", TaskID: "task", AccountKey: fmt.Sprintf("a-%d", i/150), FolderKey: fmt.Sprintf("f-%d", i/75%2), UIDValidity: uint64(i/25%3 + 1), UID: uint64(i%25 + 1), ItemID: id, PartsJSON: "[]"}
		for _, row := range []any{&identity, &occurrence} {
			if err := db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	sentinel := models.EmailScanOccurrence{RunID: "other-run", TaskID: "other-task", AccountKey: "z", FolderKey: "z", UIDValidity: 1, UID: 1, ItemID: "other"}
	if err := db.Create(&sentinel).Error; err != nil {
		t.Fatal(err)
	}
	connector := &Connector{DB: db}
	if err := db.Transaction(func(tx *gorm.DB) error { return connector.CommitScan(t.Context(), "run", tx) }); err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{&models.EmailIdentity{}, &models.EmailOccurrence{}} {
		var count int64
		if err := db.Model(model).Where("task_id = ?", "task").Count(&count).Error; err != nil || count != 301 {
			t.Fatal("keyset lost or duplicated rows", count, err)
		}
	}
	var foreign int64
	if err := db.Model(&models.EmailOccurrence{}).Where("task_id = ?", "other-task").Count(&foreign).Error; err != nil || foreign != 0 {
		t.Fatal("pagination published another run", foreign, err)
	}
	var staged int64
	db.Model(&models.EmailScanOccurrence{}).Where("run_id = ?", "run").Count(&staged)
	if staged != 0 {
		t.Fatal("committed staging not cleared")
	}
	db.Model(&models.EmailScanOccurrence{}).Where("run_id = ?", "other-run").Count(&staged)
	if staged != 1 {
		t.Fatal("pagination crossed run boundary")
	}
}

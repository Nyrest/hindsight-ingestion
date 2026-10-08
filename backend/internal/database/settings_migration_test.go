package database_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
)

func TestProxyAndScopeUpgrade(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dsn := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := database.Open("sqlite", dsn, log)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{&models.Credential{ID: "legacy", Name: "legacy", Type: "hindsight"}, &models.Task{ID: "task", Name: "legacy"}, &models.TaskItem{TaskID: "task", SourceItemID: "page", SyncedFingerprint: "existing"}, &models.Setting{Key: "global", Value: `{"maxFileSizeMB":10}`}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []struct {
		model  any
		fields []string
	}{{&models.Credential{}, []string{"ProxyMode", "ProxyJSON"}}, {&models.Task{}, []string{"ObservationScopeMode", "ObservationScopeJSON"}}, {&models.Setting{}, []string{"EncryptedSecret"}}} {
		for _, field := range entry.fields {
			if err := db.Migrator().DropColumn(entry.model, field); err != nil {
				t.Fatal(err)
			}
		}
	}
	sqlDB, _ := db.DB()
	sqlDB.Close()
	cipher, _ := crypto.New(make([]byte, 32))
	for range 2 {
		db, err = database.Open("sqlite", dsn, log)
		if err != nil {
			t.Fatal(err)
		}
		var cred models.Credential
		var task models.Task
		var item models.TaskItem
		db.First(&cred, "id = ?", "legacy")
		db.First(&task, "id = ?", "task")
		db.First(&item, "task_id = ?", "task")
		if cred.ProxyMode != "global" || task.ObservationScopeMode != "global" || item.SyncedFingerprint != "existing" {
			t.Fatal("legacy rows changed", cred, task, item)
		}
		cfg, err := settings.NewStore(db, cipher).Get(t.Context())
		if err != nil || cfg.Proxy.Type != "default" || cfg.ObservationScope.Rule != "combined" || cfg.MaxFileSizeMB != 10 {
			t.Fatal(cfg.Masked(), err)
		}
		sqlDB, _ = db.DB()
		sqlDB.Close()
	}
}

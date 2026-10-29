package database_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/google/uuid"
)

func TestGooglePeopleSchemaUpgradePreservesExistingState(t *testing.T) {
	cases := map[string]string{"sqlite": filepath.Join(t.TempDir(), "people-upgrade.db")}
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		cases["postgres"] = dsn
	}
	if dsn := os.Getenv("TEST_MYSQL_DSN"); dsn != "" {
		cases["mysql"] = dsn
	}
	for dialect, dsn := range cases {
		t.Run(dialect, func(t *testing.T) {
			db, err := database.Open(dialect, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { sql, _ := db.DB(); sql.Close() }()
			id := uuid.NewString()
			if err := db.Create(&models.Credential{ID: id, Type: "google_drive"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&models.TaskState{TaskID: id, CommittedCursorJSON: "unchanged"}).Error; err != nil {
				t.Fatal(err)
			}
			for _, model := range []any{&models.GooglePeopleScanContact{}, &models.GooglePeopleScanAlias{}, &models.GooglePeopleIdentity{}} {
				if err := db.Migrator().DropTable(model); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Migrator().DropColumn(&models.Credential{}, "OAuthSubject"); err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(db); err != nil {
				t.Fatal(err)
			}
			var credential models.Credential
			var state models.TaskState
			if err := db.First(&credential, "id = ?", id).Error; err != nil || credential.Type != "google_drive" || credential.OAuthSubject != "" {
				t.Fatal("migration changed credential", credential, err)
			}
			if err := db.First(&state, "task_id = ?", id).Error; err != nil || state.CommittedCursorJSON != "unchanged" {
				t.Fatal("migration changed cursor", state, err)
			}
			for _, model := range []any{&models.GooglePeopleScanContact{}, &models.GooglePeopleScanAlias{}, &models.GooglePeopleIdentity{}} {
				if !db.Migrator().HasTable(model) {
					t.Fatal("missing People table", model)
				}
			}
		})
	}
}

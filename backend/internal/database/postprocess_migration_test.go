package database_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm/schema"

	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/postprocess"
)

func TestPostProcessingMySQLColumnCapacity(t *testing.T) {
	s, err := schema.Parse(&models.Task{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	if typ := (mysql.Dialector{Config: &mysql.Config{}}).DataTypeOf(s.FieldsByName["PostProcessJSON"]); typ != "longtext" {
		t.Fatalf("post-processing column is %s, want longtext", typ)
	}
}

func TestPostProcessingLargeConfigMigration(t *testing.T) {
	cfg, err := (postprocess.Pipeline{Enabled: true, Mode: postprocess.ModeCode,
		Code: "/*" + strings.Repeat("<", postprocess.MaxCodeSize-64) + "*/\nfunction process(c, input) { return input; }"}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 65535 {
		t.Fatal("fixture does not exceed MySQL TEXT capacity")
	}
	cases := map[string]string{"sqlite": filepath.Join(t.TempDir(), "postprocess.db")}
	for typ, name := range map[string]string{"mysql": "TEST_MYSQL_DSN", "postgres": "TEST_POSTGRES_DSN"} {
		if dsn := os.Getenv(name); dsn != "" {
			cases[typ] = dsn
		}
	}
	for typ, dsn := range cases {
		t.Run(typ, func(t *testing.T) {
			db, err := database.Open(typ, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
			if typ == "mysql" {
				if err := db.Exec("ALTER TABLE tasks MODIFY post_process_json TEXT").Error; err != nil {
					t.Fatal(err)
				}
			}
			task := models.Task{ID: uuid.NewString(), PostProcessJSON: `{"enabled":false,"mode":"ui","steps":[],"code":""}`}
			if err := db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Delete(&models.Task{}, "id = ?", task.ID) })
			for range 2 {
				if err := database.Migrate(db); err != nil {
					t.Fatal(err)
				}
			}
			var before models.Task
			if err := db.First(&before, "id = ?", task.ID).Error; err != nil || before.PostProcessJSON != task.PostProcessJSON {
				t.Fatal("migration changed existing configuration", err)
			}
			if err := db.Model(&task).Update("post_process_json", string(raw)).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(db); err != nil {
				t.Fatal(err)
			}
			var after models.Task
			if err := db.First(&after, "id = ?", task.ID).Error; err != nil || after.PostProcessJSON != string(raw) {
				t.Fatal("large configuration did not survive saving and migration", err)
			}
		})
	}
}

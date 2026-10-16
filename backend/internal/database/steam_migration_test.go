package database_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestSteamLargePayloadMigration(t *testing.T) {
	cases := map[string]string{"sqlite": filepath.Join(t.TempDir(), "steam.db")}
	for typ, env := range map[string]string{"mysql": "TEST_MYSQL_DSN", "postgres": "TEST_POSTGRES_DSN"} {
		if dsn := os.Getenv(env); dsn != "" {
			cases[typ] = dsn
		}
	}
	for typ, dsn := range cases {
		t.Run(typ, func(t *testing.T) {
			db, err := database.Open(typ, dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sqlDB, _ := db.DB(); sqlDB.Close() })
			columns := []struct{ table, column string }{
				{"steam_schemas", "payload_json"}, {"steam_player_snapshots", "payload_json"},
				{"task_states", "committed_cursor_json"}, {"task_runs", "cursor_before"}, {"task_runs", "cursor_after"},
			}
			// Recreate the previous MySQL column types before migrating persisted rows.
			if typ == "mysql" {
				for _, col := range columns {
					if err := db.Exec(fmt.Sprintf("ALTER TABLE %s MODIFY %s TEXT", col.table, col.column)).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			id := "steam-" + time.Now().Format("150405.000000000")
			schema := models.SteamSchema{AppID: 620, Language: id, PayloadJSON: `{"legacy":true}`}
			player := models.SteamPlayerSnapshot{TaskID: id, CredentialKey: "test", SteamID: "76561198188596121", AppID: 620, Language: id, PayloadJSON: schema.PayloadJSON}
			state := models.TaskState{TaskID: id, CommittedCursorJSON: `{"version":1,"games":{"620":"legacy"}}`}
			run := models.TaskRun{ID: id, TaskID: id, CursorBefore: state.CommittedCursorJSON, CursorAfter: state.CommittedCursorJSON}
			t.Cleanup(func() {
				db.Where("language = ?", id).Delete(&models.SteamSchema{})
				db.Where("task_id = ?", id).Delete(&models.SteamPlayerSnapshot{})
				db.Where("task_id = ?", id).Delete(&models.TaskState{})
				db.Where("task_id = ?", id).Delete(&models.TaskRun{})
			})
			for _, row := range []any{&schema, &player, &state, &run} {
				if err := db.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := database.Migrate(db); err != nil {
					t.Fatal(err)
				}
			}
			check := func(payload, cur string) {
				t.Helper()
				var gotSchema models.SteamSchema
				var gotPlayer models.SteamPlayerSnapshot
				var gotState models.TaskState
				var gotRun models.TaskRun
				for _, query := range []struct {
					row any
					key string
				}{{&gotSchema, "language"}, {&gotPlayer, "task_id"}, {&gotState, "task_id"}, {&gotRun, "id"}} {
					if err := db.First(query.row, query.key+" = ?", id).Error; err != nil {
						t.Fatal(err)
					}
				}
				if gotSchema.PayloadJSON != payload || gotPlayer.PayloadJSON != payload || gotState.CommittedCursorJSON != cur || gotRun.CursorBefore != cur || gotRun.CursorAfter != cur {
					t.Fatal("migration or upsert changed persisted payload/cursor")
				}
			}
			check(schema.PayloadJSON, state.CommittedCursorJSON)
			if typ == "mysql" {
				for _, col := range columns {
					var dataType string
					if err := db.Raw("SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?", col.table, col.column).Scan(&dataType).Error; err != nil || dataType != "longtext" {
						t.Fatalf("%s.%s type=%s: %v", col.table, col.column, dataType, err)
					}
				}
			}
			// Realistic achievement data and a 10,000-game cursor both exceed TEXT capacity.
			achievementData := make([]map[string]string, 200)
			for i := range achievementData {
				achievementData[i] = map[string]string{"name": fmt.Sprintf("achievement_%d", i), "description": strings.Repeat("成就描述", 80)}
			}
			payload, _ := json.Marshal(achievementData)
			games := make([]uint32, 10000)
			for i := range games {
				games[i] = 1000000 + uint32(i)
			}
			cur, _ := json.Marshal(map[string]any{"version": 2, "games": games})
			if len(payload) <= 65535 || len(cur) <= 65535 {
				t.Fatal("fixtures do not exceed MySQL TEXT capacity")
			}
			schema.PayloadJSON, player.PayloadJSON = string(payload), string(payload)
			state.CommittedCursorJSON = string(cur)
			run.CursorBefore, run.CursorAfter = string(cur), string(cur)
			for _, row := range []any{&schema, &player, &state, &run} {
				if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Migrate(db); err != nil {
				t.Fatal(err)
			}
			check(string(payload), string(cur))
		})
	}
}

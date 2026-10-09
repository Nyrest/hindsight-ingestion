package api_test

import (
	"testing"
	"time"
)

func TestGlobalTimezoneReschedulesAllTasks(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	e := newEnv(t)
	var global map[string]any
	if code := e.do("GET", "/api/settings", nil, &global); code != 200 || global["timezone"] != "Asia/Tokyo" {
		t.Fatal(code, global)
	}
	ids := []string{makeTask(t, e, e.hsrv.URL, "filesystem"), makeTask(t, e, e.hsrv.URL, "filesystem")}
	// Legacy database columns are intentionally ignored after the upgrade.
	if err := e.db.Exec("ALTER TABLE tasks ADD COLUMN cron_timezone TEXT NOT NULL DEFAULT 'UTC'").Error; err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if err := e.db.Exec("UPDATE tasks SET cron_timezone = ? WHERE id = ?", []string{"UTC", "Europe/Berlin"}[i], id).Error; err != nil {
			t.Fatal(err)
		}
	}
	ids = append(ids, makeTask(t, e, e.hsrv.URL, "filesystem"))
	for _, zone := range []string{"Asia/Tokyo", "America/New_York", "UTC"} {
		if code := e.do("PATCH", "/api/settings", map[string]any{"timezone": zone}, &global); code != 200 {
			t.Fatal(code, global)
		}
		location, _ := time.LoadLocation(zone)
		var preview map[string]any
		if code := e.do("POST", "/api/tasks/validate-cron", map[string]any{"cronExpression": "0 3 * * *", "cronTimezone": "Europe/Berlin"}, &preview); code != 200 || preview["valid"] != true {
			t.Fatal(code, preview)
		}
		for _, value := range preview["nextRuns"].([]any) {
			next, err := time.Parse(time.RFC3339, value.(string))
			if err != nil || next.In(location).Hour() != 3 {
				t.Fatal(zone, next, err)
			}
		}
		for _, id := range ids {
			var task map[string]any
			if code := e.do("GET", "/api/tasks/"+id, nil, &task); code != 200 {
				t.Fatal(code, task)
			}
			if _, exists := task["cronTimezone"]; exists {
				t.Fatal("task timezone still exposed")
			}
			next, err := time.Parse(time.RFC3339, task["nextRunAt"].(string))
			if err != nil || next.In(location).Hour() != 3 {
				t.Fatal(zone, next, err)
			}
			if task["nextRunAt"] != preview["nextRuns"].([]any)[0] {
				t.Fatal("preview differs from scheduler", task, preview)
			}
		}
	}
	var invalid map[string]any
	if code := e.do("PATCH", "/api/settings", map[string]any{"timezone": "Mars/Unknown"}, &invalid); code != 422 {
		t.Fatal(code, invalid)
	}
	if invalid["fields"].(map[string]any)["timezone"] != "settings.timezone.invalid" {
		t.Fatal("missing localized field error", invalid)
	}
	if code := e.do("GET", "/api/settings", nil, &global); code != 200 || global["timezone"] != "UTC" {
		t.Fatal("invalid setting persisted", code, global)
	}
}

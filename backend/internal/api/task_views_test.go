package api_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

type queryCounter struct {
	logger.Interface
	queries *atomic.Int64
}

func (counter queryCounter) Trace(ctx context.Context, begin time.Time, query func() (string, int64), err error) {
	counter.queries.Add(1)
	counter.Interface.Trace(ctx, begin, query, err)
}

func TestTaskListBatchesSummariesAndCounts(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	for i := range 12 {
		taskID := fmt.Sprintf("task-%02d", i)
		for _, row := range []any{
			&models.Task{ID: taskID, Name: taskID, SourceType: "filesystem"},
			&models.TaskState{TaskID: taskID, CommittedCursorJSON: `{"cursor":1}`, LastSuccessAt: &now},
			&models.TaskItem{TaskID: taskID, SourceItemID: "present", DestinationPresent: true},
			&models.TaskItem{TaskID: taskID, SourceItemID: "absent", DestinationPresent: false},
			&models.TaskRun{ID: taskID + "-a", TaskID: taskID, StartedAt: &now, Status: models.RunFailed},
			&models.TaskRun{ID: taskID + "-b", TaskID: taskID, StartedAt: &now, Status: models.RunSucceeded},
		} {
			if err := e.db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	var queries atomic.Int64
	e.db.Logger = queryCounter{Interface: e.db.Logger, queries: &queries}
	var tasks []struct {
		ID        string
		ItemCount int64
		LastRun   struct{ ID, Status string }
		State     struct{ HasCursor bool }
	}
	if status := e.do("GET", "/api/tasks", nil, &tasks); status != 200 {
		t.Fatalf("list status=%d", status)
	}
	if len(tasks) != 12 || queries.Load() != 4 {
		t.Fatalf("tasks=%d, queries=%d; expected 12 tasks in four queries", len(tasks), queries.Load())
	}
	for _, task := range tasks {
		if task.ItemCount != 1 || !task.State.HasCursor || task.LastRun.ID != task.ID+"-b" || task.LastRun.Status != models.RunSucceeded {
			t.Fatalf("incorrect summary: %+v", task)
		}
	}
}

func TestReadEndpointsRejectDatabaseFailures(t *testing.T) {
	e := newEnv(t)
	if err := e.db.Migrator().DropTable(&models.TaskState{}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Create(&models.Task{ID: "task", Name: "task"}).Error; err != nil {
		t.Fatal(err)
	}
	if status := e.do("GET", "/api/tasks", nil, nil); status != 500 {
		t.Fatalf("state query failure returned %d", status)
	}
	if err := e.db.Migrator().DropTable(&models.Task{}); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/dashboard", "/api/runs"} {
		if status := e.do("GET", endpoint, nil, nil); status != 500 {
			t.Fatalf("%s database failure returned %d", endpoint, status)
		}
	}
}

func TestDashboardCountsIncludeAllRecentRuns(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	old := now.Add(-25 * time.Hour)
	for i, status := range []string{models.RunSucceeded, models.RunFailed, models.RunInterrupted, models.RunFailed} {
		started := &now
		if i == 3 {
			started = &old
		}
		if err := e.db.Create(&models.TaskRun{ID: fmt.Sprint(i), TaskID: "task", Status: status, StartedAt: started}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var dashboard struct {
		Totals struct{ Runs24h, Failed24h int64 }
	}
	if status := e.do("GET", "/api/dashboard", nil, &dashboard); status != 200 {
		t.Fatalf("dashboard status=%d", status)
	}
	if dashboard.Totals.Runs24h != 3 || dashboard.Totals.Failed24h != 2 {
		t.Fatalf("incorrect dashboard totals: %+v", dashboard.Totals)
	}
}

func TestHealthReturnsUnavailableForClosedDatabase(t *testing.T) {
	e := newEnv(t)
	db, err := e.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var health struct{ Status string }
	if status := e.do("GET", "/api/health", nil, &health); status != 503 || health.Status != "degraded" {
		t.Fatalf("health status=%d, body=%+v", status, health)
	}
}

func TestLatestRunUsesOrderedIndex(t *testing.T) {
	e := newEnv(t)
	var plan []struct{ Detail string }
	if err := e.db.Raw("EXPLAIN QUERY PLAN SELECT id FROM task_runs WHERE task_id = ? ORDER BY started_at DESC, id DESC LIMIT 1", "task").Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	usesIndex := false
	for _, step := range plan {
		usesIndex = usesIndex || strings.Contains(step.Detail, "idx_task_run_latest")
		if strings.Contains(step.Detail, "TEMP B-TREE") {
			t.Fatalf("latest-run lookup still sorts history: %s", step.Detail)
		}
	}
	if !usesIndex {
		t.Fatalf("latest-run lookup ignored the ordered index: %+v", plan)
	}
}

func TestTaskCreateRejectsCredentialRemovedAfterValidation(t *testing.T) {
	e := newEnv(t)
	for _, row := range []models.Credential{{ID: "source", Type: "filesystem"}, {ID: "destination", Type: "hindsight"}} {
		if err := e.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	var removed atomic.Bool
	if err := e.db.Callback().Query().After("gorm:query").Register("test:remove_validated_credential", func(tx *gorm.DB) {
		if tx.Statement.Table != "credentials" || len(tx.Statement.Vars) == 0 || tx.Statement.Vars[0] != "destination" || !removed.CompareAndSwap(false, true) {
			return
		}
		if err := e.db.Delete(&models.Credential{}, "id = ?", "destination").Error; err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	status := e.do("POST", "/api/tasks", map[string]any{"name": "task", "sourceType": "filesystem",
		"sourceCredentialId": "source", "sourceConfig": map[string]any{"folder": "."},
		"destinationCredentialId": "destination", "destinationBankId": "bank"}, nil)
	var tasks int64
	if err := e.db.Model(&models.Task{}).Count(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if !removed.Load() || status != 409 || tasks != 0 {
		t.Fatalf("credential removal left a dangling task: removed=%v, status=%d, tasks=%d", removed.Load(), status, tasks)
	}
}

func TestTaskPatchRejectsConcurrentRevisionChangeForInventorySource(t *testing.T) {
	e := newEnv(t)
	for _, row := range []any{&models.Credential{ID: "source", Type: "filesystem"}, &models.Credential{ID: "destination", Type: "hindsight"},
		&models.Task{ID: "task", Name: "original", SourceType: "filesystem", SourceCredentialID: "source", DestinationCredentialID: "destination", DestinationBankID: "bank", ConfigRevision: 1}} {
		if err := e.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	var changed atomic.Bool
	if err := e.db.Callback().Query().After("gorm:query").Register("test:change_task_revision", func(tx *gorm.DB) {
		if tx.Statement.Table != "tasks" || !changed.CompareAndSwap(false, true) {
			return
		}
		if err := e.db.Model(&models.Task{}).Where("id = ?", "task").Updates(map[string]any{"config_revision": 2, "reconcile_required": true}).Error; err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	status := e.do("PATCH", "/api/tasks/task", map[string]any{"name": "new"}, nil)
	var task models.Task
	if err := e.db.First(&task, "id = ?", "task").Error; err != nil {
		t.Fatal(err)
	}
	if status != 409 || task.Name != "original" || task.ConfigRevision != 2 || !task.ReconcileRequired {
		t.Fatalf("patch overwrote a newer task revision: status=%d, task=%+v", status, task)
	}
}

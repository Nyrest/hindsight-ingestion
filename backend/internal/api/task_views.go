package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func (s *Server) writeTask(w http.ResponseWriter, r *http.Request, task *models.Task, status int) {
	views, err := s.taskViews(r.Context(), []models.Task{*task})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, status, views[0])
}

type taskCount struct {
	TaskID    string
	ItemCount int64
}

func (s *Server) taskViews(ctx context.Context, tasks []models.Task) ([]taskDTO, error) {
	views := make([]taskDTO, 0, len(tasks))
	if len(tasks) == 0 {
		return views, nil
	}
	ids := make([]string, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
	}
	states, err := s.taskStates(ctx, ids)
	if err != nil {
		return nil, err
	}
	counts, err := s.taskCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	latest, err := s.latestTaskRuns(ctx, ids)
	if err != nil {
		return nil, err
	}
	next := s.Sched.NextRuns()
	for i := range tasks {
		view := taskConfigView(&tasks[i])
		_, view.Running = s.Runs.Active(view.ID)
		if view.Enabled {
			view.NextRunAt = timePtr(next[view.ID])
		}
		view.ItemCount, view.State = counts[view.ID], states[view.ID]
		if run, ok := latest[view.ID]; ok {
			summary := runView(&run, view.Name)
			view.LastRun = &summary
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Server) taskStates(ctx context.Context, ids []string) (map[string]taskStateDTO, error) {
	var rows []struct {
		TaskID                                            string
		LastStartedAt, LastSuccessAt, LastFullReconcileAt *time.Time
		HasCursor                                         bool
	}
	err := s.DB.WithContext(ctx).Model(&models.TaskState{}).
		Select("task_id, last_started_at, last_success_at, last_full_reconcile_at, CASE WHEN committed_cursor_json IS NOT NULL AND committed_cursor_json <> '' THEN 1 ELSE 0 END AS has_cursor").
		Where("task_id IN ?", ids).Scan(&rows).Error
	states := make(map[string]taskStateDTO, len(rows))
	for _, row := range rows {
		states[row.TaskID] = taskStateDTO{LastStartedAt: timePtr(row.LastStartedAt), LastSuccessAt: timePtr(row.LastSuccessAt),
			LastFullReconcileAt: timePtr(row.LastFullReconcileAt), HasCursor: row.HasCursor}
	}
	return states, err
}

func (s *Server) taskCounts(ctx context.Context, ids []string) (map[string]int64, error) {
	var rows []taskCount
	err := s.DB.WithContext(ctx).Model(&models.TaskItem{}).Select("task_id, COUNT(*) AS item_count").
		Where("task_id IN ? AND destination_present = ?", ids, true).Group("task_id").Scan(&rows).Error
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.TaskID] = row.ItemCount
	}
	return counts, err
}

func (s *Server) latestTaskRuns(ctx context.Context, ids []string) (map[string]models.TaskRun, error) {
	db := s.DB.WithContext(ctx)
	latestID := db.Model(&models.TaskRun{}).Select("id").Where("task_id = tasks.id").Order("started_at DESC, id DESC").Limit(1)
	selected := db.Model(&models.Task{}).Select("(?)", latestID).Where("id IN ?", ids)
	var rows []models.TaskRun
	err := db.Omit("log_json", "cursor_before", "cursor_after").Where("id IN (?)", selected).Find(&rows).Error
	latest := make(map[string]models.TaskRun, len(rows))
	for _, row := range rows {
		latest[row.TaskID] = row
	}
	return latest, err
}

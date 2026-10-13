package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var tasks []models.Task
	if err := s.DB.WithContext(ctx).Select("id", "name", "enabled").Order("name").Find(&tasks).Error; err != nil {
		s.fail(w, err)
		return
	}
	names := make(map[string]string, len(tasks))
	enabled := 0
	next := []nextRun{}
	scheduled := s.Sched.NextRuns()
	for _, task := range tasks {
		names[task.ID] = task.Name
		if task.Enabled {
			enabled++
			if at := scheduled[task.ID]; at != nil {
				next = append(next, nextRun{task.ID, task.Name, at.Format(time.RFC3339)})
			}
		}
	}
	slices.SortFunc(next, func(a, b nextRun) int { return strings.Compare(a.NextRunAt, b.NextRunAt) })
	next = next[:min(len(next), 10)]
	active, err := s.runningTasks(ctx, names)
	if err != nil {
		s.fail(w, err)
		return
	}
	failures, err := s.recentFailures(ctx, names)
	if err != nil {
		s.fail(w, err)
		return
	}
	reauth, err := s.credentialsNeedingAttention(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	totals, err := s.dashboardCounts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dashboardDTO{len(tasks), enabled, active, failures, reauth, next, totals})
}

func (s *Server) runningTasks(ctx context.Context, names map[string]string) ([]running, error) {
	active := s.Runs.ActiveTasks()
	out := make([]running, 0, len(active))
	if len(active) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(active))
	for _, runID := range active {
		ids = append(ids, runID)
	}
	var rows []models.TaskRun
	err := s.DB.WithContext(ctx).Select("id", "task_id", "started_at", "status").Where("id IN ?", ids).Find(&rows).Error
	for _, row := range rows {
		out = append(out, running{row.TaskID, names[row.TaskID], row.ID, timePtr(row.StartedAt), row.Status})
	}
	return out, err
}

func (s *Server) recentFailures(ctx context.Context, names map[string]string) ([]runDTO, error) {
	var rows []models.TaskRun
	err := s.DB.WithContext(ctx).Omit("log_json", "cursor_before", "cursor_after").
		Where("status IN ?", []string{models.RunFailed, models.RunInterrupted}).Order("started_at DESC").Limit(10).Find(&rows).Error
	failures := make([]runDTO, 0, len(rows))
	for i := range rows {
		failures = append(failures, runView(&rows[i], names[rows[i].TaskID]))
	}
	return failures, err
}

func (s *Server) credentialsNeedingAttention(ctx context.Context) ([]credRef, error) {
	var rows []models.Credential
	err := s.DB.WithContext(ctx).Select("id", "name", "type", "status").
		Where("status IN ?", []string{models.CredentialReauthRequired, models.CredentialPendingOAuth, models.CredentialError}).Find(&rows).Error
	attention := make([]credRef, 0, len(rows))
	for _, row := range rows {
		attention = append(attention, credRef{row.ID, row.Name, row.Type, row.Status})
	}
	return attention, err
}

func (s *Server) dashboardCounts(ctx context.Context) (dashboardTotals, error) {
	db := s.DB.WithContext(ctx)
	var totals dashboardTotals
	if err := db.Model(&models.TaskItem{}).Where("destination_present = ?", true).Count(&totals.Items).Error; err != nil {
		return totals, err
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	var runs struct{ Runs24h, Failed24h int64 }
	err := db.Model(&models.TaskRun{}).Select("COUNT(*) AS runs24h, COALESCE(SUM(CASE WHEN status IN ? THEN 1 ELSE 0 END), 0) AS failed24h",
		[]string{models.RunFailed, models.RunInterrupted}).Where("started_at >= ?", since).Scan(&runs).Error
	totals.Runs24h, totals.Failed24h = runs.Runs24h, runs.Failed24h
	return totals, err
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
)

type runDTO struct {
	ID              string  `json:"id"`
	TaskID          string  `json:"taskId"`
	TaskName        string  `json:"taskName"`
	TriggerType     string  `json:"triggerType"`
	Status          string  `json:"status" enum:"pending,running,waiting_operations,succeeded,failed,interrupted,cancelled"`
	SyncMode        string  `json:"syncMode"`
	ScheduledFor    *string `json:"scheduledFor"`
	StartedAt       *string `json:"startedAt"`
	FinishedAt      *string `json:"finishedAt"`
	DiscoveredCount int     `json:"discoveredCount"`
	CreatedCount    int     `json:"createdCount"`
	UpdatedCount    int     `json:"updatedCount"`
	DeletedCount    int     `json:"deletedCount"`
	UnchangedCount  int     `json:"unchangedCount"`
	SkippedCount    int     `json:"skippedCount"`
	FailedCount     int     `json:"failedCount"`
	ErrorMessage    string  `json:"errorMessage"`
}

func runView(r *models.TaskRun, taskName string) runDTO {
	return runDTO{
		ID: r.ID, TaskID: r.TaskID, TaskName: taskName, TriggerType: r.TriggerType, Status: r.Status,
		SyncMode: r.SyncMode, ScheduledFor: timePtr(r.ScheduledFor), StartedAt: timePtr(r.StartedAt),
		FinishedAt: timePtr(r.FinishedAt), DiscoveredCount: r.DiscoveredCount, CreatedCount: r.CreatedCount,
		UpdatedCount: r.UpdatedCount, DeletedCount: r.DeletedCount, UnchangedCount: r.UnchangedCount,
		SkippedCount: r.SkippedCount, FailedCount: r.FailedCount, ErrorMessage: r.ErrorMessage,
	}
}

func (s *Server) taskNames() map[string]string {
	var tasks []models.Task
	s.DB.Select("id", "name").Find(&tasks)
	out := make(map[string]string, len(tasks))
	for _, t := range tasks {
		out[t.ID] = t.Name
	}
	return out
}

func (s *Server) queryRuns(w http.ResponseWriter, r *http.Request, taskID string) {
	limit, offset := pageParams(r)
	q := s.DB.WithContext(r.Context()).Model(&models.TaskRun{})
	if taskID == "" {
		taskID = r.URL.Query().Get("taskId")
	}
	if taskID != "" {
		q = q.Where("task_id = ?", taskID)
	}
	if st := r.URL.Query().Get("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		s.fail(w, err)
		return
	}
	var rows []models.TaskRun
	if err := q.Omit("log_json", "cursor_before", "cursor_after").Order("started_at DESC").
		Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		s.fail(w, err)
		return
	}
	names := s.taskNames()
	items := make([]runDTO, 0, len(rows))
	for i := range rows {
		items = append(items, runView(&rows[i], names[rows[i].TaskID]))
	}
	writeJSON(w, http.StatusOK, runPageDTO{items, total})
}

func (s *Server) taskRuns(w http.ResponseWriter, r *http.Request) {
	s.queryRuns(w, r, r.PathValue("id"))
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) { s.queryRuns(w, r, "") }

type operationDTO struct {
	ID                string `json:"id"`
	RemoteOperationID string `json:"remoteOperationId"`
	Type              string `json:"type"`
	Status            string `json:"status" enum:"pending,running,waiting_operations,succeeded,failed,interrupted,cancelled"`
	RetryCount        int    `json:"retryCount"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	var run models.TaskRun
	if err := s.DB.WithContext(r.Context()).First(&run, "id = ?", r.PathValue("id")).Error; err != nil {
		s.fail(w, err)
		return
	}
	var task models.Task
	s.DB.WithContext(r.Context()).Select("id", "name").First(&task, "id = ?", run.TaskID)
	var ops []models.HindsightOperation
	s.DB.WithContext(r.Context()).Where("run_id = ?", run.ID).Order("created_at").Limit(1000).Find(&ops)
	opDTOs := make([]operationDTO, 0, len(ops))
	for _, o := range ops {
		opDTOs = append(opDTOs, operationDTO{
			ID: o.ID, RemoteOperationID: o.RemoteOperationID, Type: o.Type, Status: o.Status,
			RetryCount: o.RetryCount, CreatedAt: timeStr(o.CreatedAt), UpdatedAt: timeStr(o.UpdatedAt),
		})
	}
	logs := []logDTO{}
	_ = json.Unmarshal([]byte(run.LogJSON), &logs)
	writeJSON(w, http.StatusOK, runDetailDTO{runView(&run, task.Name), run.CursorBefore, run.CursorAfter, opDTOs, logs})
}

type settingsDTO struct {
	settings.Settings
	OAuthRedirectURI string `json:"oauthRedirectUri"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Settings.Get(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO{v, s.Cfg.OAuthRedirectURI()})
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Settings.Get(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	previous := v
	// Decoding into the current value applies a partial update.
	if !decode(w, r, &v) {
		return
	}
	if err := v.Validate(); err != nil {
		writeValidation(w, err.Error(), nil)
		return
	}
	if err := s.saveSettings(r.Context(), previous, v); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO{v, s.Cfg.OAuthRedirectURI()})
}

func (s *Server) saveSettings(ctx context.Context, previous, current settings.Settings) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := settings.NewStore(tx).Put(ctx, current); err != nil {
			return err
		}
		reconcile := func(query *gorm.DB) error {
			return query.Updates(map[string]any{"reconcile_required": true, "config_revision": gorm.Expr("config_revision + 1")}).Error
		}
		if previous.FilePolicy != current.FilePolicy {
			if err := reconcile(tx.Model(&models.Task{}).Where("file_policy_mode = ?", models.FilePolicyGlobal)); err != nil {
				return err
			}
		}
		if previous.InlineMultimodalEnabled != current.InlineMultimodalEnabled {
			var sourceTypes []string
			for _, source := range connectors.Sources() {
				if source.Capabilities.SupportsInlineMultimodal {
					sourceTypes = append(sourceTypes, source.Type)
				}
			}
			if len(sourceTypes) > 0 {
				if err := reconcile(tx.Model(&models.Task{}).Where("inline_multimodal_mode = ? AND source_type IN ?", models.FilePolicyGlobal, sourceTypes)); err != nil {
					return err
				}
			}
		}
		if previous.MaxFileSizeMB != current.MaxFileSizeMB {
			return reconcile(tx.Model(&models.Task{}).Where("1 = 1"))
		}
		return nil
	})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var tasks []models.Task
	s.DB.WithContext(ctx).Order("name").Find(&tasks)
	names := map[string]string{}
	enabled := 0

	var next []nextRun
	for _, t := range tasks {
		names[t.ID] = t.Name
		if t.Enabled {
			enabled++
			if n := s.Sched.NextRun(t.ID); n != nil {
				next = append(next, nextRun{t.ID, t.Name, n.Format(time.RFC3339)})
			}
		}
	}
	for i := range next {
		for j := i + 1; j < len(next); j++ {
			if next[j].NextRunAt < next[i].NextRunAt {
				next[i], next[j] = next[j], next[i]
			}
		}
	}
	if len(next) > 10 {
		next = next[:10]
	}
	if next == nil {
		next = []nextRun{}
	}

	runningList := []running{}
	for taskID, runID := range s.Runs.ActiveTasks() {
		var run models.TaskRun
		s.DB.WithContext(ctx).Select("id", "started_at", "status").First(&run, "id = ?", runID)
		runningList = append(runningList, running{taskID, names[taskID], runID, timePtr(run.StartedAt), run.Status})
	}

	var failures []models.TaskRun
	s.DB.WithContext(ctx).Omit("log_json", "cursor_before", "cursor_after").
		Where("status IN ?", []string{models.RunFailed, models.RunInterrupted}).
		Order("started_at DESC").Limit(10).Find(&failures)
	failureDTOs := make([]runDTO, 0, len(failures))
	for i := range failures {
		failureDTOs = append(failureDTOs, runView(&failures[i], names[failures[i].TaskID]))
	}

	var creds []models.Credential
	s.DB.WithContext(ctx).Where("status IN ?", []string{models.CredentialReauthRequired, models.CredentialPendingOAuth, models.CredentialError}).Find(&creds)

	reauth := []credRef{}
	for _, c := range creds {
		reauth = append(reauth, credRef{c.ID, c.Name, c.Type, c.Status})
	}

	var items, runs24, failed24 int64
	since := time.Now().Add(-24 * time.Hour)
	s.DB.WithContext(ctx).Model(&models.TaskItem{}).Where("destination_present = ?", true).Count(&items)
	s.DB.WithContext(ctx).Model(&models.TaskRun{}).Where("started_at >= ?", since).Count(&runs24)
	s.DB.WithContext(ctx).Model(&models.TaskRun{}).Where("started_at >= ? AND status IN ?", since,
		[]string{models.RunFailed, models.RunInterrupted}).Count(&failed24)

	writeJSON(w, http.StatusOK, dashboardDTO{len(tasks), enabled, runningList, failureDTOs, reauth, next, dashboardTotals{items, runs24, failed24}})
}

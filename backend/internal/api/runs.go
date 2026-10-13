package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

type runDTO struct {
	ID              string  `json:"id"`
	TaskID          string  `json:"taskId"`
	TaskName        string  `json:"taskName"`
	TriggerType     string  `json:"triggerType"`
	Status          string  `json:"status" enum:"pending,running,waiting_source,waiting_operations,succeeded,failed,interrupted,cancelled"`
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

func (s *Server) taskNames(ctx context.Context) (map[string]string, error) {
	var tasks []models.Task
	err := s.DB.WithContext(ctx).Select("id", "name").Find(&tasks).Error
	out := make(map[string]string, len(tasks))
	for _, t := range tasks {
		out[t.ID] = t.Name
	}
	return out, err
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
	names, err := s.taskNames(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
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
	Status            string `json:"status"`
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
	if err := s.DB.WithContext(r.Context()).Select("id", "name").First(&task, "id = ?", run.TaskID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.fail(w, err)
		return
	}
	var ops []models.HindsightOperation
	if err := s.DB.WithContext(r.Context()).Where("run_id = ?", run.ID).Order("created_at").Limit(1000).Find(&ops).Error; err != nil {
		s.fail(w, err)
		return
	}
	opDTOs := make([]operationDTO, 0, len(ops))
	for _, o := range ops {
		opDTOs = append(opDTOs, operationDTO{
			ID: o.ID, RemoteOperationID: o.RemoteOperationID, Type: o.Type, Status: o.Status,
			RetryCount: o.RetryCount, CreatedAt: timeStr(o.CreatedAt), UpdatedAt: timeStr(o.UpdatedAt),
		})
	}
	logs := []logDTO{}
	_ = json.Unmarshal([]byte(run.LogJSON), &logs)
	var sourceJob *sourceJobDTO
	var persisted models.SourceJob
	if err := s.DB.WithContext(r.Context()).First(&persisted, "run_id = ?", run.ID).Error; err == nil {
		sourceJob = &sourceJobDTO{Phase: persisted.Phase, Error: persisted.ErrorMessage}
		if persisted.JobJSON != "" {
			var job connectors.SourceJob
			if err := json.Unmarshal([]byte(persisted.JobJSON), &job); err != nil {
				writeError(w, http.StatusInternalServerError, "source_job_corrupt", "Invalid persisted source job")
				return
			}
			sourceJob.ID, sourceJob.StartedAt, sourceJob.Status = job.ID, job.StartedAt.Format(time.RFC3339), job.Status
			sourceJob.Total, sourceJob.Finished, sourceJob.BrowserSecondsUsed = job.Total, job.Finished, job.BrowserSecondsUsed
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runDetailDTO{SourceJob: sourceJob, runDTO: runView(&run, task.Name), CursorBefore: run.CursorBefore, CursorAfter: run.CursorAfter, Operations: opDTOs, Log: logs})
}

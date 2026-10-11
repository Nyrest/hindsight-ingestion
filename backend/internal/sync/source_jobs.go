package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/shared"
)

var SourcePollInterval = 15 * time.Second

type sourceRequestSnapshot struct {
	Config map[string]any    `json:"config"`
	Filter connectors.Filter `json:"filter"`
	Cursor json.RawMessage   `json:"cursor"`
	Full   bool              `json:"full"`
}

func (r *runner) scanSource(req connectors.ScanRequest, opts RunOptions) (connectors.ScanResult, error) {
	source, async := r.src.(connectors.AsyncSourceConnector)
	if !async {
		return r.src.Scan(r.ctx, req)
	}
	job, row, err := r.sourceJob(source, req)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	if err := r.setSourceMode(job); err != nil {
		return connectors.ScanResult{}, err
	}
	r.setStatus(models.RunWaitingSource)
	if err := opts.waitSource(func() error { return r.pollSource(source, &job, &row) }); err != nil {
		return connectors.ScanResult{}, r.recordSourceWaitError(job, &row, err)
	}
	r.setStatus(models.RunRunning)
	if err := r.lockSharedSource(); err != nil {
		return connectors.ScanResult{}, err
	}
	return r.readSourceJob(source, req, job, &row)
}

func (r *runner) setSourceMode(job connectors.SourceJob) error {
	r.full = job.Full
	if !r.reing {
		r.run.SyncMode = models.SyncIncremental
		if r.full {
			r.run.SyncMode = models.SyncFull
		}
		return r.e.DB.WithContext(r.ctx).Model(r.run).Update("sync_mode", r.run.SyncMode).Error
	}
	return nil
}

func (opts RunOptions) waitSource(wait func() error) error {
	if opts.WaitSource != nil {
		return opts.WaitSource(wait)
	}
	return wait()
}

func (r *runner) recordSourceWaitError(job connectors.SourceJob, row *models.SourceJob, err error) error {
	if r.ctx.Err() != nil {
		return err
	}
	row.ErrorMessage = err.Error()
	if job.Status != "running" && job.Status != "completed" {
		row.Phase = "failed"
	}
	return errors.Join(err, r.e.DB.Save(row).Error)
}

func (r *runner) lockSharedSource() error {
	if r.sharedDocs == nil {
		return nil
	}
	release, err := shared.Lock(r.ctx, r.sharedDocs.TargetKey)
	if err != nil {
		return err
	}
	r.releaseShared = release
	return r.awaitSharedOperations()
}

func (r *runner) readSourceJob(source connectors.AsyncSourceConnector, req connectors.ScanRequest, job connectors.SourceJob, row *models.SourceJob) (connectors.ScanResult, error) {
	outcome, err := source.ReadJob(r.ctx, req, job)
	if httpx.IsStatus(err, 404) || httpx.IsStatus(err, 410) {
		row.Phase = "failed"
	}
	if err != nil && r.ctx.Err() == nil {
		// Settle successfully submitted pages before reporting a partial result read.
		outcome.Error = errors.Join(outcome.Error, err)
		outcome.Complete = false
		err = nil
	}
	if outcome.Error != nil {
		row.ErrorMessage = outcome.Error.Error()
		if err == nil && outcome.Cursor != nil {
			row.Phase = "failed"
		}
		if saveErr := r.e.DB.Save(row).Error; saveErr != nil {
			return outcome, errors.Join(err, saveErr)
		}
	}
	return outcome, err
}

func (r *runner) sourceJob(source connectors.AsyncSourceConnector, req connectors.ScanRequest) (connectors.SourceJob, models.SourceJob, error) {
	var previous models.SourceJob
	err := r.e.DB.WithContext(r.ctx).Where("task_id = ? AND config_revision = ? AND phase IN ?", r.task.ID, r.task.ConfigRevision, []string{"running", "completed"}).Order("created_at DESC").Take(&previous).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return connectors.SourceJob{}, previous, err
	}
	if err == nil {
		return r.reuseSourceJob(previous)
	}
	snapshot, err := json.Marshal(sourceRequestSnapshot{Config: req.Config, Filter: req.Filter, Cursor: req.Cursor, Full: req.Full})
	if err != nil {
		return connectors.SourceJob{}, models.SourceJob{}, err
	}
	row := models.SourceJob{RunID: r.run.ID, TaskID: r.task.ID, ConfigRevision: r.task.ConfigRevision, Phase: "submitting", RequestJSON: string(snapshot)}
	if err := r.e.DB.WithContext(r.ctx).Create(&row).Error; err != nil {
		return connectors.SourceJob{}, row, err
	}
	job, err := source.StartJob(r.ctx, req)
	if err != nil {
		row.Phase, row.ErrorMessage = "failed", err.Error()
		if saveErr := r.e.DB.Save(&row).Error; saveErr != nil {
			return job, row, errors.Join(err, saveErr)
		}
		return job, row, err
	}
	row.Phase, row.JobJSON = "running", jobJSON(job)
	if err := r.e.DB.Save(&row).Error; err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return job, row, errors.Join(err, source.CancelJob(cleanup, req.Credential, job))
	}
	return job, row, nil
}

func (r *runner) reuseSourceJob(previous models.SourceJob) (connectors.SourceJob, models.SourceJob, error) {
	var job connectors.SourceJob
	if err := json.Unmarshal([]byte(previous.JobJSON), &job); err != nil {
		return job, previous, err
	}
	if job.ID == "" {
		return job, previous, errors.New("persisted source job is missing its remote ID")
	}
	current := previous
	current.RunID, current.ErrorMessage = r.run.ID, ""
	if previous.RunID != current.RunID {
		current.CreatedAt = time.Now().UTC()
	}
	err := r.e.DB.WithContext(r.ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		if previous.RunID != current.RunID {
			return tx.Model(&previous).Update("phase", "superseded").Error
		}
		return nil
	})
	return job, current, err
}

func jobJSON(job connectors.SourceJob) string {
	encoded, _ := json.Marshal(job)
	return string(encoded)
}

func (r *runner) pollSource(source connectors.AsyncSourceConnector, job *connectors.SourceJob, row *models.SourceJob) error {
	for {
		current, err := source.PollJob(r.ctx, r.cred, *job)
		*job = current
		row.JobJSON = jobJSON(current)
		if current.Status == "completed" {
			row.Phase = "completed"
		}
		if saveErr := r.e.DB.Save(row).Error; saveErr != nil {
			return saveErr
		}
		if err != nil {
			return err
		}
		if current.Status == "completed" {
			return nil
		}
		if err := sleepCtx(r.ctx, SourcePollInterval); err != nil {
			return err
		}
	}
}

func (e *Engine) CancelSourceJob(ctx context.Context, taskID string) (err error) {
	var task models.Task
	if err := e.DB.WithContext(ctx).First(&task, "id = ?", taskID).Error; err != nil {
		return err
	}
	connector, ok := connectors.Source(task.SourceType)
	if !ok {
		return nil
	}
	source, ok := connector.(connectors.AsyncSourceConnector)
	if !ok {
		return nil
	}
	var row models.SourceJob
	err = e.DB.WithContext(ctx).Where("task_id = ? AND phase IN ?", taskID, []string{"running", "completed", "cancel_failed"}).Order("created_at DESC").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, e.DB.Model(&row).Updates(map[string]any{"phase": "cancel_failed", "error_message": err.Error()}).Error)
		}
	}()
	cred, err := e.Creds.Load(ctx, task.SourceCredentialID)
	if err != nil {
		return err
	}
	var job connectors.SourceJob
	if err := json.Unmarshal([]byte(row.JobJSON), &job); err != nil {
		return err
	}
	if err := source.CancelJob(ctx, cred, job); err != nil {
		return err
	}
	return e.DB.WithContext(ctx).Model(&row).Updates(map[string]any{"phase": "cancelled", "error_message": ""}).Error
}

func (r *runner) awaitSharedOperations() error {
	deadline := time.Now().Add(OperationWaitTimeout)
	for {
		operations, err := r.sharedOperations()
		if err != nil {
			return err
		}
		if len(operations) == 0 {
			return nil
		}
		for _, operation := range operations {
			status, err := r.dest.GetOperation(r.ctx, r.task.DestinationBankID, operation.RemoteOperationID)
			if err != nil {
				return err
			}
			switch status.Status {
			case "pending", "processing", "retrying", "completed", "failed", "not_found", "cancelled":
			default:
				return fmt.Errorf("unknown Hindsight operation status %q; shared writes remain protected", status.Status)
			}
			if err := r.e.DB.WithContext(r.ctx).Model(&operation).Updates(map[string]any{"status": status.Status, "retry_count": status.RetryCount}).Error; err != nil {
				return err
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("shared bank has unconfirmed Hindsight writes")
		}
		if err := sleepCtx(r.ctx, OperationPollInitial); err != nil {
			return err
		}
	}
}

func (r *runner) sharedOperations() ([]models.HindsightOperation, error) {
	var tasks []string
	if err := r.e.DB.WithContext(r.ctx).Model(&models.TaskItem{}).Where("shared_target_key = ?", r.sharedDocs.TargetKey).Distinct("task_id").Pluck("task_id", &tasks).Error; err != nil {
		return nil, err
	}
	tasks = append(tasks, r.task.ID)
	var operations []models.HindsightOperation
	err := r.e.DB.WithContext(r.ctx).Where("run_id IN (?)", r.e.DB.Model(&models.TaskRun{}).Select("id").Where("task_id IN ?", tasks)).
		Where("status NOT IN ?", []string{"completed", "failed", "not_found", "cancelled"}).Find(&operations).Error
	return operations, err
}

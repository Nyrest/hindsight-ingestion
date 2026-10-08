package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

var errPendingRetain = errors.New("Hindsight is still processing this task's documents; try again after processing finishes")

type deleteDocumentsDTO struct {
	DeletedCount int `json:"deletedCount"`
}

func (s *Server) deleteTaskDocuments(w http.ResponseWriter, r *http.Request) {
	ctx, release, err := s.Runs.BeginMaintenance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, "conflict", "Task is busy; finish or cancel its current operation first")
		return
	}
	defer release()
	var task models.Task
	if err := s.DB.WithContext(ctx).First(&task, "id = ?", r.PathValue("id")).Error; err != nil {
		s.fail(w, err)
		return
	}
	count, err := s.clearDocuments(ctx, &task)
	if err != nil {
		s.documentCleanupError(w, count, err)
		return
	}
	writeJSON(w, http.StatusOK, deleteDocumentsDTO{count})
}

func (s *Server) clearDocuments(ctx context.Context, task *models.Task) (int, error) {
	if err := s.pauseForCleanup(ctx, task); err != nil {
		return 0, err
	}
	if err := s.checkPendingRetains(ctx, task); err != nil {
		return 0, err
	}
	client, err := s.taskDestination(ctx, task)
	if err != nil {
		return 0, err
	}
	ids, err := client.TaskDocuments(ctx, task.DestinationBankID, task.ID)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, id := range ids {
		if err := client.DeleteDocument(ctx, task.DestinationBankID, id); err != nil {
			return deleted, err
		}
		deleted++
		if err := s.recordDocumentDeleted(ctx, task.ID, id); err != nil {
			return deleted, err
		}
	}
	// A prior request may have deleted remotely but lost its local acknowledgment.
	if err := s.DB.WithContext(ctx).Model(&models.TaskItem{}).Where("task_id = ?", task.ID).
		Updates(map[string]any{"synced_fingerprint": ""}).Error; err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (s *Server) taskDestination(ctx context.Context, task *models.Task) (*hindsight.Client, error) {
	cred, err := s.Creds.Load(ctx, task.DestinationCredentialID)
	if err != nil {
		return nil, err
	}
	return hindsight.NewClient(cred)
}

func (s *Server) checkPendingRetains(ctx context.Context, task *models.Task) error {
	var ops []models.HindsightOperation
	if err := s.DB.WithContext(ctx).Where("run_id IN (?)", s.DB.Model(&models.TaskRun{}).Select("id").Where("task_id = ?", task.ID)).
		Where("status NOT IN ?", []string{"completed", "failed", "not_found", "cancelled"}).Find(&ops).Error; err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}
	client, err := s.taskDestination(ctx, task)
	if err != nil {
		return err
	}
	for _, op := range ops {
		status, err := client.GetOperation(ctx, task.DestinationBankID, op.RemoteOperationID)
		if err != nil {
			return err
		}
		switch status.Status {
		case "completed", "failed", "not_found", "cancelled":
			if err := s.DB.WithContext(ctx).Model(&models.HindsightOperation{}).Where("id = ?", op.ID).Update("status", status.Status).Error; err != nil {
				return err
			}
		default:
			return errPendingRetain
		}
	}
	return nil
}

func (s *Server) pauseForCleanup(ctx context.Context, task *models.Task) error {
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(task).Updates(map[string]any{"enabled": false, "reconcile_required": true, "config_revision": gorm.Expr("config_revision + 1")}).Error; err != nil {
			return err
		}
		return tx.Model(&models.TaskState{}).Where("task_id = ?", task.ID).Updates(map[string]any{"committed_cursor_json": "", "last_full_reconcile_at": nil}).Error
	})
	if err == nil {
		s.Sched.RemoveTask(task.ID)
		task.Enabled = false
	}
	return err
}

func (s *Server) recordDocumentDeleted(ctx context.Context, taskID, documentID string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.TaskItem{}).Where("task_id = ? AND destination_document_id = ?", taskID, documentID).
			Updates(map[string]any{"destination_present": false, "synced_fingerprint": "", "last_error": ""}).Error; err != nil {
			return err
		}
		return tx.Model(&models.TaskItem{}).Where("task_id = ? AND previous_document_id = ?", taskID, documentID).Update("previous_document_id", "").Error
	})
}

func (s *Server) documentCleanupError(w http.ResponseWriter, deleted int, err error) {
	if errors.Is(err, errPendingRetain) {
		writeError(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	s.Log.Warn("document cleanup failed", "deletedCount", deleted, "error", err)
	writeError(w, http.StatusBadGateway, "upstream", fmt.Sprintf("Document cleanup failed after deleting %d documents. The task was not deleted. Retry to finish cleanup.", deleted))
}

func (s *Server) credentialTags(w http.ResponseWriter, r *http.Request) {
	bankID := strings.TrimSpace(r.URL.Query().Get("bankId"))
	if bankID == "" {
		writeValidation(w, "Select a memory bank", map[string]string{"bankId": "Required"})
		return
	}
	cred, err := s.Creds.Load(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if cred.Type != hindsight.CredentialType {
		writeValidation(w, "Select a Hindsight credential", nil)
		return
	}
	client, err := hindsight.NewClient(cred)
	if err != nil {
		s.fail(w, err)
		return
	}
	limit, offset := pageParams(r)
	page, err := client.Tags(r.Context(), bankID, r.URL.Query().Get("q"), limit, offset)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", "Could not load tags from Hindsight")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

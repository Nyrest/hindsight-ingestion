package api

import (
	"context"

	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/shared"
)

func (s *Server) lockSharedDocuments(ctx context.Context, task *models.Task) (shared.Documents, func(), error) {
	cred, err := s.Creds.Load(ctx, task.DestinationCredentialID)
	if err != nil {
		return shared.Documents{}, nil, err
	}
	key, err := shared.TargetKey(cred, task.DestinationBankID)
	if err != nil {
		return shared.Documents{}, nil, err
	}
	release, err := shared.Lock(ctx, key)
	return shared.Documents{DB: s.DB, TargetKey: key}, release, err
}

func (s *Server) clearSharedDocuments(ctx context.Context, task *models.Task, client *hindsight.Client, progress *documentCleanupProgress) (int, error) {
	documents, release, err := s.lockSharedDocuments(ctx, task)
	if err != nil {
		return 0, err
	}
	defer release()
	if err := s.checkPendingRetains(ctx, task); err != nil {
		return 0, err
	}
	var references []models.TaskItem
	if err := s.DB.WithContext(ctx).Where("shared_target_key = ? AND destination_present = ?", documents.TargetKey, true).Find(&references).Error; err != nil {
		return 0, err
	}
	if err := s.checkSharedOperations(ctx, references); err != nil {
		return 0, err
	}
	deleted := 0
	total := 0
	for _, row := range references {
		if row.TaskID == task.ID {
			total++
		}
	}
	progress.setTotal(total)
	for _, row := range references {
		if row.TaskID != task.ID {
			continue
		}
		other, err := documents.OtherReferences(ctx, row)
		if err != nil {
			return deleted, err
		}
		if !other {
			if err := client.DeleteDocument(ctx, task.DestinationBankID, row.DestinationDocumentID); err != nil {
				return deleted, err
			}
			deleted++
			progress.advance(true)
			if err := documents.Forget(ctx, row.DestinationDocumentID); err != nil {
				return deleted, err
			}
		} else {
			progress.advance(false)
		}
		if err := s.recordDocumentDeleted(ctx, task.ID, row.DestinationDocumentID); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func (s *Server) checkSharedOperations(ctx context.Context, references []models.TaskItem) error {
	checked := map[string]bool{}
	for _, row := range references {
		if checked[row.TaskID] {
			continue
		}
		checked[row.TaskID] = true
		var task models.Task
		if err := s.DB.WithContext(ctx).First(&task, "id = ?", row.TaskID).Error; err != nil {
			return err
		}
		if err := s.checkPendingRetains(ctx, &task); err != nil {
			return err
		}
	}
	return nil
}

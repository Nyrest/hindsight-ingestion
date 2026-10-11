package sync

import (
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *runner) reserveShared(batch []textJob, operationID string) error {
	return r.e.DB.WithContext(r.ctx).Transaction(func(tx *gorm.DB) error {
		documents := *r.sharedDocs
		documents.DB = tx
		for _, job := range batch {
			row := job.row
			row.DestinationPresent = true
			row.SyncedFingerprint = ""
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
				return err
			}
			if err := documents.Confirm(r.ctx, row); err != nil {
				return err
			}
		}
		// The operation ID is known before submission, even if its response is lost.
		return tx.Create(&models.HindsightOperation{ID: uuid.NewString(), RunID: r.run.ID, RemoteOperationID: operationID, Type: "retain", Status: "submitting"}).Error
	})
}

func (r *runner) recordSharedOperations(ops []string, submittedID string) error {
	return r.e.DB.WithContext(r.ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("run_id = ? AND remote_operation_id = ? AND status = ?", r.run.ID, submittedID, "submitting").Delete(&models.HindsightOperation{}).Error; err != nil {
			return err
		}
		if len(ops) == 0 {
			return nil
		}
		return tx.Create(r.operationRows(ops, "retain")).Error
	})
}

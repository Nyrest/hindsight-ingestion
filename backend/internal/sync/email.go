package sync

import (
	"fmt"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

// Older asynchronous writes must settle before a new scan can delete documents.
func (r *runner) fenceEmailOperations() error {
	var ops []models.HindsightOperation
	runs := r.e.DB.Model(&models.TaskRun{}).Select("id").Where("task_id = ? AND id <> ?", r.task.ID, r.run.ID)
	if err := r.e.DB.WithContext(r.ctx).Where("run_id IN (?) AND status NOT IN ?", runs, []string{"completed", "failed", "cancelled", "not_found"}).Find(&ops).Error; err != nil {
		return err
	}
	deadline := time.Now().Add(OperationWaitTimeout)
	for _, op := range ops {
		for {
			status, err := r.dest.GetOperation(r.ctx, r.task.DestinationBankID, op.RemoteOperationID)
			if err != nil {
				return fmt.Errorf("fence prior email retain: %w", err)
			}
			switch status.Status {
			case "completed", "failed", "cancelled", "not_found":
				if err := r.e.DB.WithContext(r.ctx).Model(&models.HindsightOperation{}).Where("id = ?", op.ID).Update("status", status.Status).Error; err != nil {
					return err
				}
			case "pending", "processing", "retrying":
				if time.Now().After(deadline) {
					return fmt.Errorf("prior email retain has not settled")
				}
				if err := sleepCtx(r.ctx, OperationPollInitial); err != nil {
					return err
				}
				continue
			default:
				return fmt.Errorf("unknown prior email operation status: %s", status.Status)
			}
			break
		}
	}
	return nil
}

func (r *runner) reconcileEmailDeletions() error {
	if err := r.reconcileDeletions(false); err != nil {
		return err
	}
	for {
		var gone []models.TaskItem
		if err := r.e.DB.WithContext(r.ctx).Where("task_id = ? AND last_seen_generation < ? AND destination_present = ?", r.task.ID, r.gen, true).Order("source_item_id").Limit(500).Find(&gone).Error; err != nil {
			return err
		}
		if len(gone) == 0 {
			break
		}
		for i := range gone {
			gone[i].DesiredMatch = false
		}
		r.deletes = gone
		if err := r.reconcileDeletions(false); err != nil {
			return err
		}
	}
	return r.e.DB.WithContext(r.ctx).Where("task_id = ? AND last_seen_generation < ? AND destination_present = ?", r.task.ID, r.gen, false).Delete(&models.TaskItem{}).Error
}

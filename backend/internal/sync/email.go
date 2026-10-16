package sync

import "github.com/Nyrest/hindsight-ingestion/internal/models"

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

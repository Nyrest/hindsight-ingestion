package sync

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func (r *runner) checkTaskRevision(db *gorm.DB) error {
	var current models.Task
	if err := db.Select("id", "config_revision").First(&current, "id = ?", r.task.ID).Error; err != nil {
		return err
	}
	if current.ConfigRevision != r.task.ConfigRevision {
		return fmt.Errorf("%s task configuration changed during this run; deletion and state commit skipped", strings.ToUpper(r.task.SourceType))
	}
	return nil
}

func (r *runner) reconcileDeletions(complete bool) error {
	if err := r.checkTaskRevision(r.e.DB.WithContext(r.ctx)); err != nil {
		return err
	}
	if complete {
		if r.task.SourceType == "email" {
			return r.reconcileEmailDeletions()
		}
		var gone []models.TaskItem
		if err := r.e.DB.WithContext(r.ctx).
			Where("task_id = ? AND last_seen_generation < ? AND destination_present = ?", r.task.ID, r.gen, true).
			Find(&gone).Error; err != nil {
			return err
		}
		for i := range gone {
			gone[i].DesiredMatch = false
		}
		r.deletes = append(r.deletes, gone...)
		// Drop ledger rows of vanished items that never reached the destination.
		if err := r.e.DB.WithContext(r.ctx).
			Where("task_id = ? AND last_seen_generation < ? AND destination_present = ?", r.task.ID, r.gen, false).
			Delete(&models.TaskItem{}).Error; err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, row := range r.deletes {
		if err := r.checkTaskRevision(r.e.DB.WithContext(r.ctx)); err != nil {
			return err
		}
		if seen[row.SourceItemID] {
			continue
		}
		seen[row.SourceItemID] = true
		sharedReference := false
		if r.sharedDocs != nil {
			var err error
			sharedReference, err = r.sharedDocs.OtherReferences(r.ctx, row)
			if err != nil {
				return err
			}
		}
		if !sharedReference {
			if err := r.dest.DeleteDocument(r.ctx, r.task.DestinationBankID, row.DestinationDocumentID); err != nil {
				if r.ctx.Err() != nil {
					return r.ctx.Err()
				}
				return fmt.Errorf("delete %s from Hindsight: %w", row.DestinationDocumentID, err)
			}
			if r.sharedDocs != nil {
				if err := r.sharedDocs.Forget(r.ctx, row.DestinationDocumentID); err != nil {
					return err
				}
			}
		}
		if sharedReference {
			r.logf("info", "Released shared reference: %s", row.SourcePath)
		} else {
			r.run.DeletedCount++
			r.logf("info", "Deleted %s from Hindsight", orEmpty(row.SourcePath, orEmpty(row.SourceName, row.SourceItemID)))
		}
		row.DestinationPresent = false
		row.SyncedFingerprint = ""
		if row.LastSeenGeneration < r.gen && complete {
			// Item vanished from the source: drop it from the ledger.
			if err := r.e.DB.WithContext(r.ctx).Where("task_id = ? AND source_item_id = ?", row.TaskID, row.SourceItemID).
				Delete(&models.TaskItem{}).Error; err != nil {
				return err
			}
			continue
		}
		if err := r.saveRow(row); err != nil {
			return err
		}
	}
	r.deletes = nil
	return nil
}

func (r *runner) queueSeen(row models.TaskItem) error {
	r.seen = append(r.seen, row)
	if len(r.seen) >= ledgerFlushSize {
		return r.flushSeen()
	}
	return nil
}

func (r *runner) flushSeen() error {
	if len(r.seen) == 0 {
		return nil
	}
	rows := r.seen
	r.seen = nil
	return r.upsertRows(rows)
}

func (r *runner) saveRow(row models.TaskItem) error { return r.upsertRows([]models.TaskItem{row}) }

func (r *runner) upsertRows(rows []models.TaskItem) error {
	if len(rows) == 0 {
		return nil
	}
	return r.e.DB.WithContext(r.ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "task_id"}, {Name: "source_item_id"}},
		UpdateAll: true,
	}).CreateInBatches(rows, 100).Error
}

func (r *runner) recordRetainIntent(batch []retainJob) error {
	if r.task.SourceType != "webcal" && r.task.SourceType != "caldav" && r.task.SourceType != "email" && r.task.SourceType != "steam" && r.task.SourceType != "google_people" {
		return nil
	}
	rows := make([]models.TaskItem, 0, len(batch))
	for index := range batch {
		// Unconfirmed writes must remain retryable after an interrupted run.
		batch[index].row.LastError = "Hindsight retain has not been confirmed"
		row := batch[index].row
		if r.task.SourceType == "caldav" || r.task.SourceType == "email" || r.task.SourceType == "steam" || r.task.SourceType == "google_people" {
			row.DestinationPresent = true
		}
		rows = append(rows, row)
	}
	return r.upsertRows(rows)
}

func (r *runner) hasFailedItems() bool {
	for _, row := range r.ledger {
		if row.LastError != "" {
			return true
		}
	}
	return false
}

// loadLedger reads the task's ledger in pages to avoid per-item queries.
func (r *runner) loadLedger() error {
	r.ledger = map[string]models.TaskItem{}
	lastID := ""
	for {
		var batch []models.TaskItem
		q := r.e.DB.WithContext(r.ctx).Where("task_id = ?", r.task.ID)
		if lastID != "" {
			q = q.Where("source_item_id > ?", lastID)
		}
		if err := q.Order("source_item_id").Limit(1000).Find(&batch).Error; err != nil {
			return err
		}
		for _, row := range batch {
			r.ledger[row.SourceItemID] = row
		}
		if len(batch) < 1000 {
			return nil
		}
		lastID = batch[len(batch)-1].SourceItemID
	}
}

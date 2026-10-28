package email

import (
	"context"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (c *Connector) AbortScan(ctx context.Context, runID string) error {
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return clearStage(tx, runID) })
}
func clearStage(tx *gorm.DB, runID string) error {
	if err := tx.Where("run_id = ?", runID).Delete(&models.EmailScanOccurrence{}).Error; err != nil {
		return err
	}
	return tx.Where("run_id = ?", runID).Delete(&models.EmailScanIdentity{}).Error
}

func (c *Connector) CommitScan(ctx context.Context, runID string, tx *gorm.DB) error {
	var run models.TaskRun
	if err := tx.First(&run, "id = ?", runID).Error; err != nil {
		return err
	}
	full := run.SyncMode != models.SyncIncremental
	if full {
		if err := tx.Where("task_id = ?", run.TaskID).Delete(&models.EmailOccurrence{}).Error; err != nil {
			return err
		}
	}
	// Advance through immutable staging by the complete ordered keys; OFFSET
	// repeatedly rescans the prefix and becomes quadratic on large mailboxes.
	lastIdentity := ""
	for {
		var identities []models.EmailScanIdentity
		if err := tx.Where("run_id = ? AND item_id > ?", runID, lastIdentity).Order("item_id").Limit(100).Find(&identities).Error; err != nil {
			return err
		}
		if len(identities) == 0 {
			break
		}
		rows := make([]models.EmailIdentity, 0, len(identities))
		for _, row := range identities {
			rows = append(rows, models.EmailIdentity{TaskID: row.TaskID, ItemID: row.ItemID, AccountKey: row.AccountKey, MessageIDHash: row.MessageIDHash, ContentFingerprint: row.ContentFingerprint, SnapshotJSON: row.SnapshotJSON})
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return err
		}
		lastIdentity = identities[len(identities)-1].ItemID
	}
	var lastOccurrence *models.EmailScanOccurrence
	for {
		var occurrences []models.EmailScanOccurrence
		q := tx.Where("run_id = ?", runID)
		if lastOccurrence != nil {
			p := lastOccurrence
			q = q.Where("account_key > ? OR (account_key = ? AND folder_key > ?) OR (account_key = ? AND folder_key = ? AND uid_validity > ?) OR (account_key = ? AND folder_key = ? AND uid_validity = ? AND uid > ?)", p.AccountKey, p.AccountKey, p.FolderKey, p.AccountKey, p.FolderKey, p.UIDValidity, p.AccountKey, p.FolderKey, p.UIDValidity, p.UID)
		}
		if err := q.Order("account_key, folder_key, uid_validity, uid").Limit(100).Find(&occurrences).Error; err != nil {
			return err
		}
		if len(occurrences) == 0 {
			break
		}
		rows := make([]models.EmailOccurrence, 0, len(occurrences))
		for _, row := range occurrences {
			rows = append(rows, models.EmailOccurrence{TaskID: row.TaskID, AccountKey: row.AccountKey, FolderKey: row.FolderKey, UIDValidity: row.UIDValidity, UID: row.UID, FolderName: row.FolderName, ItemID: row.ItemID, PartsJSON: row.PartsJSON})
		}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&rows).Error; err != nil {
			return err
		}
		lastOccurrence = &occurrences[len(occurrences)-1]
	}
	if full {
		live := tx.Model(&models.EmailOccurrence{}).Select("item_id").Where("task_id = ?", run.TaskID)
		if err := tx.Where("task_id = ? AND item_id NOT IN (?)", run.TaskID, live).Delete(&models.EmailIdentity{}).Error; err != nil {
			return err
		}
	}
	return clearStage(tx, runID)
}

package api

import (
	"errors"
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

var errTaskCredentialsChanged = errors.New("task credentials changed; reload credentials")

func lockTaskCredentials(tx *gorm.DB, task *models.Task) error {
	ids := []string{task.SourceCredentialID, task.DestinationCredentialID}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	var rows []models.Credential
	// Task writes and credential deletion use the same locks, in ID order.
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id IN ?", ids).Order("id").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(ids) {
		return errTaskCredentialsChanged
	}
	return nil
}

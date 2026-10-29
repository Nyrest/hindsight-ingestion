package api

import (
	"context"
	"errors"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"gorm.io/gorm"
)

func (s *Server) validatePeopleAccountChange(ctx context.Context, old, updated models.Task) error {
	db := s.DB.WithContext(ctx)
	var state models.TaskState
	if err := db.First(&state, "task_id = ?", old.ID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var tracked int64
	if err := db.Model(&models.TaskItem{}).Where("task_id = ?", old.ID).Limit(1).Count(&tracked).Error; err != nil {
		return err
	}
	if !old.DestinationLocked && tracked == 0 && state.CommittedCursorJSON == "" && state.PendingScanRunID == "" {
		return nil
	}
	previous, err := s.Creds.Get(ctx, old.SourceCredentialID)
	if err != nil {
		return err
	}
	next, err := s.Creds.Get(ctx, updated.SourceCredentialID)
	if err != nil {
		return err
	}
	if previous.OAuthSubject == "" || previous.OAuthSubject != next.OAuthSubject {
		return errors.New("Google account changed; create a new credential and task")
	}
	return nil
}

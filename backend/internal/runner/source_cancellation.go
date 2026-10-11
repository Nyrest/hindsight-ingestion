package runner

import (
	"context"
	"fmt"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func (m *Manager) checkSourceCancellation(taskID string) error {
	var count int64
	if err := m.db.Model(&models.SourceJob{}).Where("task_id = ? AND phase = ?", taskID, "cancel_failed").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w: Cloudflare cancellation is unconfirmed; retry Cancel first", ErrAlreadyRunning)
	}
	return nil
}

func (m *Manager) RetrySourceCancellation(ctx context.Context, taskID string) (bool, error) {
	m.mu.Lock()
	if m.active[taskID] != nil || m.editing[taskID] {
		m.mu.Unlock()
		return false, ErrAlreadyRunning
	}
	var count int64
	err := m.db.WithContext(ctx).Model(&models.SourceJob{}).Where("task_id = ? AND phase = ?", taskID, "cancel_failed").Count(&count).Error
	if err != nil || count == 0 {
		m.mu.Unlock()
		return false, err
	}
	m.editing[taskID] = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.editing, taskID); m.mu.Unlock() }()
	err = m.engine.CancelSourceJob(ctx, taskID)
	return err == nil, err
}

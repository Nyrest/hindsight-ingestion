package runner

import (
	"context"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func (m *Manager) resumeSource(run models.TaskRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.active[run.TaskID]; busy {
		return ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancelCause(m.base)
	activeRun := &active{runID: run.ID, cancel: cancel, done: make(chan struct{}), asyncSource: true}
	m.active[run.TaskID] = activeRun
	m.wg.Add(1)
	go m.execute(ctx, run.TaskID, activeRun, run.SyncMode)
	return nil
}

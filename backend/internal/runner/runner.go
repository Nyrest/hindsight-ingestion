// Package runner executes task runs with a per-task guard and a global
// concurrency limit, independent of how the run was triggered.
package runner

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	stdsync "sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

// ErrAlreadyRunning is returned when a task already has an active run.
var ErrAlreadyRunning = errors.New("task is already running")

// ErrShutdown is the cancellation cause used on application shutdown.
var ErrShutdown = errors.New("interrupted by application shutdown")

// ActiveStatuses are TaskRun statuses representing an unfinished run.
var ActiveStatuses = []string{models.RunPending, models.RunRunning, models.RunWaitingOperations}

type active struct {
	maintenance bool
	runID       string
	cancel      context.CancelCauseFunc
	done        chan struct{}
	start       time.Time
}

// Manager coordinates run execution.
type Manager struct {
	db     *gorm.DB
	engine *sync.Engine
	log    *slog.Logger
	sem    chan struct{}

	base       context.Context
	cancelBase context.CancelCauseFunc

	mu      stdsync.Mutex
	active  map[string]*active
	editing map[string]bool
	wg      stdsync.WaitGroup
}

// NewManager creates a Manager allowing maxConcurrent simultaneous runs.
func NewManager(db *gorm.DB, engine *sync.Engine, maxConcurrent int, log *slog.Logger) *Manager {
	base, cancel := context.WithCancelCause(context.Background())
	return &Manager{
		db: db, engine: engine, log: log,
		sem:  make(chan struct{}, maxConcurrent),
		base: base, cancelBase: cancel,
		active:  map[string]*active{},
		editing: map[string]bool{},
	}
}

// RecoverInterrupted marks runs left unfinished by a previous process as
// interrupted. Their committed cursor was never advanced, so the next run
// resumes safely from the last committed state.
func (m *Manager) RecoverInterrupted(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	res := m.db.WithContext(ctx).Model(&models.TaskRun{}).Where("status IN ?", ActiveStatuses).
		Updates(map[string]any{
			"status":        models.RunInterrupted,
			"finished_at":   now,
			"error_message": "Interrupted: the application stopped before this run completed",
		})
	if res.Error != nil {
		return 0, res.Error
	}
	m.db.WithContext(ctx).Model(&models.HindsightOperation{}).
		Where("status IN ?", []string{"pending", "processing", "retrying"}).
		Update("status", "abandoned")
	return res.RowsAffected, nil
}

// Start begins a run. When wait is true the call blocks until the run
// finishes (used by scheduled jobs so gocron singleton mode applies).
func (m *Manager) Start(taskID, trigger, mode string, scheduledFor *time.Time, wait bool) (string, error) {
	m.mu.Lock()
	if m.base.Err() != nil {
		m.mu.Unlock()
		return "", ErrShutdown
	}
	if _, busy := m.active[taskID]; busy || m.editing[taskID] {
		m.mu.Unlock()
		return "", ErrAlreadyRunning
	}
	// DB guard. Within this (single) instance every live run is in
	// m.active, so active rows not tracked here are stale leftovers from a
	// failed status write; close them instead of blocking the task forever.
	var stale []string
	if err := m.db.Model(&models.TaskRun{}).Where("task_id = ? AND status IN ?", taskID, ActiveStatuses).
		Pluck("id", &stale).Error; err != nil {
		m.mu.Unlock()
		return "", err
	}
	for _, id := range stale {
		m.log.Warn("closing stale active run", "task", taskID, "run", id)
		m.engine.MarkTerminal(id, models.RunInterrupted, "Interrupted: run state was not recorded")
	}

	now := time.Now().UTC()
	run := models.TaskRun{
		ID: uuid.NewString(), TaskID: taskID, TriggerType: trigger, Status: models.RunPending,
		SyncMode: orDefault(mode, models.SyncIncremental), ScheduledFor: scheduledFor, StartedAt: &now,
	}
	if err := m.db.Create(&run).Error; err != nil {
		m.mu.Unlock()
		return "", err
	}
	ctx, cancel := context.WithCancelCause(m.base)
	a := &active{runID: run.ID, cancel: cancel, done: make(chan struct{}), start: now}
	m.active[taskID] = a
	m.wg.Add(1)
	m.mu.Unlock()

	go m.execute(ctx, taskID, a, mode)
	if wait {
		<-a.done
	}
	return run.ID, nil
}

func (m *Manager) execute(ctx context.Context, taskID string, a *active, mode string) {
	defer m.wg.Done()
	defer close(a.done)
	defer func() {
		m.mu.Lock()
		delete(m.active, taskID)
		m.mu.Unlock()
		a.cancel(nil)
	}()
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("run panicked", "task", taskID, "run", a.runID, "panic", p)
			m.engine.MarkTerminal(a.runID, models.RunFailed, "internal error (panic)")
		}
	}()

	// Global concurrency limit.
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		status := models.RunCancelled
		msg := "Run cancelled while queued"
		if cause := context.Cause(ctx); errors.Is(cause, ErrShutdown) {
			status, msg = models.RunInterrupted, cause.Error()
		}
		m.engine.MarkTerminal(a.runID, status, msg)
		return
	}
	defer func() { <-m.sem }()

	started := time.Now().UTC()
	m.db.Model(&models.TaskRun{}).Where("id = ?", a.runID).Updates(map[string]any{"status": models.RunRunning, "started_at": started})
	if err := m.engine.Execute(ctx, a.runID, sync.RunOptions{Mode: mode}); err != nil {
		m.log.Warn("run finished with error", "task", taskID, "run", a.runID, "error", err)
	}
	// Safety net: never leave the row active once execution returned.
	var statuses []string
	m.db.Model(&models.TaskRun{}).Where("id = ?", a.runID).Pluck("status", &statuses)
	if len(statuses) == 0 || slices.Contains(ActiveStatuses, statuses[0]) {
		m.engine.MarkTerminal(a.runID, models.RunFailed, "run ended without recording a result")
	}
}

// Cancel cancels the active run of a task.
func (m *Manager) Cancel(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.active[taskID]
	if ok {
		a.cancel(context.Canceled)
	}
	return ok
}

// Active returns the active run ID of a task.
func (m *Manager) Active(taskID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.active[taskID]
	if !ok {
		return "", false
	}
	return a.runID, true
}

// ActiveTasks returns task IDs with an active run.
func (m *Manager) ActiveTasks() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.active))
	for id, a := range m.active {
		if a.runID != "" {
			out[id] = a.runID
		}
	}
	return out
}

// DryRun shares the task guard and concurrency limit with normal runs.
func (m *Manager) DryRun(ctx context.Context, taskID string) (sync.DryRunResult, error) {
	m.mu.Lock()
	if m.base.Err() != nil {
		m.mu.Unlock()
		return sync.DryRunResult{}, ErrShutdown
	}
	if _, busy := m.active[taskID]; busy || m.editing[taskID] {
		m.mu.Unlock()
		return sync.DryRunResult{}, ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(m.base, func() { cancel(ErrShutdown) })
	a := &active{maintenance: true, cancel: cancel, done: make(chan struct{}), start: time.Now()}
	m.active[taskID] = a
	m.wg.Add(1)
	m.mu.Unlock()
	defer func() {
		stop()
		cancel(nil)
		m.mu.Lock()
		delete(m.active, taskID)
		m.mu.Unlock()
		close(a.done)
		m.wg.Done()
	}()
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-ctx.Done():
		return sync.DryRunResult{}, ctx.Err()
	}
	return m.engine.DryRun(ctx, taskID)
}

// Shutdown interrupts every active run and waits for them to record state.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.cancelBase(ErrShutdown)
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		m.log.Warn("timed out waiting for runs to stop")
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (m *Manager) BeginEdit(taskID string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.editing[taskID] || (m.active[taskID] != nil && m.active[taskID].maintenance) {
		return nil, ErrAlreadyRunning
	}
	m.editing[taskID] = true
	return func() { m.mu.Lock(); delete(m.editing, taskID); m.mu.Unlock() }, nil
}

func (m *Manager) BeginMaintenance(ctx context.Context, taskID string) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.base.Err() != nil {
		return ctx, nil, ErrShutdown
	}
	if m.active[taskID] != nil || m.editing[taskID] {
		return ctx, nil, ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(m.base, func() { cancel(ErrShutdown) })
	a := &active{maintenance: true, cancel: cancel, done: make(chan struct{}), start: time.Now()}
	m.active[taskID] = a
	m.wg.Add(1)
	release := func() {
		stop()
		cancel(nil)
		m.mu.Lock()
		delete(m.active, taskID)
		m.mu.Unlock()
		close(a.done)
		m.wg.Done()
	}
	return ctx, release, nil
}

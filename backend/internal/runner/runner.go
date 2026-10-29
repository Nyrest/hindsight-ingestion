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

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

// ErrAlreadyRunning is returned when a task already has an active run.
var ErrAlreadyRunning = errors.New("task is already running")

// ErrShutdown is the cancellation cause used on application shutdown.
var ErrShutdown = errors.New("interrupted by application shutdown")

// ActiveStatuses are TaskRun statuses representing an unfinished run.
var ActiveStatuses = []string{models.RunPending, models.RunRunning, models.RunWaitingSource, models.RunWaitingOperations}

type active struct {
	asyncSource bool
	maintenance bool
	runID       string
	cancel      context.CancelCauseFunc
	done        chan struct{}
}

// Manager coordinates run execution.
type Manager struct {
	db                 *gorm.DB
	engine             *sync.Engine
	log                *slog.Logger
	slotMu             stdsync.Mutex
	maxConcurrentTasks int
	usedSlots          int
	slotsChanged       chan struct{}

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
		maxConcurrentTasks: maxConcurrent,
		slotsChanged:       make(chan struct{}),
		base:               base, cancelBase: cancel,
		active:  map[string]*active{},
		editing: map[string]bool{},
	}
}

// RecoverInterrupted marks runs left unfinished by a previous process as
// interrupted. Their committed cursor was never advanced, so the next run
// resumes safely from the last committed state.
func (m *Manager) RecoverInterrupted(ctx context.Context) (int64, error) {
	var resumable []models.TaskRun
	if err := m.db.WithContext(ctx).Where("status = ? AND id IN (?)", models.RunWaitingSource,
		m.db.Model(&models.SourceJob{}).Select("run_id").Where("phase IN ?", []string{"running", "completed"})).Find(&resumable).Error; err != nil {
		return 0, err
	}
	var resumableIDs []string
	for _, run := range resumable {
		resumableIDs = append(resumableIDs, run.ID)
	}
	now := time.Now().UTC()
	query := m.db.WithContext(ctx).Model(&models.TaskRun{}).Where("status IN ?", ActiveStatuses)
	if len(resumableIDs) > 0 {
		query = query.Where("id NOT IN ?", resumableIDs)
	}
	res := query.
		Updates(map[string]any{
			"status":        models.RunInterrupted,
			"finished_at":   now,
			"error_message": "Interrupted: the application stopped before this run completed",
		})
	if res.Error != nil {
		return 0, res.Error
	}
	for _, model := range []any{&models.GooglePeopleScanAlias{}, &models.GooglePeopleScanContact{}} {
		liveRuns := m.db.Model(&models.TaskRun{}).Select("id").Where("status IN ?", ActiveStatuses)
		if err := m.db.WithContext(ctx).Where("run_id NOT IN (?)", liveRuns).Delete(model).Error; err != nil {
			return 0, err
		}
	}
	if err := m.db.WithContext(ctx).Model(&models.HindsightOperation{}).
		Where("status IN ?", []string{"pending", "processing", "retrying"}).
		Where("run_id NOT IN (?)", m.db.Model(&models.SourceJob{}).Select("run_id")).
		Update("status", "abandoned").Error; err != nil {
		return 0, err
	}
	for _, run := range resumable {
		if err := m.resumeSource(run); err != nil {
			return 0, err
		}
	}
	var recoverable []models.TaskRun
	if err := m.db.WithContext(ctx).Where("status = ? AND id IN (?)", models.RunInterrupted,
		m.db.Model(&models.SourceJob{}).Select("run_id").Where("phase IN ?", []string{"running", "completed"})).Find(&recoverable).Error; err != nil {
		return 0, err
	}
	for _, run := range recoverable {
		if _, err := m.Start(run.TaskID, run.TriggerType, run.SyncMode, nil, false); err != nil && !errors.Is(err, ErrAlreadyRunning) {
			return 0, err
		}
	}
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
	if err := m.checkSourceCancellation(taskID); err != nil {
		m.mu.Unlock()
		return "", err
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

	var task models.Task
	if err := m.db.First(&task, "id = ?", taskID).Error; err != nil {
		m.mu.Unlock()
		return "", err
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
	connector, _ := connectors.Source(task.SourceType)
	_, async := connector.(connectors.AsyncSourceConnector)
	a := &active{runID: run.ID, cancel: cancel, done: make(chan struct{}), asyncSource: async}
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
		if !a.asyncSource || ctx.Err() == nil || errors.Is(context.Cause(ctx), ErrShutdown) {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := m.engine.CancelSourceJob(cleanup, taskID); err != nil {
			m.log.Warn("source cancellation failed", "task", taskID, "error", err)
			m.engine.MarkTerminal(a.runID, models.RunFailed, "Cloudflare cancellation is unconfirmed; retry Cancel: "+err.Error())
		}
	}()
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("run panicked", "task", taskID, "run", a.runID, "panic", p)
			m.engine.MarkTerminal(a.runID, models.RunFailed, "internal error (panic)")
		}
	}()

	// Global concurrency limit.
	if err := m.acquireSlot(ctx); err != nil {
		status := models.RunCancelled
		msg := "Run cancelled while queued"
		if cause := context.Cause(ctx); errors.Is(cause, ErrShutdown) {
			status, msg = models.RunInterrupted, cause.Error()
		}
		m.engine.MarkTerminal(a.runID, status, msg)
		return
	}
	held := true
	defer func() {
		if held {
			m.releaseSlot()
		}
	}()

	started := time.Now().UTC()
	m.db.Model(&models.TaskRun{}).Where("id = ?", a.runID).Updates(map[string]any{"status": models.RunRunning, "started_at": started})
	if err := m.engine.Execute(ctx, a.runID, sync.RunOptions{Mode: mode, WaitSource: m.waitWithoutSlot(ctx, &held)}); err != nil {
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
	if err := m.checkSourceCancellation(taskID); err != nil {
		m.mu.Unlock()
		return sync.DryRunResult{}, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(m.base, func() { cancel(ErrShutdown) })
	a := &active{maintenance: true, cancel: cancel, done: make(chan struct{})}
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
	if err := m.acquireSlot(ctx); err != nil {
		return sync.DryRunResult{}, err
	}
	held := true
	defer func() {
		if held {
			m.releaseSlot()
		}
	}()
	return m.engine.DryRunWithOptions(ctx, taskID, sync.RunOptions{WaitSource: m.waitWithoutSlot(ctx, &held)})
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
	if m.editing[taskID] || (m.active[taskID] != nil && (m.active[taskID].maintenance || m.active[taskID].asyncSource)) {
		return nil, ErrAlreadyRunning
	}
	if err := m.checkSourceCancellation(taskID); err != nil {
		return nil, err
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
	if err := m.checkSourceCancellation(taskID); err != nil {
		return ctx, nil, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(m.base, func() { cancel(ErrShutdown) })
	a := &active{maintenance: true, cancel: cancel, done: make(chan struct{})}
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

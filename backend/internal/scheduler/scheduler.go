// Package scheduler owns the single application gocron scheduler: one cron
// job per enabled task and one-time OAuth refresh jobs.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	stdsync "sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/runner"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
)

// RefreshFunc refreshes an OAuth credential.
type RefreshFunc func(ctx context.Context, credentialID string) error

// Scheduler wraps gocron.Scheduler.
type Scheduler struct {
	s        gocron.Scheduler
	db       *gorm.DB
	runs     *runner.Manager
	refresh  RefreshFunc
	log      *slog.Logger
	settings *settings.Store

	mu          stdsync.Mutex
	taskJobs    map[string]uuid.UUID
	refreshJobs map[string]uuid.UUID
}

// New creates the scheduler (not yet started).
func New(db *gorm.DB, runs *runner.Manager, store *settings.Store, log *slog.Logger) (*Scheduler, error) {
	global, err := store.Get(context.Background())
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(global.Timezone)
	if err != nil {
		return nil, fmt.Errorf("global timezone: %w", err)
	}
	s, err := gocron.NewScheduler(
		gocron.WithLocation(location),
		gocron.WithStopTimeout(30*time.Second),
	)
	if err != nil {
		return nil, err
	}
	return &Scheduler{
		s: s, db: db, runs: runs, log: log, settings: store,
		taskJobs:    map[string]uuid.UUID{},
		refreshJobs: map[string]uuid.UUID{},
	}, nil
}

// SetRefreshFunc wires the OAuth refresh implementation.
func (s *Scheduler) SetRefreshFunc(f RefreshFunc) { s.refresh = f }

// LoadTasks registers every enabled task (startup reconstruction).
func (s *Scheduler) LoadTasks(ctx context.Context) error {
	var tasks []models.Task
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&tasks).Error; err != nil {
		return err
	}
	for _, t := range tasks {
		if err := s.SyncTask(t); err != nil {
			s.log.Error("failed to schedule task", "task", t.ID, "error", err)
		}
	}
	s.log.Info("tasks scheduled", "count", len(tasks))
	return nil
}

// Start starts the scheduler.
func (s *Scheduler) Start() { s.s.Start() }

// Shutdown stops the scheduler gracefully.
func (s *Scheduler) Shutdown() error { return s.s.Shutdown() }

// CronSpec builds the gocron crontab with timezone prefix.
func CronSpec(expr, tz string) string {
	if tz == "" {
		tz = "UTC"
	}
	return "CRON_TZ=" + tz + " " + strings.TrimSpace(expr)
}

// ValidateCron validates a 5-field cron expression and timezone and returns
// the next n run times.
func ValidateCron(expr, tz string, n int) ([]time.Time, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, errors.New("cron expression is required")
	}
	if strings.HasPrefix(expr, "TZ=") || strings.HasPrefix(expr, "CRON_TZ=") {
		return nil, errors.New("timezone prefixes are not supported; use the global timezone setting")
	}
	if len(strings.Fields(expr)) != 5 && !strings.HasPrefix(expr, "@") {
		return nil, errors.New("use a standard 5-field cron expression (minute hour day month weekday)")
	}
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return nil, fmt.Errorf("unknown timezone %q", tz)
	}
	sched, err := cron.ParseStandard(CronSpec(expr, tz))
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %v", err)
	}
	out := make([]time.Time, 0, n)
	t := time.Now()
	for range n {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t.UTC())
	}
	return out, nil
}

// SyncTask registers, updates or removes the task's job to match its state.
func (s *Scheduler) SyncTask(t models.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, exists := s.taskJobs[t.ID]
	if !t.Enabled {
		if exists {
			delete(s.taskJobs, t.ID)
			return s.s.RemoveJob(id)
		}
		return nil
	}
	global, err := s.settings.Get(context.Background())
	if err != nil {
		return err
	}
	if _, err := ValidateCron(t.CronExpression, global.Timezone, 1); err != nil {
		return err
	}
	def := gocron.CronJob(CronSpec(t.CronExpression, global.Timezone), false)
	taskID := t.ID
	task := gocron.NewTask(func() { s.runScheduled(taskID) })
	opts := []gocron.JobOption{
		gocron.WithName("task:" + t.ID),
		gocron.WithTags("task", "task:"+t.ID),
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
	}
	if exists {
		job, err := s.s.Update(id, def, task, opts...)
		if err == nil {
			s.taskJobs[t.ID] = job.ID()
			return nil
		}
		_ = s.s.RemoveJob(id)
	}
	job, err := s.s.NewJob(def, task, opts...)
	if err != nil {
		return err
	}
	s.taskJobs[t.ID] = job.ID()
	return nil
}

// RemoveTask removes the task's job.
func (s *Scheduler) RemoveTask(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.taskJobs[taskID]; ok {
		_ = s.s.RemoveJob(id)
		delete(s.taskJobs, taskID)
	}
}

// NextRun returns the next scheduled run of a task.
func (s *Scheduler) NextRun(taskID string) *time.Time {
	s.mu.Lock()
	id, ok := s.taskJobs[taskID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	for _, j := range s.s.Jobs() {
		if j.ID() == id {
			t, err := j.NextRun()
			if err != nil || t.IsZero() {
				return nil
			}
			t = t.UTC()
			return &t
		}
	}
	return nil
}

func (s *Scheduler) runScheduled(taskID string) {
	now := time.Now().UTC().Truncate(time.Second)
	// Blocks until the run finishes so singleton mode prevents overlap.
	_, err := s.runs.Start(taskID, models.TriggerScheduled, "", &now, true)
	switch {
	case errors.Is(err, runner.ErrAlreadyRunning):
		s.log.Info("scheduled run skipped: task still running", "task", taskID)
	case err != nil:
		s.log.Error("scheduled run failed to start", "task", taskID, "error", err)
	}
}

// ScheduleRefresh creates (or replaces) a one-time OAuth refresh job.
func (s *Scheduler) ScheduleRefresh(credentialID string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.refreshJobs[credentialID]; ok {
		_ = s.s.RemoveJob(id)
		delete(s.refreshJobs, credentialID)
	}
	if s.refresh == nil {
		return
	}
	if at.Before(time.Now().Add(time.Second)) {
		at = time.Now().Add(time.Second)
	}
	credID := credentialID
	job, err := s.s.NewJob(
		gocron.OneTimeJob(gocron.OneTimeJobStartDateTime(at)),
		gocron.NewTask(func(ctx context.Context) {
			s.mu.Lock()
			delete(s.refreshJobs, credID)
			s.mu.Unlock()
			// The refresh schedules the next one-time job itself.
			if err := s.refresh(ctx, credID); err != nil {
				s.log.Warn("scheduled oauth refresh failed", "credential", credID, "error", err)
			}
		}),
		gocron.WithName("oauth-refresh:"+credentialID),
		gocron.WithTags("oauth", "oauth:"+credentialID),
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
	)
	if err != nil {
		s.log.Error("failed to schedule oauth refresh", "credential", credentialID, "error", err)
		return
	}
	s.refreshJobs[credentialID] = job.ID()
	s.log.Debug("oauth refresh scheduled", "credential", credentialID, "at", at.UTC().Format(time.RFC3339))
}

// CancelRefresh removes a pending refresh job.
func (s *Scheduler) CancelRefresh(credentialID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.refreshJobs[credentialID]; ok {
		_ = s.s.RemoveJob(id)
		delete(s.refreshJobs, credentialID)
	}
}

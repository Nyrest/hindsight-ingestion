package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/runner"
	"github.com/Nyrest/hindsight-ingestion/internal/scheduler"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

type taskStateDTO struct {
	LastStartedAt       *string `json:"lastStartedAt"`
	LastSuccessAt       *string `json:"lastSuccessAt"`
	LastFullReconcileAt *string `json:"lastFullReconcileAt"`
	HasCursor           bool    `json:"hasCursor"`
}

type taskDTO struct {
	ID                      string                `json:"id"`
	Name                    string                `json:"name"`
	Enabled                 bool                  `json:"enabled"`
	SourceType              string                `json:"sourceType"`
	SourceCredentialID      string                `json:"sourceCredentialId"`
	SourceConfig            map[string]any        `json:"sourceConfig"`
	SourceFilter            connectors.Filter     `json:"sourceFilter"`
	DestinationCredentialID string                `json:"destinationCredentialId"`
	DestinationBankID       string                `json:"destinationBankId"`
	RetainStrategy          string                `json:"retainStrategy"`
	CustomTags              []string              `json:"customTags"`
	CustomMetadata          map[string]string     `json:"customMetadata"`
	FilePolicyMode          string                `json:"filePolicyMode"`
	FilePolicy              connectors.FilePolicy `json:"filePolicy"`
	CronExpression          string                `json:"cronExpression"`
	CronTimezone            string                `json:"cronTimezone"`
	ConfigRevision          int64                 `json:"configRevision"`
	PolicyRevision          int64                 `json:"policyRevision"`
	ReconcileRequired       bool                  `json:"reconcileRequired"`
	DestinationLocked       bool                  `json:"destinationLocked"`
	Running                 bool                  `json:"running"`
	NextRunAt               *string               `json:"nextRunAt"`
	LastRun                 *runDTO               `json:"lastRun"`
	ItemCount               int64                 `json:"itemCount"`
	State                   taskStateDTO          `json:"state"`
	CreatedAt               string                `json:"createdAt"`
	UpdatedAt               string                `json:"updatedAt"`
}

func (s *Server) taskView(ctx context.Context, t *models.Task) taskDTO {
	d := taskDTO{
		ID: t.ID, Name: t.Name, Enabled: t.Enabled, SourceType: t.SourceType,
		SourceCredentialID: t.SourceCredentialID, DestinationCredentialID: t.DestinationCredentialID,
		DestinationBankID: t.DestinationBankID, RetainStrategy: t.RetainStrategy,
		FilePolicyMode: t.FilePolicyMode, CronExpression: t.CronExpression, CronTimezone: t.CronTimezone,
		ConfigRevision: t.ConfigRevision, PolicyRevision: t.PolicyRevision,
		ReconcileRequired: t.ReconcileRequired, DestinationLocked: t.DestinationLocked,
		CreatedAt: timeStr(t.CreatedAt), UpdatedAt: timeStr(t.UpdatedAt),
		SourceConfig: map[string]any{}, CustomTags: []string{}, CustomMetadata: map[string]string{},
		SourceFilter: connectors.Filter{Mode: "simple", Rules: []connectors.FilterRule{}},
	}
	_ = json.Unmarshal([]byte(t.SourceConfigJSON), &d.SourceConfig)
	_ = json.Unmarshal([]byte(t.SourceFilterJSON), &d.SourceFilter)
	if d.SourceFilter.Mode == "" {
		d.SourceFilter.Mode = "simple"
	}
	if d.SourceFilter.Rules == nil {
		d.SourceFilter.Rules = []connectors.FilterRule{}
	}
	_ = json.Unmarshal([]byte(t.CustomTagsJSON), &d.CustomTags)
	_ = json.Unmarshal([]byte(t.CustomMetadataJSON), &d.CustomMetadata)
	if err := json.Unmarshal([]byte(t.FilePolicyJSON), &d.FilePolicy); err != nil {
		d.FilePolicy = connectors.DefaultFilePolicy
	}
	_, d.Running = s.Runs.Active(t.ID)
	if t.Enabled {
		d.NextRunAt = timePtr(s.Sched.NextRun(t.ID))
	}
	var last models.TaskRun
	if err := s.DB.WithContext(ctx).Where("task_id = ?", t.ID).Order("started_at DESC").Limit(1).Take(&last).Error; err == nil {
		v := runView(&last, t.Name)
		d.LastRun = &v
	}
	s.DB.WithContext(ctx).Model(&models.TaskItem{}).Where("task_id = ? AND destination_present = ?", t.ID, true).Count(&d.ItemCount)
	var st models.TaskState
	if err := s.DB.WithContext(ctx).Where("task_id = ?", t.ID).Take(&st).Error; err == nil {
		d.State = taskStateDTO{
			LastStartedAt: timePtr(st.LastStartedAt), LastSuccessAt: timePtr(st.LastSuccessAt),
			LastFullReconcileAt: timePtr(st.LastFullReconcileAt), HasCursor: st.CommittedCursorJSON != "",
		}
	}
	return d
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	var rows []models.Task
	if err := s.DB.WithContext(r.Context()).Order("name").Find(&rows).Error; err != nil {
		s.fail(w, err)
		return
	}
	out := make([]taskDTO, 0, len(rows))
	for i := range rows {
		out = append(out, s.taskView(r.Context(), &rows[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	var t models.Task
	if err := s.DB.WithContext(r.Context()).First(&t, "id = ?", r.PathValue("id")).Error; err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskView(r.Context(), &t))
}

// taskBody is a create/patch request. Pointer fields are optional on patch.
type taskBody struct {
	Name                    *string                `json:"name"`
	Enabled                 *bool                  `json:"enabled"`
	SourceType              *string                `json:"sourceType"`
	SourceCredentialID      *string                `json:"sourceCredentialId"`
	SourceConfig            map[string]any         `json:"sourceConfig"`
	SourceFilter            *connectors.Filter     `json:"sourceFilter"`
	DestinationCredentialID *string                `json:"destinationCredentialId"`
	DestinationBankID       *string                `json:"destinationBankId"`
	RetainStrategy          *string                `json:"retainStrategy"`
	CustomTags              *[]string              `json:"customTags"`
	CustomMetadata          *map[string]string     `json:"customMetadata"`
	FilePolicyMode          *string                `json:"filePolicyMode"`
	FilePolicy              *connectors.FilePolicy `json:"filePolicy"`
	CronExpression          *string                `json:"cronExpression"`
	CronTimezone            *string                `json:"cronTimezone"`
}

type fieldErrors map[string]string

func (f fieldErrors) add(field, msg string) {
	if _, ok := f[field]; !ok {
		f[field] = msg
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// applyTask merges and validates task configuration, then computes its sync effects.
// It returns whether the incremental cursor must be reset.
func (s *Server) applyTask(ctx context.Context, t *models.Task, b taskBody, creating bool) (resetCursor bool, errs fieldErrors) {
	errs = fieldErrors{}
	old := *t

	if b.Name != nil {
		t.Name = strings.TrimSpace(*b.Name)
	}
	if t.Name == "" {
		errs.add("name", "Name is required")
	}
	if b.Enabled != nil {
		t.Enabled = *b.Enabled
	}

	if b.SourceType != nil {
		if !creating && *b.SourceType != t.SourceType {
			errs.add("sourceType", "Source type cannot be changed; create a new task")
		}
		t.SourceType = *b.SourceType
	}
	src, ok := connectors.Source(t.SourceType)
	if !ok {
		errs.add("sourceType", "Unknown source type")
		return false, errs
	}
	info := src.Info()

	if b.SourceCredentialID != nil {
		t.SourceCredentialID = *b.SourceCredentialID
	}
	if cred, err := s.Creds.Get(ctx, t.SourceCredentialID); err != nil {
		errs.add("sourceCredentialId", "Select a source credential")
	} else if cred.Type != info.CredentialType {
		errs.add("sourceCredentialId", "Credential type does not match the source")
	}

	cfg := map[string]any{}
	_ = json.Unmarshal([]byte(t.SourceConfigJSON), &cfg)
	oldCfg := cloneMap(cfg)
	if b.SourceConfig != nil {
		cfg = map[string]any{}
		for _, f := range info.Fields {
			if v, ok := b.SourceConfig[f.Key]; ok && v != nil {
				cfg[f.Key] = v
			} else if f.Default != nil {
				cfg[f.Key] = f.Default
			}
		}
	}
	filter := connectors.Filter{Mode: "simple"}
	_ = json.Unmarshal([]byte(t.SourceFilterJSON), &filter)
	if b.SourceFilter != nil {
		filter = *b.SourceFilter
		if filter.Mode == "" {
			filter.Mode = "simple"
		}
		if filter.Mode == "simple" {
			filter.AdvancedQuery = ""
		}
	}
	if filter.Rules == nil {
		filter.Rules = []connectors.FilterRule{}
	}
	for _, f := range info.Fields {
		if f.Required && connectors.AsString(cfg[f.Key]) == "" {
			errs.add("sourceConfig."+f.Key, f.Label+" is required")
		}
	}
	if err := src.ValidateConfig(cfg, filter); err != nil {
		var fe *connectors.FieldError
		switch {
		case errors.As(err, &fe):
			errs.add("sourceConfig."+fe.Field, fe.Message)
		case strings.Contains(err.Error(), "filter"):
			errs.add("sourceFilter", err.Error())
		default:
			errs.add("sourceConfig", err.Error())
		}
	}
	t.SourceConfigJSON = mustJSON(cfg)
	t.SourceFilterJSON = mustJSON(filter)

	destChanged := false
	if b.DestinationCredentialID != nil && *b.DestinationCredentialID != t.DestinationCredentialID {
		destChanged = true
		t.DestinationCredentialID = *b.DestinationCredentialID
	}
	if b.DestinationBankID != nil && strings.TrimSpace(*b.DestinationBankID) != t.DestinationBankID {
		destChanged = true
		t.DestinationBankID = strings.TrimSpace(*b.DestinationBankID)
	}
	if destChanged && t.DestinationLocked {
		errs.add("destinationBankId", "Destination is locked after the first successful run; clone the task to change it")
	}
	if cred, err := s.Creds.Get(ctx, t.DestinationCredentialID); err != nil {
		errs.add("destinationCredentialId", "Select a Hindsight credential")
	} else if cred.Type != hindsight.CredentialType {
		errs.add("destinationCredentialId", "Destination must be a Hindsight credential")
	}
	if t.DestinationBankID == "" {
		errs.add("destinationBankId", "Bank is required")
	}
	if t.SourceType == "hindsight" && t.SourceCredentialID == t.DestinationCredentialID &&
		connectors.AsString(cfg["bankId"]) == t.DestinationBankID {
		errs.add("destinationBankId", "Source and destination are the same bank (no-op)")
	}

	if b.RetainStrategy != nil {
		t.RetainStrategy = strings.TrimSpace(*b.RetainStrategy)
	}
	if b.CustomTags != nil {
		var tags []string
		for _, tag := range *b.CustomTags {
			if tag = strings.TrimSpace(tag); tag != "" && !slices.Contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
		if tags == nil {
			tags = []string{}
		}
		t.CustomTagsJSON = mustJSON(tags)
	} else if creating {
		t.CustomTagsJSON = "[]"
	}
	if b.CustomMetadata != nil {
		md := map[string]string{}
		for k, v := range *b.CustomMetadata {
			k = strings.TrimSpace(k)
			if k == "" {
				errs.add("customMetadata", "Metadata keys must not be empty")
				continue
			}
			if strings.HasPrefix(k, sync.ReservedMetadataPrefix) {
				errs.add("customMetadata", "Keys starting with _ingestion_ are reserved")
				continue
			}
			md[k] = v
		}
		t.CustomMetadataJSON = mustJSON(md)
	} else if creating {
		t.CustomMetadataJSON = "{}"
	}

	if b.FilePolicyMode != nil {
		t.FilePolicyMode = *b.FilePolicyMode
	}
	if t.FilePolicyMode == "" {
		t.FilePolicyMode = models.FilePolicyGlobal
	}
	if t.FilePolicyMode != models.FilePolicyGlobal && t.FilePolicyMode != models.FilePolicyOverride {
		errs.add("filePolicyMode", "Must be global or override")
	}
	if b.FilePolicy != nil {
		t.FilePolicyJSON = mustJSON(*b.FilePolicy)
	} else if t.FilePolicyJSON == "" {
		t.FilePolicyJSON = mustJSON(connectors.DefaultFilePolicy)
	}

	if b.CronExpression != nil {
		t.CronExpression = strings.TrimSpace(*b.CronExpression)
	}
	if b.CronTimezone != nil {
		t.CronTimezone = strings.TrimSpace(*b.CronTimezone)
	}
	if t.CronTimezone == "" {
		t.CronTimezone = "UTC"
	}
	if _, err := scheduler.ValidateCron(t.CronExpression, t.CronTimezone, 1); err != nil {
		if strings.Contains(err.Error(), "timezone") {
			errs.add("cronTimezone", err.Error())
		} else {
			errs.add("cronExpression", err.Error())
		}
	}

	if creating {
		return true, errs
	}

	policyChanged := old.RetainStrategy != t.RetainStrategy || old.CustomTagsJSON != t.CustomTagsJSON ||
		old.CustomMetadataJSON != t.CustomMetadataJSON
	if policyChanged {
		t.PolicyRevision++
		// Re-retain matching items: requires observing every item.
		t.ReconcileRequired = true
	}
	if old.SourceCredentialID != t.SourceCredentialID {
		resetCursor = true
	}
	for _, f := range info.Fields {
		if reflect.DeepEqual(oldCfg[f.Key], cfg[f.Key]) {
			continue
		}
		switch f.Effect {
		case connectors.EffectRebaseline:
			resetCursor = true
		default:
			t.ReconcileRequired = true
		}
	}
	if old.SourceFilterJSON != t.SourceFilterJSON || old.FilePolicyMode != t.FilePolicyMode ||
		(t.FilePolicyMode == models.FilePolicyOverride && old.FilePolicyJSON != t.FilePolicyJSON) {
		t.ReconcileRequired = true
	}
	if resetCursor {
		t.ReconcileRequired = true
	}
	if old.SourceConfigJSON != t.SourceConfigJSON || old.SourceFilterJSON != t.SourceFilterJSON ||
		old.SourceCredentialID != t.SourceCredentialID || old.FilePolicyJSON != t.FilePolicyJSON ||
		old.FilePolicyMode != t.FilePolicyMode || policyChanged {
		t.ConfigRevision++
	}
	return resetCursor, errs
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var b taskBody
	if !decode(w, r, &b) {
		return
	}
	t := models.Task{ID: uuid.NewString(), Enabled: true, ConfigRevision: 1, PolicyRevision: 1, CronTimezone: "UTC"}
	if _, errs := s.applyTask(r.Context(), &t, b, true); len(errs) > 0 {
		writeValidation(w, "Please fix the highlighted fields", errs)
		return
	}
	err := s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&t).Error; err != nil {
			return err
		}
		return tx.Create(&models.TaskState{TaskID: t.ID}).Error
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Sched.SyncTask(t); err != nil {
		s.Log.Error("schedule task", "task", t.ID, "error", err)
	}
	s.Log.Info("task created", "task", t.ID)
	writeJSON(w, http.StatusCreated, s.taskView(r.Context(), &t))
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	var t models.Task
	if err := s.DB.WithContext(r.Context()).First(&t, "id = ?", r.PathValue("id")).Error; err != nil {
		s.fail(w, err)
		return
	}
	var b taskBody
	if !decode(w, r, &b) {
		return
	}
	origDestCred, origBank := t.DestinationCredentialID, t.DestinationBankID
	resetCursor, errs := s.applyTask(r.Context(), &t, b, false)
	if len(errs) > 0 {
		writeValidation(w, "Please fix the highlighted fields", errs)
		return
	}
	destChanged := t.DestinationCredentialID != origDestCred || t.DestinationBankID != origBank
	errLocked := errors.New("destination locked")
	err := s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		// Re-read engine-owned fields under a row lock: a run may have
		// locked the destination since the task was loaded.
		var cur models.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("destination_locked").First(&cur, "id = ?", t.ID).Error; err != nil {
			return err
		}
		if cur.DestinationLocked && destChanged {
			return errLocked
		}
		t.DestinationLocked = cur.DestinationLocked
		if err := tx.Save(&t).Error; err != nil {
			return err
		}
		if resetCursor {
			// Require full rebaseline: forget the cursor, keep the ledger so
			// unchanged items are not re-uploaded and removed ones are deleted.
			return tx.Model(&models.TaskState{}).Where("task_id = ?", t.ID).
				Updates(map[string]any{"committed_cursor_json": "", "last_full_reconcile_at": nil}).Error
		}
		return nil
	})
	if errors.Is(err, errLocked) {
		writeValidation(w, "Please fix the highlighted fields", map[string]string{
			"destinationBankId": "Destination is locked after the first successful run; clone the task to change it"})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Sched.SyncTask(t); err != nil {
		s.Log.Error("schedule task", "task", t.ID, "error", err)
	}
	writeJSON(w, http.StatusOK, s.taskView(r.Context(), &t))
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, running := s.Runs.Active(id); running {
		writeError(w, http.StatusConflict, "conflict", "task is running; cancel it first")
		return
	}
	var t models.Task
	if err := s.DB.WithContext(r.Context()).First(&t, "id = ?", id).Error; err != nil {
		s.fail(w, err)
		return
	}
	s.Sched.RemoveTask(id)
	err := s.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var runIDs []string
		tx.Model(&models.TaskRun{}).Where("task_id = ?", id).Pluck("id", &runIDs)
		if len(runIDs) > 0 {
			if err := tx.Where("run_id IN ?", runIDs).Delete(&models.HindsightOperation{}).Error; err != nil {
				return err
			}
		}
		for _, m := range []any{&models.TaskRun{}, &models.TaskItem{}, &models.TaskState{}} {
			if err := tx.Where("task_id = ?", id).Delete(m).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&models.Task{}, "id = ?", id).Error
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("task deleted", "task", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runTask(mode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var t models.Task
		if err := s.DB.WithContext(r.Context()).First(&t, "id = ?", id).Error; err != nil {
			s.fail(w, err)
			return
		}
		runID, err := s.Runs.Start(id, models.TriggerManual, mode, nil, false)
		if errors.Is(err, runner.ErrAlreadyRunning) {
			writeError(w, http.StatusConflict, "conflict", "task is already running")
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"runId": runID})
	}
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	if !s.Runs.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusConflict, "conflict", "task is not running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"cancelled": true})
}

func (s *Server) validateCron(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CronExpression string `json:"cronExpression"`
		CronTimezone   string `json:"cronTimezone"`
	}
	if !decode(w, r, &b) {
		return
	}
	next, err := scheduler.ValidateCron(b.CronExpression, b.CronTimezone, 3)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error(), "nextRuns": []string{}})
		return
	}
	runs := make([]string, 0, len(next))
	for _, t := range next {
		runs = append(runs, t.Format(time.RFC3339))
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "error": "", "nextRuns": runs})
}

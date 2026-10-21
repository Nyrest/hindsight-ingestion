package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
)

type settingsDTO struct {
	settings.Settings
	OAuthRedirectURI string `json:"oauthRedirectUri"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Settings.Get(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO{v.Masked(), s.Cfg.OAuthRedirectURI()})
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Settings.Get(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	previous := v
	// Decoding custom groups can reuse slice storage; keep the comparison snapshot independent.
	previous.ObservationScope, err = v.ObservationScope.Normalize()
	if err != nil {
		s.fail(w, err)
		return
	}
	// Decoding into the current value applies a partial update.
	if !decode(w, r, &v) {
		return
	}
	v.Proxy = v.Proxy.Normalize()
	v.Timezone = strings.TrimSpace(v.Timezone)
	if v.Proxy.Password == proxy.Mask {
		v.Proxy.Password = previous.Proxy.Password
	}
	v.ObservationScope, err = v.ObservationScope.Normalize()
	if err != nil {
		writeValidation(w, err.Error(), map[string]string{"observationScope": err.Error()})
		return
	}
	if err := v.Validate(); err != nil {
		var fields map[string]string
		if errors.Is(err, settings.ErrInvalidTimezone) {
			fields = map[string]string{"timezone": "settings.timezone.invalid"}
		}
		writeValidation(w, err.Error(), fields)
		return
	}
	if err := s.saveSettings(r.Context(), previous, v); err != nil {
		s.fail(w, err)
		return
	}
	s.Runs.SetMaxConcurrentTasks(v.MaxConcurrentTasks)
	if previous.Timezone != v.Timezone {
		if err := s.Sched.LoadTasks(r.Context()); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, settingsDTO{v.Masked(), s.Cfg.OAuthRedirectURI()})
}

func (s *Server) saveSettings(ctx context.Context, previous, current settings.Settings) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.Settings.WithDB(tx).Put(ctx, current); err != nil {
			return err
		}
		reconcile := func(query *gorm.DB) error {
			return query.Updates(map[string]any{"reconcile_required": true, "config_revision": gorm.Expr("config_revision + 1")}).Error
		}
		if previous.Timezone != current.Timezone {
			if err := reconcile(tx.Model(&models.Task{}).Where("source_type IN ?", []string{"webcal", "caldav"})); err != nil {
				return err
			}
			tasks := tx.Model(&models.Task{}).Select("id").Where("source_type IN ?", []string{"webcal", "caldav"})
			if err := tx.Model(&models.TaskState{}).Where("task_id IN (?)", tasks).Updates(map[string]any{"committed_cursor_json": "", "last_full_reconcile_at": nil}).Error; err != nil {
				return err
			}
		}
		if !reflect.DeepEqual(previous.ObservationScope, current.ObservationScope) {
			if err := tx.Model(&models.Task{}).Where("observation_scope_mode = ? AND source_type IN ?", "global", []string{"notion", "siyuan"}).Updates(map[string]any{"reconcile_required": true, "policy_revision": gorm.Expr("policy_revision + 1"), "config_revision": gorm.Expr("config_revision + 1")}).Error; err != nil {
				return err
			}
		}
		if previous.FilePolicy != current.FilePolicy {
			if err := reconcile(tx.Model(&models.Task{}).Where("file_policy_mode = ?", models.FilePolicyGlobal)); err != nil {
				return err
			}
		}
		if previous.InlineMultimodalEnabled != current.InlineMultimodalEnabled {
			var sourceTypes []string
			for _, source := range connectors.Sources() {
				if source.Capabilities.SupportsInlineMultimodal {
					sourceTypes = append(sourceTypes, source.Type)
				}
			}
			if len(sourceTypes) > 0 {
				if err := reconcile(tx.Model(&models.Task{}).Where("inline_multimodal_mode = ? AND source_type IN ?", models.FilePolicyGlobal, sourceTypes)); err != nil {
					return err
				}
			}
		}
		if previous.MaxFileSizeMB != current.MaxFileSizeMB {
			return reconcile(tx.Model(&models.Task{}).Where("1 = 1"))
		}
		return nil
	})
}

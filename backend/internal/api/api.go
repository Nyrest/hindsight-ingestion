// Package api implements the REST API.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/config"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/oauth"
	"github.com/Nyrest/hindsight-ingestion/internal/runner"
	"github.com/Nyrest/hindsight-ingestion/internal/scheduler"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
)

// Server holds API dependencies.
type Server struct {
	Cfg      *config.Config
	DB       *gorm.DB
	Creds    *credentials.Service
	Settings *settings.Store
	OAuth    *oauth.Manager
	Runs     *runner.Manager
	Sched    *scheduler.Scheduler
	Log      *slog.Logger
}

// Routes registers all API routes on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	for _, route := range s.endpoints() {
		mux.HandleFunc(route.method+" "+route.path, route.handler)
	}
	mux.HandleFunc("GET /api/openapi.json", s.openAPI)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown API endpoint")
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	if sqlDB, err := s.DB.DB(); err != nil || sqlDB.PingContext(r.Context()) != nil {
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, healthDTO{status, httpx.Version, !s.Cfg.AuthDisabled, s.Cfg.DBType})
}

type apiError struct {
	Error  string            `json:"error"`
	Code   string            `json:"code"`
	Fields map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

func writeValidation(w http.ResponseWriter, msg string, fields map[string]string) {
	writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: msg, Code: "validation", Fields: fields})
}

// fail maps an error to a response.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var ve *credentials.ValidationError
	switch {
	case errors.As(err, &ve):
		writeValidation(w, ve.Message, ve.Fields)
	case errors.Is(err, credentials.ErrNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	default:
		s.Log.Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pageParams(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return
}

func timePtr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func timeStr(t time.Time) string { return t.UTC().Format(time.RFC3339) }

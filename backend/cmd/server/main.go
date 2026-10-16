// Command server runs the hindsight-ingestion service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/api"
	"github.com/Nyrest/hindsight-ingestion/internal/auth"
	"github.com/Nyrest/hindsight-ingestion/internal/config"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/email"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/sqlsource"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/steam"
	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/oauth"
	"github.com/Nyrest/hindsight-ingestion/internal/runner"
	"github.com/Nyrest/hindsight-ingestion/internal/scheduler"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
	"github.com/Nyrest/hindsight-ingestion/internal/web"

	// Source connectors register themselves.
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/cloudflare"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/filesystem"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/googledrive"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/notion"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/onedrive"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/s3"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/siyuan"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/webcal"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/webdav"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	log.Info("starting hindsight-ingestion", "version", httpx.Version, "auth", !cfg.AuthDisabled, "db", cfg.DBType)
	if cfg.AuthDisabled {
		log.Warn("authentication is DISABLED (DISABLE_AUTH=true); the WebUI and API are open to anyone who can reach them")
	}

	cipher, err := crypto.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}

	db, err := database.Open(cfg.DBType, cfg.DBDSN, log)
	if err != nil {
		return err
	}

	sqlConnector := &sqlsource.Connector{}
	if cfg.DBType == "sqlite" {
		sqlConnector.ApplicationSQLitePath = cfg.DBDSN
	}
	connectors.RegisterSource(sqlConnector)
	connectors.RegisterSource(&email.Connector{DB: db})
	connectors.RegisterSource(&steam.Connector{DB: db})

	creds := credentials.NewService(db, cipher)
	store := settings.NewStore(db, cipher)
	oauthMgr := oauth.NewManager(cfg, db, creds, log)
	engine := &sync.Engine{
		DB: db, Creds: creds, Settings: store, OAuth: oauthMgr, Log: log,
		NewDestination: func(c connectors.Credential) (sync.Destination, error) { return hindsight.NewClient(c) },
	}
	global, err := store.Get(context.Background())
	if err != nil {
		return err
	}
	if err := global.Validate(); err != nil {
		return err
	}
	runs := runner.NewManager(db, engine, global.MaxConcurrentTasks, log)

	ctx := context.Background()
	// Crash recovery: runs left active by a previous process.
	if n, err := runs.RecoverInterrupted(ctx); err != nil {
		return err
	} else if n > 0 {
		log.Warn("marked interrupted runs from previous process", "count", n)
	}

	sched, err := scheduler.New(db, runs, store, log)
	if err != nil {
		return err
	}
	oauthMgr.SetScheduler(sched)
	sched.SetRefreshFunc(func(ctx context.Context, id string) error {
		_, err := oauthMgr.Refresh(ctx, id)
		return err
	})

	if err := sched.LoadTasks(ctx); err != nil {
		return err
	}
	if err := oauthMgr.ScheduleAll(ctx); err != nil {
		return err
	}
	sched.Start()

	srv := &api.Server{Cfg: cfg, DB: db, Creds: creds, Settings: store, OAuth: oauthMgr, Runs: runs, Sched: sched, Log: log}
	mux := http.NewServeMux()
	srv.Routes(mux)
	mux.Handle("/", web.Handler())

	var handler http.Handler = auth.CSRFProtection(mux)
	if !cfg.AuthDisabled {
		public := func(r *http.Request) bool {
			// Health is unauthenticated for container health checks; the
			// OAuth callbacks validate browser-bound, expiring single-use state.
			return r.URL.Path == "/api/health" || r.URL.Path == "/api/oauth/callback"
		}
		handler = auth.BasicAuth(cfg.BasicAuthUsername, cfg.BasicAuthPassword, public)(handler)
	}
	handler = securityHeaders(recoverer(log, handler))

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s.String())
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpStopped := make(chan error, 1)
	go func() { httpStopped <- httpSrv.Shutdown(shutdownCtx) }()
	// Interrupt runs first: scheduled jobs block until their run returns, so
	// the scheduler can only stop promptly once runs are cancelled.
	runs.Shutdown(shutdownCtx)
	if err := sched.Shutdown(); err != nil {
		log.Warn("scheduler shutdown", "error", err)
	}
	if err := <-httpStopped; err != nil {
		log.Warn("http shutdown", "error", err)
		_ = httpSrv.Close()
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	log.Info("stopped")
	return nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic in handler", "path", r.URL.Path, "panic", p)
				if strings.HasPrefix(r.URL.Path, "/api/") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"internal server error","code":"internal"}`))
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

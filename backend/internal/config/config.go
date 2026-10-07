// Package config loads application configuration from environment variables.
package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Config is the process-wide configuration resolved at startup.
type Config struct {
	ListenAddr string
	PublicURL  string

	DBType string
	DBDSN  string

	AuthDisabled      bool
	BasicAuthUsername string
	BasicAuthPassword string

	EncryptionKey []byte

	MaxConcurrentTasks int
	LogLevel           slog.Level
}

// Load reads and validates the environment. Startup must fail on error.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:        env("LISTEN_ADDR", ":8080"),
		PublicURL:         strings.TrimRight(env("PUBLIC_URL", ""), "/"),
		DBType:            strings.ToLower(env("DB_TYPE", "sqlite")),
		DBDSN:             env("DB_DSN", ""),
		BasicAuthUsername: os.Getenv("BASIC_AUTH_USERNAME"),
		BasicAuthPassword: os.Getenv("BASIC_AUTH_PASSWORD"),
	}

	disabled, err := parseBool("DISABLE_AUTH", false)
	if err != nil {
		return nil, err
	}
	cfg.AuthDisabled = disabled
	if !cfg.AuthDisabled && (cfg.BasicAuthUsername == "" || cfg.BasicAuthPassword == "") {
		return nil, errors.New("BASIC_AUTH_USERNAME and BASIC_AUTH_PASSWORD are required unless DISABLE_AUTH=true")
	}

	switch cfg.DBType {
	case "sqlite":
		if cfg.DBDSN == "" {
			cfg.DBDSN = "/data/hindsight-ingestion.db"
		}
	case "postgres", "postgresql":
		cfg.DBType = "postgres"
		if cfg.DBDSN == "" {
			return nil, errors.New("DB_DSN is required for DB_TYPE=postgres")
		}
	case "mysql":
		if cfg.DBDSN == "" {
			return nil, errors.New("DB_DSN is required for DB_TYPE=mysql")
		}
	default:
		return nil, fmt.Errorf("unsupported DB_TYPE %q (use sqlite, postgres or mysql)", cfg.DBType)
	}

	key, err := parseKey(os.Getenv("CREDENTIAL_ENCRYPTION_KEY"))
	if err != nil {
		return nil, err
	}
	cfg.EncryptionKey = key

	cfg.MaxConcurrentTasks = 4
	if v := os.Getenv("MAX_CONCURRENT_TASKS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("MAX_CONCURRENT_TASKS must be a positive integer")
		}
		cfg.MaxConcurrentTasks = n
	}

	switch strings.ToLower(env("LOG_LEVEL", "info")) {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "info":
		cfg.LogLevel = slog.LevelInfo
	case "warn", "warning":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		return nil, fmt.Errorf("unsupported LOG_LEVEL %q", os.Getenv("LOG_LEVEL"))
	}
	return cfg, nil
}

// OAuthRedirectURI is the callback URL registered with OAuth providers.
func (c *Config) OAuthRedirectURI() string {
	base := c.PublicURL
	if base == "" {
		port := c.ListenAddr
		if strings.HasPrefix(port, ":") {
			port = "localhost" + port
		}
		base = "http://" + port
	}
	return base + "/api/oauth/callback"
}

// parseKey accepts a 32-byte key encoded as hex (64 chars), base64, or raw
// 32 characters. Any other non-empty value is stretched with SHA-256 so that
// operators can use a passphrase; empty is rejected.
func parseKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("CREDENTIAL_ENCRYPTION_KEY is required (32-byte secret; e.g. `openssl rand -hex 32`)")
	}
	if len(raw) == 64 {
		if b, err := hex.DecodeString(raw); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(raw); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	if len(raw) < 16 {
		return nil, errors.New("CREDENTIAL_ENCRYPTION_KEY is too short; use a 32-byte secret")
	}
	sum := sha256.Sum256([]byte(raw))
	return sum[:], nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseBool(key string, fallback bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return b, nil
}

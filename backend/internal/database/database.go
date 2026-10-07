// Package database opens the configured GORM database and runs migrations.
package database

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

// Open connects to the database and applies migrations.
func Open(dbType, dsn string, log *slog.Logger) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch dbType {
	case "sqlite":
		if dir := filepath.Dir(sqlitePath(dsn)); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("create sqlite directory: %w", err)
			}
		}
		dialector = sqlite.Open(sqliteDSN(dsn))
	case "postgres":
		dialector = postgres.Open(dsn)
	case "mysql":
		dialector = mysql.Open(dsn)
	default:
		return nil, fmt.Errorf("unsupported database type %q", dbType)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Silent),
		DisableForeignKeyConstraintWhenMigrating: true,
		NowFunc:                                  func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", dbType, err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if dbType == "sqlite" {
		// SQLite allows a single writer; serialize through one connection.
		sqlDB.SetMaxOpenConns(1)
	} else {
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}

	if err := Migrate(db); err != nil {
		return nil, err
	}
	log.Info("database ready", "type", dbType)
	return db, nil
}

// Migrate applies schema migrations.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(models.All()...); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}

func sqlitePath(dsn string) string {
	p := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return p
}

// sqliteDSN adds pragmas for durability and concurrency unless already set.
func sqliteDSN(dsn string) string {
	if dsn == ":memory:" || strings.Contains(dsn, "_journal") || strings.Contains(dsn, "_busy") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_journal_mode=WAL&_busy_timeout=10000&_synchronous=NORMAL&_txlock=immediate"
}

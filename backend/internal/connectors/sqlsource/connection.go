package sqlsource

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/mattn/go-sqlite3"
)

func tlsConfig(cred connectors.Credential) (*tls.Config, error) {
	switch cred.String("tlsMode") {
	case "disable":
		return nil, nil
	case "verify-full":
	default:
		return nil, errors.New("TLS mode must be disable or verify-full")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cred.String("host")}
	if pem := cred.String("tlsCA"); pem != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM([]byte(pem)) {
			return nil, errors.New("invalid CA certificate")
		}
		cfg.RootCAs = roots
	}
	return cfg, nil
}

func (c *Connector) open(cred connectors.Credential) (*sql.DB, error) {
	var dc driver.Connector
	switch cred.Type {
	case MySQL, Postgres:
		port, err := strconv.Atoi(cred.String("port"))
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("port must be between 1 and 65535")
		}
		for _, key := range []string{"host", "database", "username"} {
			if strings.TrimSpace(cred.String(key)) == "" {
				return nil, fmt.Errorf("%s is required", key)
			}
		}
		tlsCfg, err := tlsConfig(cred)
		if err != nil {
			return nil, err
		}
		addr := net.JoinHostPort(cred.String("host"), strconv.Itoa(port))
		if cred.Type == MySQL {
			cfg := mysql.NewConfig()
			cfg.Net = "tcp"
			cfg.Addr = addr
			cfg.User = cred.String("username")
			cfg.Passwd = cred.String("password")
			cfg.DBName = cred.String("database")
			cfg.TLS = tlsCfg
			cfg.ParseTime = true
			cfg.Timeout = 15 * time.Second
			dc, err = mysql.NewConnector(cfg)
			if err != nil {
				return nil, errors.New("invalid MySQL connection configuration")
			}
		} else {
			// Parse only generated, non-secret URL fields; passwords never enter errors or DSNs.
			u := url.URL{Scheme: "postgres", Host: addr, Path: "/" + cred.String("database"), RawQuery: "sslmode=disable"}
			cfg, err := pgx.ParseConfig(u.String())
			if err != nil {
				return nil, errors.New("invalid PostgreSQL connection configuration")
			}
			cfg.User = cred.String("username")
			cfg.Password = cred.String("password")
			cfg.TLSConfig = tlsCfg
			cfg.Fallbacks = nil
			cfg.ConnectTimeout = 15 * time.Second
			dc = stdlib.GetConnector(*cfg)
		}
	case SQLite:
		path := cred.String("path")
		if !filepath.IsAbs(path) {
			return nil, errors.New("SQLite requires an absolute server file path")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("SQLite file must exist and be a regular file")
		}
		if c.ApplicationSQLitePath != "" {
			appPath := SQLiteFilePath(c.ApplicationSQLitePath)
			app, err := os.Stat(appPath)
			if err == nil && os.SameFile(info, app) {
				return nil, errors.New("the application's SQLite database cannot be used as a source")
			}
			resolved, err := filepath.Abs(appPath)
			if err == nil && filepath.Clean(resolved) == filepath.Clean(path) {
				return nil, errors.New("the application's SQLite database cannot be used as a source")
			}
		}
		uriPath := filepath.ToSlash(path)
		if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
			uriPath = "/" + uriPath
		}
		u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&_query_only=1&_busy_timeout=5000"}
		d := &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			conn.RegisterAuthorizer(func(op int, _, arg2, _ string) int {
				switch op {
				case sqlite3.SQLITE_SELECT, sqlite3.SQLITE_READ, sqliteRecursive, sqlite3.SQLITE_TRANSACTION:
					return sqlite3.SQLITE_OK
				case sqlite3.SQLITE_FUNCTION:
					if strings.EqualFold(arg2, "load_extension") {
						return sqlite3.SQLITE_DENY
					}
					return sqlite3.SQLITE_OK
				default:
					return sqlite3.SQLITE_DENY
				}
			})
			return nil
		}}
		dc = sqliteConnector{driver: d, dsn: u.String()}
	default:
		return nil, errors.New("credential is not a SQL database credential")
	}
	db := sql.OpenDB(dc)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	return db, nil
}

// SQLiteFilePath extracts a file path from the application's SQLite DSN.
func SQLiteFilePath(dsn string) string {
	if strings.HasPrefix(dsn, "file:") {
		u, err := url.Parse(dsn)
		if err == nil {
			path := u.Path
			if path == "" {
				path = u.Opaque
			}
			if len(path) > 3 && path[0] == '/' && path[2] == ':' {
				path = path[1:]
			}
			return filepath.FromSlash(path)
		}
	}
	return strings.SplitN(dsn, "?", 2)[0]
}

// sqliteRecursive is SQLITE_RECURSIVE (33), omitted from the Go driver constants.
const sqliteRecursive = 33

type sqliteConnector struct {
	driver *sqlite3.SQLiteDriver
	dsn    string
}

func (c sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.driver.Open(c.dsn)
}
func (c sqliteConnector) Driver() driver.Driver { return c.driver }

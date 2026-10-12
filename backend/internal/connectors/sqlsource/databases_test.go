package sqlsource

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

// These databases are disposable test instances, supplied by CI services.
func TestNetworkDatabases(t *testing.T) {
	for _, typ := range []string{MySQL, Postgres} {
		t.Run(typ, func(t *testing.T) {
			env := map[string]string{MySQL: "SQLSOURCE_TEST_MYSQL_DSN", Postgres: "SQLSOURCE_TEST_POSTGRES_DSN"}[typ]
			dsn := os.Getenv(env)
			if dsn == "" {
				t.Skip(env + " is not configured")
			}
			var cred connectors.Credential
			var setup *sql.DB
			var err error
			if typ == MySQL {
				cfg, e := mysql.ParseDSN(dsn)
				if e != nil {
					t.Fatal("invalid MySQL test DSN")
				}
				// The test fixture never prints a DSN or password.
				host, port, e := splitAddress(cfg.Addr)
				if e != nil {
					t.Fatal(e)
				}
				cred = connectors.Credential{Type: typ, Config: map[string]any{"host": host, "port": port, "database": cfg.DBName, "username": cfg.User, "tlsMode": "disable"}, Secrets: map[string]string{"password": cfg.Passwd}}
				setup, err = sql.Open("mysql", dsn)
			} else {
				cfg, e := pgx.ParseConfig(dsn)
				if e != nil {
					t.Fatal("invalid PostgreSQL test DSN")
				}
				cred = connectors.Credential{Type: typ, Config: map[string]any{"host": cfg.Host, "port": int(cfg.Port), "database": cfg.Database, "username": cfg.User, "tlsMode": "disable"}, Secrets: map[string]string{"password": cfg.Password}}
				setup, err = sql.Open("pgx", dsn)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer setup.Close()
			table := fmt.Sprintf("sql_source_%d", time.Now().UnixNano())
			ddl := `(id BIGINT PRIMARY KEY, body TEXT, context TEXT, amount DECIMAL(38,18), payload JSON, blob_value BYTEA)`
			blobExpr := `decode('00ff','hex')`
			if typ == MySQL {
				ddl = `(id BIGINT UNSIGNED PRIMARY KEY, body TEXT, context TEXT, amount DECIMAL(38,18), payload JSON, blob_value BLOB)`
				blobExpr = `UNHEX('00ff')`
			}
			if _, err = setup.Exec("CREATE TABLE " + table + ddl); err != nil {
				t.Fatal(err)
			}
			defer setup.Exec("DROP TABLE " + table)
			q := fmt.Sprintf(`INSERT INTO %s VALUES(9223372036854775807,'Unicode 用户','context',12345678901234567890.123456789012345678,'{"n":9007199254740993}',%s)`, table, blobExpr)
			if _, err = setup.Exec(q); err != nil {
				t.Fatal(err)
			}
			c := &Connector{}
			if _, err = c.ValidateCredential(t.Context(), cred); err != nil {
				t.Fatal(err)
			}
			a, r := scan(t, c, cred, `WITH selected AS (SELECT * FROM `+table+`) SELECT id AS __id,body AS __content,context AS __context FROM selected`, RowsMode)
			if r.Error != nil || !r.Complete || len(a) != 1 {
				t.Fatalf("%+v", r)
			}
			a, r = scan(t, c, cred, `SELECT id,amount,payload,blob_value FROM `+table, QueryMode)
			if r.Error != nil || !r.Complete || len(a) != 1 {
				t.Fatalf("%+v", r)
			}
			doc, err := c.OpenContent(t.Context(), connectors.ContentRequest{Item: a[0]})
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{`"9223372036854775807"`, `"12345678901234567890.123456789012345678"`, `9007199254740993`, `"AP8="`} {
				if !strings.Contains(doc.Text, expected) {
					t.Fatalf("precision/type lost: %s want %s", doc.Text, expected)
				}
			}
			for _, bad := range []string{
				"SELECT amount AS __id,body AS __content FROM " + table,
				"SELECT id AS __id,amount AS __content FROM " + table,
				"SELECT id AS __id,body AS __content,payload AS __context FROM " + table,
			} {
				_, res := scan(t, c, cred, bad, RowsMode)
				if res.Error == nil || res.Complete {
					t.Fatalf("invalid native column type accepted: %s", bad)
				}
			}
			if typ == MySQL {
				if _, err = setup.Exec(`INSERT INTO ` + table + `(id,body) VALUES(18446744073709551615,'unsigned')`); err != nil {
					t.Fatal(err)
				}
				a, r = scan(t, c, cred, `SELECT id AS __id,body AS __content FROM `+table+` ORDER BY id`, RowsMode)
				if r.Error != nil || len(a) != 2 {
					t.Fatalf("unsigned IDs: %+v", r)
				}
			}
			// Exercise the actual transaction guard independently of statement validation.
			ro, err := c.open(cred)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := ro.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(t.Context(), `DELETE FROM `+table); err == nil {
				t.Fatal("read-only transaction accepted DELETE")
			}
			tx.Rollback()
			if ro.Stats().OpenConnections != 0 {
				t.Fatal("connection was not closed", ro.Stats())
			}
			ro.Close()
			if typ == Postgres {
				_, r = scan(t, c, cred, `WITH deleted AS (DELETE FROM `+table+` RETURNING id) SELECT id FROM deleted`, QueryMode)
				if r.Error == nil || r.Complete {
					t.Fatal("data-modifying CTE accepted")
				}
			}
			slow := `SELECT pg_sleep(10)`
			if typ == MySQL {
				slow = `SELECT SLEEP(10)`
			}
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			result, err := c.Scan(ctx, connectors.ScanRequest{Credential: cred, Config: map[string]any{"query": slow, "documentMode": QueryMode}, Emit: func(connectors.SourceItem) error { t.Fatal("cancelled query emitted snapshot"); return nil }})
			cancel()
			if err != nil || result.Error == nil || result.Complete {
				t.Fatal("cancellation did not fail", result, err)
			}
			if _, err = c.ValidateCredential(t.Context(), cred); err != nil {
				t.Fatal("connection after cancellation", err)
			}
			if caFile := os.Getenv("SQLSOURCE_TEST_CA"); caFile != "" {
				ca, err := os.ReadFile(caFile)
				if err != nil {
					t.Fatal(err)
				}
				cred.Config["tlsMode"] = "verify-full"
				cred.Config["tlsCA"] = string(ca)
				if _, err = c.ValidateCredential(t.Context(), cred); err != nil {
					t.Fatal("verified TLS failed", err)
				}
				cred.Config["tlsCA"] = "" // The private test CA is not in the system trust store.
				if _, err = c.ValidateCredential(t.Context(), cred); err == nil {
					t.Fatal("untrusted TLS certificate accepted")
				}
			} else {
				t.Log("verified TLS integration not configured; set SQLSOURCE_TEST_CA")
			}
		})
	}
}

func splitAddress(addr string) (string, int, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(port)
	return host, n, err
}

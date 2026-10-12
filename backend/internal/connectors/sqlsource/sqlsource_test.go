package sqlsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func sqliteFixture(t testing.TB) (*sql.DB, connectors.Credential) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE articles(id INTEGER PRIMARY KEY, body TEXT, context TEXT, extra TEXT); INSERT INTO articles VALUES(1,' 你好\n','上下文','ignored'),(2,'second',NULL,'ignored')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, connectors.Credential{Type: SQLite, Config: map[string]any{"path": path}}
}

func scan(t testing.TB, c *Connector, cred connectors.Credential, query, mode string) ([]connectors.SourceItem, connectors.ScanResult) {
	t.Helper()
	var items []connectors.SourceItem
	res, err := c.Scan(context.Background(), connectors.ScanRequest{Credential: cred, Config: map[string]any{"query": query, "documentMode": mode}, Emit: func(it connectors.SourceItem) error { items = append(items, it); return nil }})
	if err != nil {
		t.Fatalf("scan returned immediate error instead of deferred failure: %v", err)
	}
	return items, res
}

func TestSQLiteRowsAndRevisions(t *testing.T) {
	db, cred := sqliteFixture(t)
	c := &Connector{}
	query := `WITH selected AS (SELECT * FROM articles) SELECT id AS __id,body AS __content,context AS __context,extra FROM selected ORDER BY id`
	a, res := scan(t, c, cred, query, RowsMode)
	if res.Error != nil || !res.Complete || len(a) != 2 {
		t.Fatalf("%+v, items=%d", res, len(a))
	}
	content, err := c.OpenContent(t.Context(), connectors.ContentRequest{Item: a[0]})
	if err != nil || content.Text != " 你好\\n" || content.Context != "上下文" {
		t.Fatalf("content=%+v err=%v", content, err)
	}
	_, err = db.Exec(`UPDATE articles SET extra='changed'`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := scan(t, c, cred, query, RowsMode)
	if a[0].Revision != b[0].Revision {
		t.Fatal("extra column changed revision")
	}
	_, err = db.Exec(`UPDATE articles SET context='new context' WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := scan(t, c, cred, query, RowsMode)
	if a[0].ID != d[0].ID || a[0].Revision == d[0].Revision {
		t.Fatal("context change must update same document")
	}
	// Opaque remains usable even after the database is removed.
	db.Close()
	if err = os.Remove(cred.String("path")); err != nil {
		t.Fatal(err)
	}
	if _, err = c.OpenContent(t.Context(), connectors.ContentRequest{Item: a[0]}); err != nil {
		t.Fatal(err)
	}
}

func TestRowValidation(t *testing.T) {
	_, cred := sqliteFixture(t)
	tests := []struct{ query, want string }{
		{`SELECT 'body' AS __content`, "requires"},
		{`SELECT 1 AS __id,2 AS __id,'body' AS __content`, "duplicate reserved"},
		{`SELECT 1 AS __id,'body' AS __content,'x' AS __context,'y' AS __context`, "duplicate reserved"},
		{`SELECT NULL AS __id,'body' AS __content`, "__id"},
		{`SELECT '' AS __id,'body' AS __content`, "__id"},
		{`SELECT 1.5 AS __id,'body' AS __content`, "__id"},
		{`SELECT x'6162' AS __id,'body' AS __content`, "__id"},
		{`SELECT 1 AS __id,NULL AS __content`, "__content"},
		{`SELECT 1 AS __id,'  ' AS __content`, "__content"},
		{`SELECT 1 AS __id,123 AS __content`, "__content"},
		{`SELECT 1 AS __id,x'6162' AS __content`, "__content"},
		{`SELECT 1 AS __id,'body' AS __content,123 AS __context`, "__context"},
		{`SELECT 1 AS __id,'body' AS __content UNION ALL SELECT 1,'other'`, "duplicate __id"},
	}
	for _, tt := range tests {
		t.Run(tt.want+tt.query, func(t *testing.T) {
			_, r := scan(t, &Connector{}, cred, tt.query, RowsMode)
			if r.Error == nil || r.Complete || !strings.Contains(r.Error.Error(), tt.want) {
				t.Fatalf("%+v want %s", r, tt.want)
			}
		})
	}
	for _, q := range []string{
		`SELECT 9223372036854775807 AS __id,'body' AS __content`,
		`SELECT '18446744073709551615' AS __id,'body' AS __content`,
		`SELECT json_array('a:b','用户') AS __id,'body' AS __content`,
		`SELECT 1 AS __id,'body' AS __content,NULL AS __context`,
	} {
		a, r := scan(t, &Connector{}, cred, q, RowsMode)
		if r.Error != nil || len(a) != 1 {
			t.Fatalf("%s: %+v", q, r)
		}
	}
	if a, _ := normalizeID(int64(1), ""); a == "string:1" {
		t.Fatal("integer and string identity conflated")
	}
}

func TestQuerySnapshotAndNormalization(t *testing.T) {
	_, cred := sqliteFixture(t)
	c := &Connector{}
	query := `SELECT 9223372036854775807 AS duplicate,NULL AS duplicate,x'00ff' AS blob UNION ALL SELECT 9223372036854775807,NULL,x'00ff'`
	a, r := scan(t, c, cred, query, QueryMode)
	if r.Error != nil || len(a) != 1 {
		t.Fatalf("%+v", r)
	}
	content, err := c.OpenContent(t.Context(), connectors.ContentRequest{Item: a[0]})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"columns":["duplicate","duplicate","blob"],"rows":[["9223372036854775807",null,"AP8="],["9223372036854775807",null,"AP8="]]}`
	if content.Text != want {
		t.Fatalf("%s\nwant %s", content.Text, want)
	}
	empty, r := scan(t, c, cred, `SELECT id,body FROM articles WHERE 0`, QueryMode)
	if r.Error != nil || len(empty) != 1 {
		t.Fatalf("%+v", r)
	}
	text, _ := c.OpenContent(t.Context(), connectors.ContentRequest{Item: empty[0]})
	if text.Text != `{"columns":["id","body"],"rows":[]}` {
		t.Fatal(text.Text)
	}
	precise, err := normalizeValue([]byte(`{"n":123456789012345678901234567890,"d":0.1234567890123456789}`), "JSONB")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(precise)
	if !strings.Contains(string(b), "123456789012345678901234567890") || !strings.Contains(string(b), "0.1234567890123456789") {
		t.Fatal(string(b))
	}
	now := time.Date(2026, 10, 12, 1, 2, 3, 123456789, time.UTC)
	v, _ := normalizeValue(now, "TIMESTAMP")
	if v != now.Format(time.RFC3339Nano) {
		t.Fatal(v)
	}
	decimal, _ := normalizeValue([]byte("12345678901234567890.123456789"), "DECIMAL")
	if decimal != "12345678901234567890.123456789" {
		t.Fatal(decimal)
	}
}

func TestQueryBoundaries(t *testing.T) {
	if err := ValidateQuery(`SELECT '\' AS value`, ""); err != nil {
		t.Fatal("valid PostgreSQL/SQLite literal rejected before credential selection", err)
	}
	for _, q := range []string{
		`SELECT ';--/*' AS value; -- trailing`,
		`/* before */ WITH c AS (SELECT 1) SELECT * FROM c`,
		`SELECT $$a;b$$, $tag$a;'b$tag$;`,
		`SELECT $日本$a;b$日本$;`,
		`SELECT E'a\';b';`,
		`SELECT 1 /* outer /* nested */ comment */;`,
	} {
		if err := ValidateQuery(q, Postgres); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	for _, q := range []string{`SELECT 1; SELECT 2`, `SELECT 1;;`, `ATTACH 'x' AS other`, `PRAGMA query_only=0`, `UPDATE x SET y=1`, `SELECT 'unterminated`, `SELECT /* open`, `SELECT $$open`, `/*! SELECT */ SELECT 1`, `SELECT 1 /*M! ;DROP TABLE x */`} {
		if err := ValidateQuery(q, Postgres); err == nil {
			t.Errorf("accepted %s", q)
		}
	}
	if err := ValidateQuery(`SELECT 'a\';b'`, SQLite); err == nil {
		t.Fatal("SQLite backslash must not hide statement separator")
	}
}

func TestSQLiteProtectionAndConnectionClosure(t *testing.T) {
	db, cred := sqliteFixture(t)
	c := &Connector{ApplicationSQLitePath: cred.String("path")}
	if _, err := c.ValidateCredential(t.Context(), cred); err == nil {
		t.Fatal("application DB accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias.db")
	if err := os.Link(cred.String("path"), alias); err == nil {
		other := cred
		other.Config = map[string]any{"path": alias}
		if _, err = c.open(other); err == nil {
			t.Fatal("hard-linked application DB accepted")
		}
	}
	c = &Connector{}
	source, err := c.open(cred)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := source.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`PRAGMA query_only=OFF`, `ATTACH ':memory:' AS other`, `CREATE TEMP TABLE x(a)`, `DELETE FROM articles`, `SELECT load_extension('x')`} {
		if _, err = conn.ExecContext(t.Context(), q); err == nil {
			t.Fatalf("mutation accepted: %s", q)
		}
	}
	conn.Close()
	if source.Stats().OpenConnections != 0 {
		t.Fatalf("connection retained: %+v", source.Stats())
	}
	source.Close()
	if _, err = db.Exec(`UPDATE articles SET extra='still writable externally'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	r, err := c.Scan(ctx, connectors.ScanRequest{Credential: cred, Config: map[string]any{"documentMode": QueryMode, "query": `WITH RECURSIVE cnt(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM cnt WHERE x<1000000000) SELECT sum(x) FROM cnt`}, Emit: func(connectors.SourceItem) error { t.Fatal("interrupted query emitted document"); return nil }})
	if err != nil || r.Error == nil || r.Complete {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err = c.ValidateCredential(t.Context(), cred); err != nil {
		t.Fatal(err)
	}
}

func TestLimitsAndPreview(t *testing.T) {
	_, cred := sqliteFixture(t)
	c := &Connector{}
	q := `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100001) SELECT x AS __id,'x' AS __content FROM n`
	count := 0
	r, err := c.Scan(t.Context(), connectors.ScanRequest{Credential: cred, Config: map[string]any{"documentMode": RowsMode, "query": q}, Emit: func(connectors.SourceItem) error { count++; return nil }})
	if err != nil || r.Error == nil || r.Complete || count != MaxRows {
		t.Fatalf("count=%d %+v %v", count, r, err)
	}
	for _, mode := range []string{RowsMode, QueryMode} {
		out, err := c.Preview(t.Context(), cred, PreviewRequest{Query: q, DocumentMode: mode})
		if err != nil || !out.Truncated {
			t.Fatalf("%s: %+v %v", mode, out, err)
		}
		encoded, _ := json.Marshal(out)
		if len(encoded) > PreviewBytes {
			t.Fatal("oversized preview")
		}
		if mode == RowsMode && len(out.Documents) != 100 {
			t.Fatal(len(out.Documents))
		}
	}
	for _, mode := range []string{RowsMode, QueryMode} {
		items, res := scan(t, c, cred, `SELECT 1 AS __id,replace(hex(zeroblob(2097153)),'0','x') AS __content`, mode)
		if res.Error == nil || len(items) != 0 {
			t.Fatalf("oversized %s: %d %+v", mode, len(items), res)
		}
	}
	q = `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<17) SELECT x AS __id,hex(zeroblob(524288)) AS __content FROM n`
	_, res := scan(t, c, cred, q, RowsMode)
	if res.Error == nil || !strings.Contains(res.Error.Error(), "16 MiB") {
		t.Fatalf("%+v", res)
	}
	// Full scans validate duplicates beyond the preview window.
	q = `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<101) SELECT CASE WHEN x=101 THEN 1 ELSE x END AS __id,'x' AS __content FROM n`
	if out, err := c.Preview(t.Context(), cred, PreviewRequest{Query: q, DocumentMode: RowsMode}); err != nil || !out.Truncated {
		t.Fatalf("%+v %v", out, err)
	}
	_, res = scan(t, c, cred, q, RowsMode)
	if res.Error == nil || !strings.Contains(res.Error.Error(), "duplicate") {
		t.Fatal(res)
	}
}

func TestEmitFailureIsDeferred(t *testing.T) {
	_, cred := sqliteFixture(t)
	want := errors.New("destination rejected")
	r, err := (&Connector{}).Scan(t.Context(), connectors.ScanRequest{Credential: cred, Config: map[string]any{"documentMode": RowsMode, "query": `SELECT id AS __id,body AS __content FROM articles`}, Emit: func(connectors.SourceItem) error { return want }})
	if err != nil || !errors.Is(r.Error, want) || r.Complete {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestConnectionTarget(t *testing.T) {
	before := map[string]any{"host": "localhost", "port": 5432, "database": "docs", "username": "reader", "password": "old", "tlsMode": "disable"}
	after := map[string]any{"host": "localhost", "port": float64(5432), "database": "docs", "username": "reader", "password": "new", "tlsMode": "verify-full"}
	if ConnectionTarget(Postgres, before) != ConnectionTarget(Postgres, after) {
		t.Fatal("password or TLS rotation changed baseline")
	}
	after["database"] = "other"
	if ConnectionTarget(Postgres, before) == ConnectionTarget(Postgres, after) {
		t.Fatal("database target unchanged")
	}
}

func TestTLSConfig(t *testing.T) {
	if cfg, err := tlsConfig(connectors.Credential{Config: map[string]any{"tlsMode": "disable"}}); err != nil || cfg != nil {
		t.Fatal(cfg, err)
	}
	cfg, err := tlsConfig(connectors.Credential{Config: map[string]any{"tlsMode": "verify-full", "host": "db.example"}})
	if err != nil || cfg.InsecureSkipVerify || cfg.ServerName != "db.example" {
		t.Fatal(cfg, err)
	}
	if _, err = tlsConfig(connectors.Credential{Config: map[string]any{"tlsMode": "verify-full", "tlsCA": "invalid"}}); err == nil {
		t.Fatal("invalid CA accepted")
	}
}

func BenchmarkScan100k(b *testing.B) {
	_, cred := sqliteFixture(b)
	q := `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100000) SELECT x AS __id,'short body' AS __content FROM n`
	b.ReportAllocs()
	for b.Loop() {
		count := 0
		var before, peak runtime.MemStats
		runtime.ReadMemStats(&before)
		res, err := (&Connector{}).Scan(context.Background(), connectors.ScanRequest{Credential: cred, Config: map[string]any{"query": q, "documentMode": RowsMode}, Emit: func(connectors.SourceItem) error {
			count++
			if count%10000 == 0 {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.HeapAlloc {
					peak = m
				}
			}
			return nil
		}})
		if err != nil || res.Error != nil || !res.Complete || count != 100000 {
			b.Fatalf("%+v %v count=%d", res, err, count)
		}
		if peak.HeapAlloc > before.HeapAlloc {
			b.ReportMetric(float64(peak.HeapAlloc-before.HeapAlloc), "peak-extra-heap-B")
		}
	}
}

func TestNormalizationArrayOrder(t *testing.T) {
	value, err := normalizeValue(`[{"n":9007199254740993},null]`, "JSON")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value, []any{map[string]any{"n": json.Number("9007199254740993")}, nil}) {
		t.Fatal(value)
	}
}

func TestNativeColumnTypesCannotMasqueradeAsText(t *testing.T) {
	for _, typ := range []string{"NUMERIC", "DECIMAL", "JSON", "JSONB", "TIME", "BIT", "BOOLEAN", "BYTEA", "FLOAT", "DOUBLE"} {
		if _, err := columnText([]byte("123"), typ, MySQL); err == nil {
			t.Errorf("accepted __content/%s as text", typ)
		}
		if _, err := normalizeID([]byte("123"), typ); err == nil {
			t.Errorf("accepted __id/%s as string", typ)
		}
	}
	if v, err := normalizeID([]byte("18446744073709551615"), "UNSIGNED BIGINT"); err != nil || v != "integer:18446744073709551615" {
		t.Fatal(v, err)
	}
}

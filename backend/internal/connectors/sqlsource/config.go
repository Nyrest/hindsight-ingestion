package sqlsource

import (
	"context"
	"errors"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

const (
	SourceType       = "sql"
	MySQL            = "sql_mysql"
	Postgres         = "sql_postgres"
	SQLite           = "sql_sqlite"
	RowsMode         = "rows"
	QueryMode        = "query"
	MaxRows          = 100_000
	MaxOutputBytes   = 16 << 20
	MaxDocumentBytes = 4 << 20
	PreviewRows      = 100
	PreviewBytes     = 4 << 20
)

type Connector struct{ ApplicationSQLitePath string }

func IsCredential(typ string) bool { return typ == MySQL || typ == Postgres || typ == SQLite }

func init() {
	for _, spec := range []struct {
		typ, name string
		port      int
	}{{MySQL, "MySQL", 3306}, {Postgres, "PostgreSQL", 5432}} {
		connectors.RegisterCredentialType(connectors.CredentialType{
			Type: spec.typ, Name: spec.name, Description: spec.name + " database connection",
			Fields: []connectors.FieldSpec{
				{Key: "host", Label: "Host", Type: connectors.FieldString, Required: true},
				{Key: "port", Label: "Port", Type: connectors.FieldNumber, Required: true, Default: spec.port},
				{Key: "database", Label: "Database", Type: connectors.FieldString, Required: true},
				{Key: "username", Label: "Username", Type: connectors.FieldString, Required: true},
				{Key: "password", Label: "Password", Type: connectors.FieldPassword, Secret: true},
				{Key: "tlsMode", Label: "TLS mode", Type: connectors.FieldSelect, Required: true, Default: "verify-full", Options: []connectors.Option{{Value: "disable", Label: "Disabled"}, {Value: "verify-full", Label: "Verify certificate and hostname"}}},
				{Key: "tlsCA", Label: "CA certificate (PEM)", Type: connectors.FieldTextarea},
			},
		})
	}
	connectors.RegisterCredentialType(connectors.CredentialType{Type: SQLite, Name: "SQLite", Description: "SQLite database file on the server", Fields: []connectors.FieldSpec{{Key: "path", Label: "Server file path", Type: connectors.FieldString, Required: true}}})
	connectors.RegisterSource(&Connector{})
}

func (*Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type: SourceType, Name: "SQL", CredentialTypes: []string{MySQL, Postgres, SQLite},
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalInventory, DeletionMode: connectors.DeletionFullReconcile},
		BrowseKinds:  []string{}, FilterFields: []connectors.FilterFieldSpec{},
		Fields: []connectors.FieldSpec{
			{Key: "documentMode", Label: "Document mode", Type: connectors.FieldSelect, Required: true, Default: RowsMode, Effect: connectors.EffectRebaseline, Options: []connectors.Option{{Value: RowsMode, Label: "One document per row"}, {Value: QueryMode, Label: "Entire query as one document"}}},
			{Key: "query", Label: "SQL query", Type: connectors.FieldTextarea, Required: true, Effect: connectors.EffectRebaseline},
		},
	}
}

func (*Connector) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	if mode := connectors.AsString(cfg["documentMode"]); mode != RowsMode && mode != QueryMode {
		return &connectors.FieldError{Field: "documentMode", Message: "must be rows or query"}
	}
	if err := ValidateQuery(connectors.AsString(cfg["query"]), ""); err != nil {
		return &connectors.FieldError{Field: "query", Message: err.Error()}
	}
	return connectors.ValidateFilter(filter, nil, false)
}

func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	db, err := c.open(cred)
	if err != nil {
		return "", err
	}
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		return "", errors.New("SQL connection failed: " + redact(err.Error(), cred))
	}
	return "Connected to " + cred.Type, nil
}

func (*Connector) Browse(context.Context, connectors.Credential, connectors.BrowseRequest) (connectors.BrowseResult, error) {
	return connectors.BrowseResult{}, errors.New("SQL sources do not support browsing")
}

// ConnectionTarget excludes secrets and transport settings so password rotation keeps the baseline.
func ConnectionTarget(typ string, cfg map[string]any) string {
	keys := []string{"host", "port", "database", "username"}
	if typ == SQLite {
		keys = []string{"path"}
	}
	var b strings.Builder
	for _, k := range keys {
		v := connectors.AsString(cfg[k])
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte(0)
	}
	return b.String()
}

func redact(msg string, cred connectors.Credential) string {
	for _, s := range cred.Secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	return msg
}

type redactedError struct {
	cause   error
	message string
}

func (e redactedError) Error() string { return e.message }
func (e redactedError) Unwrap() error { return e.cause }
func redactError(err error, cred connectors.Credential) error {
	message := redact(err.Error(), cred)
	if message == err.Error() {
		return err
	}
	return redactedError{cause: err, message: message}
}

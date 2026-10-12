package sqlsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

type PreviewRequest struct {
	Query        string `json:"query"`
	DocumentMode string `json:"documentMode" enum:"rows,query"`
}

type PreviewResult struct {
	Columns   []string   `json:"columns"`
	Documents []Document `json:"documents"`
	Truncated bool       `json:"truncated"`
}

func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	_, err := c.read(ctx, req.Credential, PreviewRequest{Query: connectors.AsString(req.Config["query"]), DocumentMode: connectors.AsString(req.Config["documentMode"])}, false, req.Emit)
	if err != nil {
		return connectors.ScanResult{Error: err}, nil
	}
	return connectors.ScanResult{Complete: true, Cursor: json.RawMessage(`{"inventory":true}`)}, nil
}

func (c *Connector) Preview(ctx context.Context, cred connectors.Credential, req PreviewRequest) (PreviewResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.read(ctx, cred, req, true, nil)
}

func (c *Connector) read(ctx context.Context, cred connectors.Credential, req PreviewRequest, preview bool, emit func(connectors.SourceItem) error) (PreviewResult, error) {
	if req.DocumentMode != RowsMode && req.DocumentMode != QueryMode {
		return PreviewResult{}, errors.New("documentMode must be rows or query")
	}
	if err := ValidateQuery(req.Query, cred.Type); err != nil {
		return PreviewResult{}, err
	}
	rows, closeQuery, err := c.openQuery(ctx, cred, req.Query)
	if err != nil {
		return PreviewResult{}, err
	}
	defer closeQuery()
	reader, err := newResultReader(rows, req.DocumentMode, cred.Type, preview, emit)
	if err != nil {
		return PreviewResult{}, err
	}
	if err = reader.readRows(ctx, rows); err != nil {
		return reader.out, redactError(err, cred)
	}
	out, err := reader.finish()
	if err == nil {
		err = ctx.Err()
	}
	return out, err
}

func (c *Connector) openQuery(ctx context.Context, cred connectors.Credential, query string) (*sql.Rows, func(), error) {
	db, err := c.open(cred)
	if err != nil {
		return nil, nil, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("begin read-only query: %s", redact(err.Error(), cred))
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		tx.Rollback()
		db.Close()
		return nil, nil, fmt.Errorf("SQL query failed: %s", redact(err.Error(), cred))
	}
	closeQuery := func() { rows.Close(); tx.Rollback(); db.Close() }
	return rows, closeQuery, nil
}

func (*Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	if err := ctx.Err(); err != nil {
		return connectors.SourceContent{}, err
	}
	var doc Document
	if err := json.Unmarshal(req.Item.Opaque, &doc); err != nil {
		return connectors.SourceContent{}, err
	}
	return connectors.SourceContent{Text: doc.Content, Context: doc.Context}, nil
}

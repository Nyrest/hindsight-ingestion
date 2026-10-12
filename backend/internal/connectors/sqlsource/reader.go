package sqlsource

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

type resultReader struct {
	mode, dialect string
	preview       bool
	emit          func(connectors.SourceItem) error
	out           PreviewResult
	types         []*sql.ColumnType
	cols          rowColumns
	seen          map[string]bool
	count, total  int
	buf           bytes.Buffer
}

func newResultReader(rows *sql.Rows, mode, dialect string, preview bool, emit func(connectors.SourceItem) error) (*resultReader, error) {
	r := &resultReader{mode: mode, dialect: dialect, preview: preview, emit: emit, out: PreviewResult{Documents: []Document{}}}
	var err error
	if r.out.Columns, err = rows.Columns(); err != nil {
		return nil, err
	}
	if r.types, err = rows.ColumnTypes(); err != nil {
		return nil, err
	}
	if mode == RowsMode {
		r.cols, err = reservedColumns(r.out.Columns)
		r.seen = map[string]bool{}
	} else {
		var header []byte
		header, err = queryHeader(r.out.Columns)
		r.buf.Write(header)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(r.out.Columns)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxDocumentBytes/2 {
		return nil, errors.New("column definitions exceed size limit")
	}
	return r, nil
}

func (r *resultReader) rowLimitReached() error {
	if !r.preview {
		return fmt.Errorf("SQL query exceeds %d rows", MaxRows)
	}
	r.out.Truncated = true
	return nil
}

func (r *resultReader) readRows(ctx context.Context, rows *sql.Rows) error {
	values := make([]any, len(r.types))
	targets := make([]any, len(values))
	for i := range values {
		targets[i] = &values[i]
	}
	limit := MaxRows
	if r.preview {
		limit = PreviewRows
	}
	for rows.Next() {
		if r.count >= limit {
			if err := r.rowLimitReached(); err != nil {
				return err
			}
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := rows.Scan(targets...); err != nil {
			return fmt.Errorf("read SQL row: %w", err)
		}
		more, err := r.accept(values)
		if err != nil {
			return fmt.Errorf("row %d: %w", r.count+1, err)
		}
		if !more {
			break
		}
		r.count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("SQL query interrupted: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return rows.Close()
}

func (r *resultReader) accept(values []any) (bool, error) {
	if r.mode == QueryMode {
		return r.appendQueryRow(values)
	}
	doc, err := rowDocument(values, r.types, r.cols, r.dialect)
	if err != nil {
		return false, err
	}
	if r.seen[doc.ID] {
		return false, errors.New("duplicate __id")
	}
	r.seen[doc.ID] = true
	return r.appendDocument(doc)
}

func (r *resultReader) appendDocument(doc Document) (bool, error) {
	size := len(doc.Content) + len(doc.Context)
	if size > MaxDocumentBytes {
		return false, errors.New("SQL document exceeds 4 MiB")
	}
	r.total += size
	if r.total > MaxOutputBytes {
		return false, errors.New("SQL output exceeds 16 MiB")
	}
	if r.preview {
		r.out.Documents = append(r.out.Documents, doc)
		encoded, err := json.Marshal(r.out)
		if err != nil {
			return false, err
		}
		if len(encoded) > PreviewBytes {
			r.out.Documents = r.out.Documents[:len(r.out.Documents)-1]
			r.out.Truncated = true
			return false, nil
		}
		return true, nil
	}
	item, err := sourceItem(doc)
	if err != nil {
		return false, err
	}
	return true, r.emit(item)
}

func (r *resultReader) appendQueryRow(values []any) (bool, error) {
	encoded, err := encodeRow(values, r.types, r.dialect)
	if err != nil {
		return false, err
	}
	limit := MaxDocumentBytes
	if r.preview {
		columns, _ := json.Marshal(r.out.Columns)
		// JSON content is escaped again inside the preview response.
		limit = (PreviewBytes - len(columns) - 1024) / 2
	}
	comma := 0
	if r.count > 0 {
		comma = 1
	}
	if r.buf.Len()+comma+len(encoded)+2 > limit {
		if !r.preview {
			return false, errors.New("SQL query document exceeds 4 MiB")
		}
		r.out.Truncated = true
		return false, nil
	}
	if comma > 0 {
		r.buf.WriteByte(',')
	}
	r.buf.Write(encoded)
	return true, nil
}

func (r *resultReader) finish() (PreviewResult, error) {
	if r.mode == QueryMode {
		if _, err := r.appendDocument(queryDocument(&r.buf)); err != nil {
			return r.out, err
		}
	}
	return r.out, nil
}

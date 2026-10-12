package sqlsource

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

type Document struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Context string `json:"context,omitempty"`
}

type rowColumns struct{ id, content, context int }

func reservedColumns(names []string) (rowColumns, error) {
	r := rowColumns{-1, -1, -1}
	for i, name := range names {
		var target *int
		switch name {
		case "__id":
			target = &r.id
		case "__content":
			target = &r.content
		case "__context":
			target = &r.context
		default:
			continue
		}
		if *target != -1 {
			return r, fmt.Errorf("duplicate reserved column %s", name)
		}
		*target = i
	}
	if r.id < 0 || r.content < 0 {
		return r, errors.New("row mode requires __id and __content columns")
	}
	return r, nil
}

func textValue(v any) (string, error) {
	var text string
	switch v := v.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return "", errors.New("must be text")
	}
	if !utf8.ValidString(text) {
		return "", errors.New("must be valid UTF-8 text")
	}
	return text, nil
}

func normalizeID(v any, typ string) (string, error) {
	if isNonTextType(typ) && !isIntegerType(typ) {
		return "", errors.New("__id must be a string or integer")
	}
	var value string
	integer := true
	switch v := v.(type) {
	case int64:
		value = strconv.FormatInt(v, 10)
	case uint64:
		value = strconv.FormatUint(v, 10)
	case string:
		value = v
		integer = isIntegerType(typ)
	case []byte:
		value = string(v)
		integer = isIntegerType(typ)
	default:
		return "", errors.New("__id must be a nonempty string or integer")
	}
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
		return "", errors.New("__id must be nonempty UTF-8")
	}
	prefix := "string:"
	if integer {
		n, ok := new(big.Int).SetString(value, 10)
		if !ok {
			return "", errors.New("__id must be an integer")
		}
		value = n.String()
		prefix = "integer:"
	}
	return prefix + value, nil
}

func isIntegerType(t string) bool {
	t = strings.ToUpper(t)
	return strings.Contains(t, "INT") || t == "SERIAL" || t == "BIGSERIAL"
}
func isBlobType(t string) bool {
	t = strings.ToUpper(t)
	return strings.Contains(t, "BLOB") || strings.Contains(t, "BINARY") || t == "BYTEA"
}

func isNonTextType(t string) bool {
	t = strings.ToUpper(t)
	if isIntegerType(t) || isBlobType(t) {
		return true
	}
	switch t {
	case "DECIMAL", "NUMERIC", "FLOAT", "FLOAT4", "FLOAT8", "REAL", "DOUBLE", "DOUBLE PRECISION", "BOOL", "BOOLEAN", "BIT", "VARBIT", "DATE", "TIME", "TIMETZ", "TIMESTAMP", "TIMESTAMPTZ", "DATETIME", "INTERVAL", "JSON", "JSONB":
		return true
	}
	return false
}

func columnText(v any, typ, dialect string) (string, error) {
	if dialect != SQLite && isNonTextType(typ) {
		return "", errors.New("must be text")
	}
	if _, blob := v.([]byte); blob && dialect == SQLite {
		return "", errors.New("must be text, not BLOB")
	}
	return textValue(v)
}

func rowDocument(values []any, types []*sql.ColumnType, cols rowColumns, dialect string) (Document, error) {
	if _, blob := values[cols.id].([]byte); blob && dialect == SQLite {
		return Document{}, errors.New("__id must be a string or integer, not BLOB")
	}
	idType := types[cols.id].DatabaseTypeName()
	if dialect == SQLite {
		idType = ""
	} // SQLite storage classes can differ from declared types.
	id, err := normalizeID(values[cols.id], idType)
	if err != nil {
		return Document{}, err
	}
	content, err := columnText(values[cols.content], types[cols.content].DatabaseTypeName(), dialect)
	if err != nil || strings.TrimSpace(content) == "" {
		return Document{}, errors.New("__content must be nonempty text")
	}
	var context string
	if cols.context >= 0 && values[cols.context] != nil {
		context, err = columnText(values[cols.context], types[cols.context].DatabaseTypeName(), dialect)
		if err != nil {
			return Document{}, errors.New("__context must be text or NULL")
		}
	}
	sum := sha256.Sum256([]byte(id))
	return Document{ID: "row:" + hex.EncodeToString(sum[:]), Content: content, Context: context}, nil
}

func normalizeValue(v any, typ string) (any, error) {
	if v == nil {
		return nil, nil
	}
	typ = strings.ToUpper(typ)
	if typ == "JSON" || typ == "JSONB" {
		text, err := textValue(v)
		if err != nil {
			return nil, err
		}
		var result any
		dec := json.NewDecoder(strings.NewReader(text))
		dec.UseNumber()
		if err = dec.Decode(&result); err != nil {
			return nil, errors.New("invalid JSON column value")
		}
		if dec.Decode(new(any)) != io.EOF {
			return nil, errors.New("invalid JSON column value")
		}
		return result, nil
	}
	switch v := v.(type) {
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case time.Time:
		return v.Format(time.RFC3339Nano), nil
	case []byte:
		if isBlobType(typ) {
			return base64.StdEncoding.EncodeToString(v), nil
		}
		return textValue(v)
	case string:
		return textValue(v)
	case float64:
		if strings.Contains(typ, "DECIMAL") || strings.Contains(typ, "NUMERIC") {
			return strconv.FormatFloat(v, 'f', -1, 64), nil
		}
		return v, nil
	case bool:
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported SQL value type %T", v)
	}
}

func sourceItem(doc Document) (connectors.SourceItem, error) {
	opaque, err := json.Marshal(doc)
	if err != nil {
		return connectors.SourceItem{}, err
	}
	revision := sha256.Sum256(opaque)
	return connectors.SourceItem{ID: doc.ID, Name: doc.ID, Kind: connectors.KindDocument, MIMEType: "text/plain", Size: int64(len(doc.Content)), Revision: hex.EncodeToString(revision[:]), Opaque: opaque}, nil
}

func queryHeader(columns []string) ([]byte, error) {
	names, err := json.Marshal(columns)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(`{"columns":`), names...), []byte(`,"rows":[`)...), nil
}

func encodeRow(values []any, types []*sql.ColumnType, dialect string) ([]byte, error) {
	row := make([]any, len(values))
	for i, v := range values {
		switch x := v.(type) {
		case string:
			if len(x) > MaxDocumentBytes {
				return nil, errors.New("SQL value exceeds document size limit")
			}
		case []byte:
			if len(x) > MaxDocumentBytes {
				return nil, errors.New("SQL value exceeds document size limit")
			}
		}
		typ := types[i].DatabaseTypeName()
		if _, blob := v.([]byte); blob && dialect == SQLite {
			typ = "BLOB"
		}
		n, err := normalizeValue(v, typ)
		if err != nil {
			return nil, err
		}
		row[i] = n
	}
	return json.Marshal(row)
}

func queryDocument(buf *bytes.Buffer) Document {
	return Document{ID: "query", Content: buf.String() + "]}"}
}

package connectors

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// AsString converts a loosely typed config value to a string.
func AsString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

// AsBool converts a loosely typed config value to a bool.
func AsBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(t))
		return b
	case float64:
		return t != 0
	}
	return false
}

// AsFloat converts a loosely typed value to a float.
func AsFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

// BoolDefault returns a config bool, or def when unset.
func BoolDefault(cfg map[string]any, key string, def bool) bool {
	v, ok := cfg[key]
	if !ok || v == nil || v == "" {
		return def
	}
	return AsBool(v)
}

// FieldError is a validation error for a single source config field.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Message }

// Require returns a FieldError when any of the keys is empty in cfg.
func Require(cfg map[string]any, keys ...string) error {
	for _, k := range keys {
		if AsString(cfg[k]) == "" {
			return &FieldError{Field: k, Message: "This field is required"}
		}
	}
	return nil
}

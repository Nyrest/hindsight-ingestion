package connectors

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
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

// MatchFilter evaluates structured filter rules against item attributes.
// Rules referencing unknown attributes do not match. Advanced queries are
// evaluated by the provider, never here.
func MatchFilter(f Filter, attrs map[string]any) bool {
	for _, r := range f.Rules {
		if r.Field == "" {
			continue
		}
		if !matchRule(r, attrs[r.Field]) {
			return false
		}
	}
	return true
}

// ValidateFilter checks rules against a connector's filter field specs.
func ValidateFilter(f Filter, specs []FilterFieldSpec, allowAdvanced bool) error {
	switch f.Mode {
	case "", "simple":
	case "advanced":
		if !allowAdvanced {
			return fmt.Errorf("advanced filters are not supported by this source")
		}
	default:
		return fmt.Errorf("unknown filter mode %q", f.Mode)
	}
	for i, r := range f.Rules {
		idx := slices.IndexFunc(specs, func(s FilterFieldSpec) bool { return s.Key == r.Field })
		if idx < 0 {
			return fmt.Errorf("filter rule %d: unknown field %q", i+1, r.Field)
		}
		if !slices.Contains(specs[idx].Operators, r.Operator) {
			return fmt.Errorf("filter rule %d: operator %q is not allowed for %s", i+1, r.Operator, r.Field)
		}
		if (r.Operator == OpIn || r.Operator == OpNotIn) && toList(r.Value) == nil {
			return fmt.Errorf("filter rule %d: %s requires a list of values", i+1, r.Operator)
		}
		if specs[idx].Type == FilterDatetime {
			if _, ok := asTime(r.Value); !ok {
				return fmt.Errorf("filter rule %d: invalid date/time", i+1)
			}
		}
	}
	return nil
}

func matchRule(r FilterRule, actual any) bool {
	switch r.Operator {
	case OpEquals:
		return equalValues(actual, r.Value)
	case OpNotEquals:
		return !equalValues(actual, r.Value)
	case OpContains:
		if list, ok := actual.([]string); ok {
			needle := strings.ToLower(AsString(r.Value))
			return slices.ContainsFunc(list, func(s string) bool { return strings.Contains(strings.ToLower(s), needle) })
		}
		return strings.Contains(strings.ToLower(AsString(actual)), strings.ToLower(AsString(r.Value)))
	case OpIn:
		return slices.ContainsFunc(toList(r.Value), func(v any) bool { return equalValues(actual, v) })
	case OpNotIn:
		return !slices.ContainsFunc(toList(r.Value), func(v any) bool { return equalValues(actual, v) })
	case OpGreaterThan:
		return compare(actual, r.Value) > 0
	case OpLessThan:
		c := compare(actual, r.Value)
		return c < 0 && c != incomparable
	}
	return false
}

const incomparable = -2

func compare(a, b any) int {
	if at, ok := asTime(a); ok {
		if bt, ok := asTime(b); ok {
			return at.Compare(bt)
		}
	}
	if af, ok := AsFloat(a); ok {
		if bf, ok := AsFloat(b); ok {
			switch {
			case af > bf:
				return 1
			case af < bf:
				return -1
			}
			return 0
		}
	}
	return incomparable
}

func equalValues(actual, want any) bool {
	if list, ok := actual.([]string); ok {
		w := strings.ToLower(AsString(want))
		return slices.ContainsFunc(list, func(s string) bool { return strings.ToLower(s) == w })
	}
	switch a := actual.(type) {
	case bool:
		return a == AsBool(want)
	case float64, int, int64:
		af, _ := AsFloat(a)
		wf, ok := AsFloat(want)
		return ok && af == wf
	case time.Time:
		wt, ok := asTime(want)
		return ok && a.Equal(wt)
	}
	return strings.EqualFold(AsString(actual), AsString(want))
}

func toList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		var out []any
		for _, p := range strings.Split(t, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

func asTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, !t.IsZero()
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02"} {
			if parsed, err := time.Parse(layout, strings.TrimSpace(t)); err == nil {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

package connectors

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

// CompileFilter prepares a matcher once per scan, including regular expressions.
func CompileFilter(f Filter) (func(map[string]any) bool, error) {
	return compileGroup(f.Expression, 1)
}

func compileGroup(g FilterGroup, depth int) (func(map[string]any) bool, error) {
	if depth > MaxFilterDepth || (g.Combinator != "" && g.Combinator != "and" && g.Combinator != "or") {
		return nil, fmt.Errorf("filter: invalid group or maximum depth exceeded")
	}
	if depth > 1 && len(g.Rules)+len(g.Groups) == 0 {
		return nil, fmt.Errorf("filter: a nested group must contain a condition")
	}
	conditions := make([]func(map[string]any) bool, 0, len(g.Rules)+len(g.Groups))
	for _, rule := range g.Rules {
		match, err := compileRule(rule)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, match)
	}
	for _, child := range g.Groups {
		match, err := compileGroup(child, depth+1)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, match)
	}
	return combineConditions(conditions, g.Combinator == "or", depth == 1), nil
}

func combineConditions(conditions []func(map[string]any) bool, matchAny, root bool) func(map[string]any) bool {
	return func(attrs map[string]any) bool {
		if len(conditions) == 0 {
			return root
		}
		for _, match := range conditions {
			if match(attrs) == matchAny {
				return matchAny
			}
		}
		return !matchAny
	}
}

func compileRule(rule FilterRule) (func(map[string]any) bool, error) {
	if rule.Operator != OpRegex {
		return func(attrs map[string]any) bool { return matchRule(rule, attrs[rule.Field]) }, nil
	}
	pattern, ok := rule.Value.(string)
	if !ok {
		return nil, fmt.Errorf("filter: regex requires a string pattern")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("filter: invalid regex: %w", err)
	}
	return func(attrs map[string]any) bool {
		actual := attrs[rule.Field]
		return actual != nil && slices.ContainsFunc(textValues(actual), re.MatchString)
	}, nil
}

// ValidateFilter checks rules against a connector's filter field specs.
func ValidateFilter(f Filter, specs []FilterFieldSpec, allowAdvanced bool) error {
	switch f.Mode {
	case "", "simple":
	case "advanced":
		if !allowAdvanced {
			return fmt.Errorf("advanced filters are not supported by this source")
		}
		if strings.TrimSpace(f.AdvancedQuery) == "" {
			return fmt.Errorf("filter: advanced query must not be empty")
		}
	default:
		return fmt.Errorf("unknown filter mode %q", f.Mode)
	}
	return validateGroup(f.Expression, specs, "sourceFilter.expression", 1)
}

func validateGroup(g FilterGroup, specs []FilterFieldSpec, path string, depth int) error {
	if depth > MaxFilterDepth {
		return &FieldError{Field: path, Message: fmt.Sprintf("filter: maximum group depth is %d", MaxFilterDepth)}
	}
	if g.Combinator != "" && g.Combinator != "and" && g.Combinator != "or" {
		return &FieldError{Field: path + ".combinator", Message: "filter: combinator must be and or or"}
	}
	if depth > 1 && len(g.Rules)+len(g.Groups) == 0 {
		return &FieldError{Field: path, Message: "filter: a nested group must contain a condition"}
	}
	for i, rule := range g.Rules {
		if err := validateRule(rule, specs); err != nil {
			return &FieldError{Field: fmt.Sprintf("%s.rules.%d", path, i), Message: "filter: " + err.Error()}
		}
	}
	for i, group := range g.Groups {
		if err := validateGroup(group, specs, fmt.Sprintf("%s.groups.%d", path, i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateRule(r FilterRule, specs []FilterFieldSpec) error {
	idx := slices.IndexFunc(specs, func(s FilterFieldSpec) bool { return s.Key == r.Field })
	if idx < 0 {
		return fmt.Errorf("unknown field %q", r.Field)
	}
	spec := specs[idx]
	if !slices.Contains(spec.Operators, r.Operator) {
		return fmt.Errorf("operator %q is not allowed for %s", r.Operator, r.Field)
	}
	if r.Operator == OpExists || r.Operator == OpNotExists {
		return nil
	}
	if r.Operator == OpRegex {
		pattern, ok := r.Value.(string)
		if !ok {
			return fmt.Errorf("regex requires a string pattern")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid regex: %w", err)
		}
	}
	if r.Operator == OpIn || r.Operator == OpNotIn || r.Operator == OpContainsAny || r.Operator == OpContainsAll {
		values := toList(r.Value)
		if len(values) == 0 {
			return fmt.Errorf("%s requires a non-empty list of values", r.Operator)
		}
		for _, value := range values {
			if err := validateFilterValue(spec, value); err != nil {
				return err
			}
		}
		return nil
	}
	return validateFilterValue(spec, r.Value)
}

func validateFilterValue(spec FilterFieldSpec, value any) error {
	switch spec.Type {
	case FilterNumber:
		if n, ok := AsFloat(value); !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("%s requires a finite number", spec.Key)
		}
	case FilterDatetime:
		if _, ok := asTime(value); !ok {
			return fmt.Errorf("invalid date/time")
		}
	case FilterBoolean:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s requires a boolean", spec.Key)
		}
	case FilterString, FilterEnum:
		s, ok := value.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s requires a non-empty string", spec.Key)
		}
		if spec.Type == FilterEnum && !slices.ContainsFunc(spec.Options, func(o Option) bool { return o.Value == s }) {
			return fmt.Errorf("unknown value %q for %s", s, spec.Key)
		}
	}
	return nil
}

func matchRule(r FilterRule, actual any) bool {
	if r.Operator == OpExists {
		return actual != nil
	}
	if r.Operator == OpNotExists {
		return actual == nil
	}
	if actual == nil {
		return false
	}
	switch r.Operator {
	case OpEquals, OpNotEquals, OpIn, OpNotIn:
		return matchEquality(r, actual)
	case OpContains, OpNotContains, OpStartsWith, OpEndsWith, OpContainsAny, OpContainsAll:
		return matchTextRule(r, actual)
	default:
		return matchComparison(r, actual)
	}
}

func matchEquality(r FilterRule, actual any) bool {
	switch r.Operator {
	case OpEquals:
		return equalValues(actual, r.Value)
	case OpNotEquals:
		return !equalValues(actual, r.Value)
	case OpIn:
		return slices.ContainsFunc(toList(r.Value), func(v any) bool { return equalValues(actual, v) })
	case OpNotIn:
		return !slices.ContainsFunc(toList(r.Value), func(v any) bool { return equalValues(actual, v) })
	}
	return false
}

func matchTextRule(r FilterRule, actual any) bool {
	switch r.Operator {
	case OpContains:
		return matchText(actual, r.Value, strings.Contains)
	case OpNotContains:
		return !matchText(actual, r.Value, strings.Contains)
	case OpStartsWith:
		return matchText(actual, r.Value, strings.HasPrefix)
	case OpEndsWith:
		return matchText(actual, r.Value, strings.HasSuffix)
	case OpContainsAny:
		return slices.ContainsFunc(toList(r.Value), func(v any) bool { return matchText(actual, v, strings.Contains) })
	case OpContainsAll:
		values := toList(r.Value)
		return len(values) > 0 && !slices.ContainsFunc(values, func(v any) bool { return !matchText(actual, v, strings.Contains) })
	}
	return false
}

func matchComparison(r FilterRule, actual any) bool {
	c := compare(actual, r.Value)
	if c == incomparable {
		return false
	}
	switch r.Operator {
	case OpGreaterThan:
		return c > 0
	case OpLessThan:
		return c < 0
	case OpGEQ:
		return c >= 0
	case OpLEQ:
		return c <= 0
	}
	return false
}

func textValues(actual any) []string {
	if list, ok := actual.([]string); ok {
		return list
	}
	return []string{AsString(actual)}
}

func matchText(actual, want any, matches func(string, string) bool) bool {
	needle := strings.ToLower(AsString(want))
	return slices.ContainsFunc(textValues(actual), func(s string) bool { return matches(strings.ToLower(s), needle) })
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
			if math.IsNaN(af) || math.IsNaN(bf) || math.IsInf(af, 0) || math.IsInf(bf, 0) {
				return incomparable
			}
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

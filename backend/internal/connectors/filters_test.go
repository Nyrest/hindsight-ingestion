package connectors

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestExtendedOperators(t *testing.T) {
	attrs := map[string]any{"name": "Quarterly Report.PDF", "size": 2048, "tags": []string{"Finance", "Q1"}, "at": time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "flag": false, "zero": 0, "empty": "", "null": nil, "badNumber": math.NaN()}
	cases := []struct {
		rule FilterRule
		want bool
	}{
		{FilterRule{"size", OpGEQ, 2048}, true},
		{FilterRule{"size", OpLEQ, 2048}, true},
		{FilterRule{"size", OpGEQ, 2049}, false},
		{FilterRule{"size", OpLEQ, 2047}, false},
		{FilterRule{"at", OpGEQ, "2026-03-01T08:00:00+08:00"}, true},
		{FilterRule{"at", OpLEQ, "2026-03-01T00:00:00Z"}, true},
		{FilterRule{"badNumber", OpLEQ, 10}, false},
		{FilterRule{"name", OpNotContains, "secret"}, true},
		{FilterRule{"name", OpNotContains, "report"}, false},
		{FilterRule{"name", OpStartsWith, "QUARTER"}, true},
		{FilterRule{"name", OpEndsWith, ".pdf"}, true},
		{FilterRule{"name", OpRegex, `(?i)^quarterly.*\.pdf$`}, true},
		{FilterRule{"name", OpRegex, `^quarterly`}, false},
		{FilterRule{"name", OpContainsAny, []any{"secret", "report"}}, true},
		{FilterRule{"name", OpContainsAll, []any{"quarter", "report"}}, true},
		{FilterRule{"name", OpContainsAll, []any{"report", "secret"}}, false},
		{FilterRule{"name", OpContainsAll, []any{}}, false},
		{FilterRule{"tags", OpContainsAll, []any{"fin", "q1"}}, true},
		{FilterRule{"tags", OpContainsAny, []string{"q2", "q1"}}, true},
		{FilterRule{"tags", OpNotContains, "q"}, false},
		{FilterRule{"tags", OpStartsWith, "fin"}, true},
		{FilterRule{"tags", OpEndsWith, "ce"}, true},
		{FilterRule{"tags", OpRegex, `^Q[0-9]$`}, true},
		{FilterRule{"flag", OpExists, nil}, true},
		{FilterRule{"zero", OpExists, nil}, true},
		{FilterRule{"empty", OpExists, nil}, true},
		{FilterRule{"null", OpExists, nil}, false},
		{FilterRule{"missing", OpNotExists, nil}, true},
		{FilterRule{"missing", OpNotContains, "secret"}, false},
		{FilterRule{"missing", OpNotEquals, "secret"}, false},
		{FilterRule{"missing", OpNotIn, []any{"x"}}, false},
		{FilterRule{"missing", OpGEQ, 0}, false},
		{FilterRule{"missing", OpLEQ, 0}, false},
	}
	for _, c := range cases {
		if got := matchFilter(t, Filter{Expression: FilterGroup{Rules: []FilterRule{c.rule}}}, attrs); got != c.want {
			t.Errorf("%+v: got %v want %v", c.rule, got, c.want)
		}
	}
}

func TestFilterExpression(t *testing.T) {
	var filter Filter
	if err := json.Unmarshal([]byte(`{"mode":"simple","expression":{"combinator":"and","rules":[{"field":"size","operator":"geq","value":100},{"field":"size","operator":"leq","value":200}],"groups":[{"combinator":"or","rules":[{"field":"name","operator":"endsWith","value":".md"}],"groups":[{"rules":[{"field":"name","operator":"regex","value":"^Report"}]}]}]}}`), &filter); err != nil {
		t.Fatal(err)
	}
	specs := []FilterFieldSpec{{Key: "name", Type: FilterString, Operators: StringOps}, {Key: "size", Type: FilterNumber, Operators: NumberOps}}
	if err := ValidateFilter(filter, specs, false); err != nil {
		t.Fatal(err)
	}
	match, err := CompileFilter(filter)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		size int
		want bool
	}{{"notes.md", 100, true}, {"Report.pdf", 200, true}, {"notes.md", 99, false}, {"Report.pdf", 201, false}, {"Other.pdf", 150, false}} {
		if match(map[string]any{"name": c.name, "size": c.size}) != c.want {
			t.Errorf("case %+v", c)
		}
	}
	filter.Mode, filter.AdvancedQuery = "advanced", "provider query"
	if matchFilter(t, filter, map[string]any{"name": "Other.pdf", "size": 150}) {
		t.Fatal("expression was bypassed in advanced mode")
	}
	if !matchFilter(t, Filter{Expression: FilterGroup{Combinator: "or"}}, nil) {
		t.Fatal("empty root should not restrict scope")
	}

}

func TestFilterValidationBoundaries(t *testing.T) {
	specs := []FilterFieldSpec{{Key: "name", Type: FilterString, Operators: StringOps}, {Key: "size", Type: FilterNumber, Operators: NumberOps}, {Key: "flag", Type: FilterBoolean, Operators: BooleanOps}}
	for _, rule := range []FilterRule{{"name", OpRegex, "["}, {"name", OpRegex, `(?=x)`}, {"name", OpContainsAny, []any{}}, {"name", OpIn, "a,b"}, {"name", OpContainsAll, []any{true}}, {"size", OpGEQ, "NaN"}, {"size", OpLEQ, math.Inf(1)}, {"size", OpIn, []any{2, "oops"}}, {"flag", OpEquals, "true"}, {"size", OpRegex, "x"}} {
		filter := Filter{Expression: FilterGroup{Groups: []FilterGroup{{Rules: []FilterRule{rule}}}}}
		var fe *FieldError
		if err := ValidateFilter(filter, specs, false); !errors.As(err, &fe) || fe.Field != "sourceFilter.expression.groups.0.rules.0" {
			t.Errorf("%+v: error %v", rule, err)
		}
	}
	for _, op := range []string{OpExists, OpNotExists} {
		if err := ValidateFilter(Filter{Expression: FilterGroup{Rules: []FilterRule{{"flag", op, nil}}}}, specs, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range []FilterGroup{{Combinator: "xor"}, {Combinator: "or", Groups: []FilterGroup{{}}}} {
		if ValidateFilter(Filter{Expression: group}, specs, false) == nil {
			t.Fatalf("invalid group accepted: %+v", group)
		}
	}
	group := FilterGroup{Rules: []FilterRule{{"name", OpExists, nil}}}
	for range MaxFilterDepth - 1 {
		group = FilterGroup{Groups: []FilterGroup{group}}
	}
	if err := ValidateFilter(Filter{Expression: group}, specs, false); err != nil {
		t.Fatal(err)
	}
	group = FilterGroup{Groups: []FilterGroup{group}}
	if err := ValidateFilter(Filter{Expression: group}, specs, false); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("depth limit: %v", err)
	}
	if _, err := CompileFilter(Filter{Expression: group}); err == nil {
		t.Fatal("compiler accepted excessive depth")
	}

}

func matchFilter(t *testing.T, filter Filter, attrs map[string]any) bool {
	t.Helper()
	match, err := CompileFilter(filter)
	if err != nil {
		t.Fatal(err)
	}
	return match(attrs)
}

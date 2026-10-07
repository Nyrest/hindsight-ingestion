package connectors

import (
	"testing"
	"time"
)

func TestMatchFilter(t *testing.T) {
	attrs := map[string]any{
		"name":       "Quarterly Report.pdf",
		"size":       float64(2048),
		"modifiedAt": time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		"fileType":   "documents",
		"tags":       []string{"Finance", "q1"},
		"archived":   false,
	}
	cases := []struct {
		rule FilterRule
		want bool
	}{
		{FilterRule{"name", OpContains, "report"}, true},
		{FilterRule{"name", OpEquals, "quarterly report.pdf"}, true},
		{FilterRule{"name", OpNotEquals, "x"}, true},
		{FilterRule{"fileType", OpIn, []any{"documents", "images"}}, true},
		{FilterRule{"fileType", OpNotIn, []any{"documents"}}, false},
		{FilterRule{"size", OpGreaterThan, 1024}, true},
		{FilterRule{"size", OpLessThan, "1024"}, false},
		{FilterRule{"modifiedAt", OpGreaterThan, "2026-01-01"}, true},
		{FilterRule{"modifiedAt", OpLessThan, "2026-01-01T00:00"}, false},
		{FilterRule{"tags", OpContains, "fin"}, true},
		{FilterRule{"tags", OpEquals, "Q1"}, true},
		{FilterRule{"archived", OpEquals, false}, true},
		{FilterRule{"missing", OpGreaterThan, 1}, false},
		{FilterRule{"missing", OpLessThan, 1}, false},
	}
	for _, c := range cases {
		if got := MatchFilter(Filter{Rules: []FilterRule{c.rule}}, attrs); got != c.want {
			t.Errorf("%+v: got %v want %v", c.rule, got, c.want)
		}
	}
}

func TestValidateFilter(t *testing.T) {
	specs := []FilterFieldSpec{{Key: "name", Type: FilterString, Operators: StringOps}, {Key: "at", Type: FilterDatetime, Operators: DatetimeOps}}
	if err := ValidateFilter(Filter{Rules: []FilterRule{{"name", OpContains, "x"}}}, specs, false); err != nil {
		t.Fatal(err)
	}
	bad := []Filter{
		{Rules: []FilterRule{{"nope", OpEquals, "x"}}},
		{Rules: []FilterRule{{"name", OpGreaterThan, "x"}}},
		{Rules: []FilterRule{{"name", OpIn, nil}}},
		{Rules: []FilterRule{{"at", OpGreaterThan, "not a date"}}},
		{Mode: "advanced"},
	}
	for _, f := range bad {
		if ValidateFilter(f, specs, false) == nil {
			t.Errorf("expected error for %+v", f)
		}
	}
}

func TestFileGroup(t *testing.T) {
	cases := map[string]string{
		"a.md": GroupPlainText, "b.PDF": GroupDocuments, "c.jpeg": GroupImages, "d.mp3": GroupAudios,
		"e.docx": GroupDocuments, "noext": "", "f.bin": "",
	}
	for name, want := range cases {
		if got := FileGroup(name, ""); got != want {
			t.Errorf("%s: %q want %q", name, got, want)
		}
	}
	if FileGroup("x", "text/plain") != GroupPlainText {
		t.Error("MIME fallback failed")
	}
}

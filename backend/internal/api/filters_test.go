package api_test

import (
	"encoding/json"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestFilterExpressionAPI(t *testing.T) {
	e := newEnv(t)
	id := makeTask(t, e, e.hsrv.URL, "filesystem")
	filter := connectors.Filter{Mode: "simple", Expression: connectors.FilterGroup{
		Groups: []connectors.FilterGroup{{Combinator: "or", Rules: []connectors.FilterRule{
			{Field: "name", Operator: connectors.OpRegex, Value: `(?i)\.md$`},
			{Field: "size", Operator: connectors.OpLEQ, Value: 1024},
		}}},
	}}
	var task struct {
		SourceFilter connectors.Filter `json:"sourceFilter"`
	}
	if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"sourceFilter": filter}, &task); code != 200 {
		t.Fatal(code)
	}
	if task.SourceFilter.Expression.Rules == nil || len(task.SourceFilter.Expression.Groups) != 1 {
		t.Fatalf("expression did not round-trip: %+v", task.SourceFilter)
	}
	var stored models.Task
	e.db.First(&stored, "id = ?", id)
	if !stored.ReconcileRequired || stored.ConfigRevision != 2 {
		t.Fatal("changing expression did not invalidate scope")
	}
	before := stored.SourceFilterJSON
	filter.Expression.Groups[0].Rules[0].Value = "["
	var response struct {
		Fields map[string]string `json:"fields"`
	}
	if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"sourceFilter": filter}, &response); code != 422 || response.Fields["sourceFilter.expression.groups.0.rules.0"] == "" {
		t.Fatalf("nested validation: %d %+v", code, response)
	}
	e.db.First(&stored, "id = ?", id)
	if stored.SourceFilterJSON != before {
		t.Fatal("invalid filter changed saved task")
	}
	var catalog struct {
		Sources []connectors.SourceInfo `json:"sources"`
	}
	if e.do("GET", "/api/connectors", nil, &catalog) != 200 {
		t.Fatal("catalog request failed")
	}
	for _, source := range catalog.Sources {
		if len(source.FilterCapabilities.Combinators) != 2 || source.FilterCapabilities.MaxDepth != connectors.MaxFilterDepth {
			t.Fatalf("missing structured capabilities for %s", source.Type)
		}
	}
	filter.Expression = connectors.FilterGroup{Rules: []connectors.FilterRule{{Field: "modifiedAt", Operator: connectors.OpExists}}}
	if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"sourceFilter": filter}, &task); code != 200 {
		t.Fatalf("valueless operator: %d", code)
	}
	data, _ := json.Marshal(task.SourceFilter)
	if task.SourceFilter.Expression.Rules[0].Value != nil {
		t.Fatalf("exists acquired a value: %s", data)
	}
}

func TestFilterPlatformRestrictions(t *testing.T) {
	e := newEnv(t)
	id := makeTask(t, e, e.hsrv.URL, "notion")
	for _, filter := range []connectors.Filter{
		{Mode: "simple", Expression: connectors.FilterGroup{Rules: []connectors.FilterRule{{Field: "size", Operator: connectors.OpGEQ, Value: 100}}}},
		{Mode: "advanced", AdvancedQuery: "name contains 'notes'"},
	} {
		if code := e.do("PATCH", "/api/tasks/"+id, map[string]any{"sourceFilter": filter}, nil); code != 422 {
			t.Fatalf("unsupported Notion filter accepted: %d", code)
		}
	}
}

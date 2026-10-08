package observations

import (
	"encoding/json"
	"testing"
)

func TestScopeResolution(t *testing.T) {
	refs := []TagRef{{Kind: "dynamic", Value: "task"}, {Kind: "dynamic", Value: "source"}, {Kind: "dynamic", Value: "source_group"}, {Kind: "literal", Value: " future-tag "}, {Kind: "literal", Value: "future-tag"}}
	s, err := (Scope{Rule: "custom", Scopes: [][]TagRef{refs, refs}}).Normalize()
	if err != nil || len(s.Scopes) != 1 || len(s.Scopes[0]) != 4 {
		t.Fatalf("normalization: %+v %v", s, err)
	}
	got, err := s.Resolve(DocumentTags{TaskID: "a", SourceType: "notion", SourceGroup: []string{"notion_data_source_id:b"}})
	if err != nil || string(got) != `[["future-tag","ingestion_task:a","notion_data_source_id:b","source:notion"]]` {
		t.Fatalf("resolve: %s %v", got, err)
	}
	if _, err := s.Resolve(DocumentTags{TaskID: "a", SourceType: "notion"}); err == nil {
		t.Fatal("missing group must fail")
	}
	for _, rule := range []string{"combined", "shared", "per_tag", "all_combinations"} {
		got, err := (Scope{Rule: rule}).Resolve(DocumentTags{})
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(rule)
		if rule == "combined" {
			if got != nil {
				t.Fatal("combined changes legacy request")
			}
		} else if string(got) != string(want) {
			t.Fatalf("%s: %s", rule, got)
		}
	}
}

func TestInvalidScopes(t *testing.T) {
	for _, s := range []Scope{{Rule: "bad"}, {Rule: "custom"}, {Rule: "custom", Scopes: [][]TagRef{{}}}, {Rule: "custom", Scopes: [][]TagRef{{{Kind: "literal", Value: " "}}}}, {Rule: "custom", Scopes: [][]TagRef{{{Kind: "dynamic", Value: "unknown"}}}}} {
		if _, err := s.Normalize(); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

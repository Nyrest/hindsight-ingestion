package observations

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type TagRef struct {
	Kind  string `json:"kind" enum:"literal,dynamic"`
	Value string `json:"value"`
}

type Scope struct {
	Rule   string     `json:"rule" enum:"combined,shared,per_tag,all_combinations,custom"`
	Scopes [][]TagRef `json:"scopes"`
}

var Default = Scope{Rule: "combined", Scopes: [][]TagRef{}}

func (s Scope) Normalize() (Scope, error) {
	if s.Rule == "" {
		s.Rule = "combined"
	}
	switch s.Rule {
	case "combined", "shared", "per_tag", "all_combinations":
		s.Scopes = [][]TagRef{}
		return s, nil
	case "custom":
	default:
		return s, errors.New("Select a valid observation scope rule")
	}
	if len(s.Scopes) == 0 {
		return s, errors.New("Add at least one observation scope")
	}
	out := make([][]TagRef, 0, len(s.Scopes))
	seen := map[string]bool{}
	for _, group := range s.Scopes {
		refs := []TagRef{}
		for _, ref := range group {
			ref.Value = strings.TrimSpace(ref.Value)
			if ref.Kind == "literal" {
				if ref.Value == "" {
					return s, errors.New("Scope tags must not be empty")
				}
			} else if ref.Kind != "dynamic" || !slices.Contains([]string{"task", "source", "source_group"}, ref.Value) {
				return s, errors.New("Invalid observation scope tag")
			}
			if !slices.Contains(refs, ref) {
				refs = append(refs, ref)
			}
		}
		if len(refs) == 0 {
			return s, errors.New("Each observation scope needs at least one tag")
		}
		slices.SortFunc(refs, func(a, b TagRef) int { return strings.Compare(a.Kind+"\x00"+a.Value, b.Kind+"\x00"+b.Value) })
		encoded, _ := json.Marshal(refs)
		if !seen[string(encoded)] {
			out = append(out, refs)
			seen[string(encoded)] = true
		}
	}
	s.Scopes = out
	return s, nil
}

type DocumentTags struct {
	TaskID      string
	SourceType  string
	SourceGroup []string
}

// Combined is omitted to preserve the existing default request and fingerprints.
func (s Scope) Resolve(tags DocumentTags) (json.RawMessage, error) {
	if s.Rule == "" || s.Rule == "combined" {
		return nil, nil
	}
	if s.Rule != "custom" {
		return json.Marshal(s.Rule)
	}
	groups := make([][]string, 0, len(s.Scopes))
	for _, refs := range s.Scopes {
		group := []string{}
		for _, ref := range refs {
			resolved := []string{ref.Value}
			if ref.Kind == "dynamic" {
				switch ref.Value {
				case "task":
					if tags.TaskID == "" {
						return nil, errors.New("Current task tag is unavailable")
					}
					resolved = []string{"ingestion_task:" + tags.TaskID}
				case "source":
					if tags.SourceType == "" {
						return nil, errors.New("Current source tag is unavailable")
					}
					resolved = []string{"source:" + tags.SourceType}
				case "source_group":
					resolved = tags.SourceGroup
				default:
					return nil, fmt.Errorf("Unknown dynamic scope tag %q", ref.Value)
				}
				if len(resolved) == 0 {
					return nil, errors.New("Current source group tag is unavailable")
				}
			}
			for _, tag := range resolved {
				if !slices.Contains(group, tag) {
					group = append(group, tag)
				}
			}
		}
		slices.Sort(group)
		if !slices.ContainsFunc(groups, func(existing []string) bool { return slices.Equal(existing, group) }) {
			groups = append(groups, group)
		}
	}
	return json.Marshal(groups)
}

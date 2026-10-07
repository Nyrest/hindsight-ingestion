package notion

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PropertiesText converts Notion page properties to readable "Name: value"
// lines. Unknown property types fall back to compact JSON so no information
// is lost.
func PropertiesText(raw json.RawMessage) string {
	var props map[string]json.RawMessage
	if err := json.Unmarshal(raw, &props); err != nil || len(props) == 0 {
		return ""
	}
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		v := propertyValue(props[name])
		if v == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", name, v)
	}
	return strings.TrimSpace(b.String())
}

func propertyValue(raw json.RawMessage) string {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	var typ string
	_ = json.Unmarshal(p["type"], &typ)
	val := p[typ]
	switch typ {
	case "title", "rich_text":
		var rt []richText
		_ = json.Unmarshal(val, &rt)
		return plainText(rt)
	case "number":
		var n *float64
		_ = json.Unmarshal(val, &n)
		if n == nil {
			return ""
		}
		return fmt.Sprint(*n)
	case "checkbox":
		var c bool
		_ = json.Unmarshal(val, &c)
		if c {
			return "Yes"
		}
		return "No"
	case "select", "status":
		var s *struct{ Name string }
		_ = json.Unmarshal(val, &s)
		if s == nil {
			return ""
		}
		return s.Name
	case "multi_select":
		var ms []struct{ Name string }
		_ = json.Unmarshal(val, &ms)
		names := make([]string, 0, len(ms))
		for _, m := range ms {
			names = append(names, m.Name)
		}
		return strings.Join(names, ", ")
	case "date":
		var d *struct {
			Start string  `json:"start"`
			End   *string `json:"end"`
		}
		_ = json.Unmarshal(val, &d)
		if d == nil {
			return ""
		}
		if d.End != nil && *d.End != "" {
			return d.Start + " → " + *d.End
		}
		return d.Start
	case "url", "email", "phone_number", "created_time", "last_edited_time":
		var s *string
		_ = json.Unmarshal(val, &s)
		if s == nil {
			return ""
		}
		return *s
	case "people":
		var ps []struct {
			Name   string `json:"name"`
			Person *struct {
				Email string `json:"email"`
			} `json:"person"`
		}
		_ = json.Unmarshal(val, &ps)
		var out []string
		for _, p := range ps {
			if p.Name != "" {
				out = append(out, p.Name)
			} else if p.Person != nil {
				out = append(out, p.Person.Email)
			}
		}
		return strings.Join(out, ", ")
	case "created_by", "last_edited_by":
		var u struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(val, &u)
		return u.Name
	case "relation":
		var rs []struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(val, &rs)
		ids := make([]string, 0, len(rs))
		for _, r := range rs {
			ids = append(ids, r.ID)
		}
		return strings.Join(ids, ", ")
	case "files":
		var fs []struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(val, &fs)
		names := make([]string, 0, len(fs))
		for _, f := range fs {
			names = append(names, f.Name)
		}
		return strings.Join(names, ", ")
	case "formula":
		var f map[string]json.RawMessage
		_ = json.Unmarshal(val, &f)
		var ft string
		_ = json.Unmarshal(f["type"], &ft)
		return compact(f[ft])
	case "unique_id":
		var u struct {
			Prefix *string `json:"prefix"`
			Number *int    `json:"number"`
		}
		_ = json.Unmarshal(val, &u)
		if u.Number == nil {
			return ""
		}
		if u.Prefix != nil && *u.Prefix != "" {
			return fmt.Sprintf("%s-%d", *u.Prefix, *u.Number)
		}
		return fmt.Sprint(*u.Number)
	}
	return compact(val)
}

func compact(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "[]" || s == "{}" {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return s
}

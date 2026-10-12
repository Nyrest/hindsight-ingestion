package webcal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	ical "github.com/emersion/go-ical"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func parseCalendar(body []byte, maxComponents int) (calendar *ical.Calendar, err error) {
	// The decoder can panic on malformed parameters; never expose an input line.
	defer func() {
		if recover() != nil {
			calendar, err = nil, errors.New("webcal: malformed calendar syntax")
		}
	}()
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(body) {
		return nil, errors.New("webcal: calendar must be UTF-8")
	}
	if err := checkStructure(body, maxComponents); err != nil {
		return nil, err
	}
	decoder := ical.NewDecoder(bytes.NewReader(body))
	calendar, err = decoder.Decode()
	if err != nil {
		return nil, errors.New("webcal: malformed calendar syntax")
	}
	if _, trailing := decoder.Decode(); trailing != io.EOF {
		return nil, errors.New("webcal: unexpected trailing calendar content")
	}
	versions := calendar.Props.Values("VERSION")
	if len(versions) != 1 || versions[0].Value != "2.0" {
		return nil, errors.New("webcal: expected iCalendar VERSION 2.0")
	}
	methods := calendar.Props.Values("METHOD")
	if len(methods) > 1 || (len(methods) == 1 && !strings.EqualFold(methods[0].Value, "PUBLISH")) {
		return nil, errors.New("webcal: scheduling messages are not subscription snapshots")
	}
	return calendar, nil
}

func checkStructure(body []byte, maxComponents int) error {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), len(body)+1)
	state := structureState{maxComponents: maxComponents}
	var logical strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if logical.Len() == 0 {
				return errors.New("webcal: orphan folded line")
			}
			logical.WriteString(line[1:])
			continue
		}
		if logical.Len() > 0 {
			if err := state.line(logical.String()); err != nil {
				return err
			}
		}
		logical.Reset()
		logical.WriteString(line)
	}
	if logical.Len() > 0 {
		if err := state.line(logical.String()); err != nil {
			return err
		}
	}
	if scanner.Err() != nil || state.roots != 1 || len(state.stack) != 0 {
		return errors.New("webcal: truncated calendar structure")
	}
	return nil
}

type structureState struct {
	stack                            []string
	components, roots, maxComponents int
}

func (s *structureState) line(line string) error {
	if err := checkParameterSize(line); err != nil {
		return err
	}
	key, val, hasColon := strings.Cut(line, ":")
	if !hasColon {
		return errors.New("webcal: malformed calendar syntax")
	}
	switch strings.ToUpper(key) {
	case "BEGIN":
		s.components++
		if s.components > s.maxComponents || len(s.stack) >= 32 {
			return errors.New("webcal: component count or nesting limit exceeded")
		}
		kind := strings.ToUpper(val)
		if len(s.stack) == 0 {
			s.roots++
			if s.roots != 1 || kind != "VCALENDAR" {
				return errors.New("webcal: expected one complete calendar")
			}
		} else if !validNesting(s.stack[len(s.stack)-1], kind) {
			return errors.New("webcal: invalid calendar component nesting")
		}
		s.stack = append(s.stack, kind)
	case "END":
		if len(s.stack) == 0 || s.stack[len(s.stack)-1] != strings.ToUpper(val) {
			return errors.New("webcal: unbalanced calendar structure")
		}
		s.stack = s.stack[:len(s.stack)-1]
	default:
		if len(s.stack) == 0 {
			return errors.New("webcal: property outside calendar")
		}
	}
	return nil
}

func validNesting(parent, child string) bool {
	switch child {
	case "VCALENDAR":
		return false
	case "VEVENT", "VTODO", "VJOURNAL", "VFREEBUSY", "VTIMEZONE":
		return parent == "VCALENDAR"
	case "VALARM":
		return parent == "VEVENT" || parent == "VTODO"
	case "STANDARD", "DAYLIGHT":
		return parent == "VTIMEZONE"
	default:
		return true
	}
}

func checkParameterSize(line string) error {
	first := strings.IndexAny(line, ";:")
	if first < 0 || line[first] != ';' {
		return nil
	}
	quoted := false
	for index := first; index < len(line); index++ {
		// go-ical copies parameter values byte by byte; bound its quadratic work.
		if index-first > 8192 {
			return errors.New("webcal: calendar property parameters exceed parsing limit")
		}
		if line[index] == '"' {
			quoted = !quoted
		} else if line[index] == ':' && !quoted {
			break
		}
	}
	return nil
}

type calendarGroup struct {
	kind       string
	uid        string
	master     *ical.Component
	exceptions map[string]*ical.Component
}

func calendarItems(calendar *ical.Calendar, location *time.Location) ([]connectors.SourceItem, []string, error) {
	groups, warnings, skipped, err := groupComponents(calendar)
	if err != nil {
		return nil, warnings, err
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]connectors.SourceItem, 0, len(groups))
	for _, key := range keys {
		calendarItem, itemWarnings, err := buildItem(groups[key], calendar, location)
		warnings = append(warnings, itemWarnings...)
		if err != nil {
			skipped++
			warnings = append(warnings, "Skipped a calendar item with invalid properties")
			continue
		}
		items = append(items, calendarItem)
	}
	if skipped > 0 {
		return items, warnings, fmt.Errorf("webcal: incomplete snapshot (%d invalid components or items); deletion and cursor commit disabled", skipped)
	}
	return items, warnings, nil
}

func groupComponents(calendar *ical.Calendar) (map[string]*calendarGroup, []string, int, error) {
	groups := map[string]*calendarGroup{}
	var warnings []string
	skipped := 0
	for _, component := range calendar.Children {
		if component.Name != "VEVENT" && component.Name != "VTODO" {
			continue
		}
		uid, err := component.Props.Text("UID")
		if err != nil || strings.TrimSpace(uid) == "" || len(component.Props.Values("UID")) != 1 {
			warnings = append(warnings, "Skipped a calendar component with missing or invalid UID")
			skipped++
			continue
		}
		key := component.Name + "\x00" + uid
		group := groups[key]
		if group == nil {
			group = &calendarGroup{kind: component.Name, uid: uid, exceptions: map[string]*ical.Component{}}
			groups[key] = group
		}
		if err := group.add(component); err != nil {
			return nil, warnings, skipped, err
		}
	}
	return groups, warnings, skipped, nil
}

func (g *calendarGroup) add(component *ical.Component) error {
	identity := ""
	if recurrence := component.Props.Get("RECURRENCE-ID"); recurrence != nil {
		canonical := canonicalProp(*recurrence)
		delete(canonical.Params, "RANGE")
		identity = jsonDigest(canonical)
	}
	previous := g.master
	if identity != "" {
		previous = g.exceptions[identity]
	}
	if previous != nil && jsonDigest(canonicalComponent(previous)) != jsonDigest(canonicalComponent(component)) {
		return errors.New("webcal: conflicting components for the same UID and recurrence identity")
	}
	if identity == "" {
		g.master = component
	} else {
		g.exceptions[identity] = component
	}
	return nil
}

type canonicalProperty struct {
	Name   string
	Params map[string][]string
	Value  string
}

func canonicalProp(prop ical.Prop) canonicalProperty {
	params := map[string][]string{}
	for key, vals := range prop.Params {
		params[key] = append([]string(nil), vals...)
		sort.Strings(params[key])
	}
	val := prop.Value
	if prop.Name == "RRULE" {
		parts := strings.Split(strings.ToUpper(val), ";")
		for index, part := range parts {
			key, entries, _ := strings.Cut(part, "=")
			list := strings.Split(entries, ",")
			sort.Strings(list)
			parts[index] = key + "=" + strings.Join(list, ",")
		}
		sort.Strings(parts)
		val = strings.Join(parts, ";")
	}
	if prop.Name == "CATEGORIES" {
		if entries, err := prop.TextList(); err == nil {
			sort.Strings(entries)
			encoded := ical.Prop{Name: prop.Name, Params: ical.Params{}}
			encoded.SetTextList(entries)
			val = encoded.Value
		}
	}
	if prop.Name == "RDATE" || prop.Name == "EXDATE" {
		entries := strings.Split(val, ",")
		sort.Strings(entries)
		val = strings.Join(entries, ",")
	}
	if prop.ValueType() == ical.ValueBinary || (prop.Name == "ATTACH" && strings.HasPrefix(strings.ToLower(val), "data:")) {
		val = "[inline attachment omitted]"
	}
	return canonicalProperty{prop.Name, params, val}
}

func canonicalComponent(component *ical.Component) any {
	props := normalizedProperties(component)
	children := make([]any, 0, len(component.Children))
	for _, child := range component.Children {
		children = append(children, canonicalComponent(child))
	}
	sort.Slice(children, func(i, j int) bool { return jsonDigest(children[i]) < jsonDigest(children[j]) })
	return struct {
		Name     string
		Props    []canonicalProperty
		Children []any
	}{component.Name, props, children}
}

func normalizedProperties(component *ical.Component) []canonicalProperty {
	var props []canonicalProperty
	for key, entries := range component.Props {
		if key == "DTSTAMP" || key == "SEQUENCE" {
			continue
		}
		for _, prop := range entries {
			props = append(props, canonicalProp(prop))
		}
	}
	sort.Slice(props, func(i, j int) bool {
		if props[i].Name != props[j].Name {
			return props[i].Name < props[j].Name
		}
		left, _ := json.Marshal(props[i])
		right, _ := json.Marshal(props[j])
		return string(left) < string(right)
	})
	return props
}

package webcal

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func (g *calendarGroup) components() []*ical.Component {
	components := make([]*ical.Component, 0, len(g.exceptions)+1)
	if g.master != nil {
		components = append(components, g.master)
	}
	exceptions := make([]*ical.Component, 0, len(g.exceptions))
	for _, component := range g.exceptions {
		exceptions = append(exceptions, component)
	}
	sort.Slice(exceptions, func(i, j int) bool {
		left, _ := json.Marshal(canonicalProp(*exceptions[i].Props.Get("RECURRENCE-ID")))
		right, _ := json.Marshal(canonicalProp(*exceptions[j].Props.Get("RECURRENCE-ID")))
		return string(left) < string(right)
	})
	return append(components, exceptions...)
}

type calendarDocument struct {
	markdown    strings.Builder
	bounds      seriesBounds
	masterTimes map[string]calendarTime
	modified    time.Time
	warnings    []string
}

func buildItem(group *calendarGroup, calendar *ical.Calendar, location *time.Location) (connectors.SourceItem, []string, error) {
	components := group.components()
	title, err := components[0].Props.Text("SUMMARY")
	if err != nil {
		return connectors.SourceItem{}, nil, errors.New("invalid summary")
	}
	if title == "" {
		title = group.kind + " " + group.uid
	}
	document, err := renderGroup(group, calendar, location, title)
	if err != nil {
		return connectors.SourceItem{}, document.warnings, err
	}
	stamp := itemTimestamp(group, document.masterTimes)
	sort.Strings(document.warnings)
	document.warnings = slices.Compact(document.warnings)
	content := connectors.SourceContent{Text: document.markdown.String(), Timestamp: stamp, TimestampUnset: stamp.IsZero(), Warnings: document.warnings,
		Context: "Calendar schedule and to-do information. Status and dates are explicit in the content; cancelled entries remain historical records."}
	metadata := map[string]string{"ical_uid": group.uid, "ical_component_type": group.kind, "ical_status": propertyValue(components[0], "STATUS"), "ical_recurring": strconv.FormatBool(group.recurring())}
	opaque, _ := json.Marshal(content)
	attrs := calendarAttributes(group, document.masterTimes, document.bounds)
	id := "ical:" + digest([]byte(group.kind+"\x00"+group.uid))
	return connectors.SourceItem{ID: id, Name: title, Path: group.kind + "/" + id, Kind: connectors.KindDocument,
		Revision: jsonDigest(struct {
			InterpretationVersion int
			Content               connectors.SourceContent
			Metadata              map[string]string
		}{cursorVersion, content, metadata}), ModifiedAt: document.modified, MIMEType: "text/markdown", Size: int64(len(content.Text)), Metadata: metadata, Attributes: attrs, Opaque: opaque}, document.warnings, nil
}

func renderGroup(group *calendarGroup, calendar *ical.Calendar, location *time.Location, title string) (*calendarDocument, error) {
	document := &calendarDocument{masterTimes: map[string]calendarTime{}}
	fmt.Fprintf(&document.markdown, "# %s\n\nComponent: %s\nUID: %s\nRecurring: %t\n", title, group.kind, group.uid, group.recurring())
	if group.master == nil {
		document.markdown.WriteString("\nThe master component is absent. This document preserves the explicit exceptions only.\n")
	}
	components := group.components()
	for _, component := range components {
		times, err := document.appendComponent(component, location)
		if err != nil {
			return document, err
		}
		if component == group.master {
			document.masterTimes = times
		}
	}
	if timezones := referencedTimezones(components, calendar); timezones != "" {
		document.markdown.WriteString("\n## Referenced timezone definitions\n\n" + timezones)
	}
	return document, nil
}

func (d *calendarDocument) appendComponent(component *ical.Component, location *time.Location) (map[string]calendarTime, error) {
	section, times, warnings, err := renderComponent(component, location)
	d.warnings = append(d.warnings, warnings...)
	if err != nil {
		return nil, err
	}
	end, err := componentEnd(component, times)
	if err != nil {
		return nil, err
	}
	d.bounds.include(times["DTSTART"], end)
	if err := d.bounds.includeRule(component, times, location); err != nil {
		return nil, err
	}
	if err := d.bounds.includeDates(component, times, location); err != nil {
		return nil, err
	}
	if recurrence := component.Props.Get("RECURRENCE-ID"); recurrence != nil && strings.EqualFold(recurrence.Params.Get("RANGE"), "THISANDFUTURE") {
		d.bounds.startUnknown, d.bounds.endUnknown = true, true
	}
	if lastModified := times["LAST-MODIFIED"].instant; lastModified.After(d.modified) {
		d.modified = lastModified
	}
	d.markdown.WriteString("\n" + section)
	return times, nil
}

func propertyValue(component *ical.Component, name string) string {
	if prop := component.Props.Get(name); prop != nil {
		return prop.Value
	}
	return ""
}

func calendarAttributes(group *calendarGroup, times map[string]calendarTime, bounds seriesBounds) map[string]any {
	attrs := map[string]any{"componentType": group.kind, "recurring": group.recurring()}
	components := group.components()
	for _, name := range []string{"STATUS", "LOCATION", "CATEGORIES", "ATTENDEE"} {
		var entries []string
		for _, component := range components {
			for _, prop := range component.Props.Values(name) {
				entries = append(entries, renderRawProp(canonicalProp(prop)))
			}
		}
		sort.Strings(entries)
		key := map[string]string{"STATUS": "status", "LOCATION": "location", "CATEGORIES": "categories", "ATTENDEE": "attendees"}[name]
		if name == "STATUS" || name == "LOCATION" {
			attrs[key] = propertyValue(components[0], name)
			if name == "LOCATION" {
				location, _ := components[0].Props.Text(name)
				attrs[key] = location
			}
		} else {
			attrs[key] = strings.Join(entries, "\n")
		}
	}
	for property, field := range map[string]string{"DTSTART": "startAt", "DTEND": "endAt", "DUE": "dueAt", "COMPLETED": "completedAt"} {
		if parsed := times[property]; parsed.known {
			attrs[field] = parsed.instant
		}
	}
	if !bounds.startUnknown && !bounds.start.IsZero() {
		attrs["seriesStartAt"] = bounds.start
	}
	if !bounds.endUnknown && !bounds.end.IsZero() {
		attrs["seriesEndAt"] = bounds.end
	}
	return attrs
}

func renderComponent(component *ical.Component, location *time.Location) (string, map[string]calendarTime, []string, error) {
	if err := validateProperties(component); err != nil {
		return "", nil, nil, err
	}
	times, err := componentTimes(component, location)
	if err != nil {
		return "", nil, nil, err
	}
	var markdown strings.Builder
	fmt.Fprintf(&markdown, "## %s\n\n", component.Name)
	var warnings []string
	for _, prop := range normalizedProperties(component) {
		line := renderRawProp(prop)
		if parsed, exists := times[prop.Name]; exists {
			if !parsed.known {
				line += " (timezone unresolved; original local time preserved)"
				warnings = append(warnings, "Calendar timezone is unresolved; original times and unknown boundaries are preserved")
			} else {
				line += " (" + parsed.instant.UTC().Format(time.RFC3339) + "; timezone " + parsed.zone + ")"
			}
			if parsed.allDay {
				line += " [all-day date]"
				if prop.Name == "DTEND" {
					line += " [exclusive end date]"
				}
			}
		}
		markdown.WriteString("- " + line + "\n")
	}
	children := append([]*ical.Component(nil), component.Children...)
	sort.Slice(children, func(i, j int) bool {
		return jsonDigest(canonicalComponent(children[i])) < jsonDigest(canonicalComponent(children[j]))
	})
	for _, child := range children {
		section, _, childWarnings, err := renderComponent(child, location)
		if err != nil {
			return "", nil, warnings, err
		}
		markdown.WriteString("\n" + section)
		warnings = append(warnings, childWarnings...)
	}
	return markdown.String(), times, warnings, nil
}

func validateProperties(component *ical.Component) error {
	for _, key := range []string{"SUMMARY", "DESCRIPTION", "STATUS", "LOCATION", "DURATION", "RRULE", "RECURRENCE-ID", "PRIORITY", "PERCENT-COMPLETE"} {
		if len(component.Props.Values(key)) > 1 {
			return errors.New("duplicate singleton property")
		}
	}
	for _, key := range []string{"SUMMARY", "DESCRIPTION", "LOCATION", "CATEGORIES"} {
		for _, prop := range component.Props.Values(key) {
			if _, err := prop.TextList(); err != nil {
				return errors.New("invalid calendar text")
			}
		}
	}
	for _, key := range []string{"PERCENT-COMPLETE", "PRIORITY"} {
		if prop := component.Props.Get(key); prop != nil {
			number, err := prop.Int()
			upper := 100
			if key == "PRIORITY" {
				upper = 9
			}
			if err != nil || number < 0 || number > upper {
				return errors.New("invalid calendar numeric property")
			}
		}
	}
	return nil
}

func renderRawProp(prop canonicalProperty) string {
	var line strings.Builder
	line.WriteString(prop.Name)
	keys := make([]string, 0, len(prop.Params))
	for key := range prop.Params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		line.WriteString(";" + key + "=" + strings.Join(prop.Params[key], ","))
	}
	value := prop.Value
	parsed := ical.Prop{Name: prop.Name, Value: prop.Value, Params: ical.Params(prop.Params)}
	if parsed.ValueType() == ical.ValueText {
		if texts, err := parsed.TextList(); err == nil {
			value = strings.Join(texts, ", ")
		}
	}
	line.WriteString(": " + value)
	return line.String()
}

func referencedTimezones(components []*ical.Component, calendar *ical.Calendar) string {
	referenced := map[string]bool{}
	var collect func(*ical.Component)
	collect = func(component *ical.Component) {
		for _, props := range component.Props {
			for _, prop := range props {
				if tzid := prop.Params.Get("TZID"); tzid != "" {
					referenced[tzid] = true
				}
			}
		}
		for _, child := range component.Children {
			collect(child)
		}
	}
	for _, component := range components {
		collect(component)
	}
	var definitions []string
	for _, child := range calendar.Children {
		if child.Name == "VTIMEZONE" && referenced[propertyValue(child, "TZID")] {
			encoded, _ := json.MarshalIndent(canonicalComponent(child), "", "  ")
			definitions = append(definitions, "```json\n"+string(encoded)+"\n```\n")
		}
	}
	sort.Strings(definitions)
	return strings.Join(definitions, "\n")
}

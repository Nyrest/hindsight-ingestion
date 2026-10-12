package webcal

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
)

type calendarTime struct {
	instant time.Time
	known   bool
	allDay  bool
	zone    string
}

func parseTime(prop *ical.Prop, location *time.Location) (calendarTime, error) {
	parsed := calendarTime{allDay: prop.ValueType() == ical.ValueDate, zone: prop.Params.Get("TZID")}
	if (!parsed.allDay && prop.ValueType() != ical.ValueDateTime) || len(prop.Params["TZID"]) > 1 || len(prop.Params["VALUE"]) > 1 {
		return parsed, errors.New("invalid calendar date type or parameters")
	}
	layout := "20060102T150405"
	if parsed.allDay {
		layout = "20060102"
	} else if strings.HasSuffix(prop.Value, "Z") {
		layout = "20060102T150405Z"
		location = time.UTC
	}
	wall, err := time.Parse(layout, prop.Value)
	if err != nil || wall.Year() < 1 {
		return parsed, errors.New("invalid calendar date")
	}
	if parsed.zone != "" && !strings.HasSuffix(prop.Value, "Z") {
		zone, err := time.LoadLocation(parsed.zone)
		if err != nil {
			return parsed, nil
		}
		location = zone
	}
	parsed.instant = resolveWallTime(wall, location)
	if parsed.instant.UTC().Year() < 1 || parsed.instant.UTC().Year() > 9999 {
		parsed.instant = time.Time{}
		return parsed, nil
	}
	parsed.known = true
	if parsed.zone == "" {
		parsed.zone = location.String()
	}
	return parsed, nil
}

func resolveWallTime(wall time.Time, location *time.Location) time.Time {
	offsets := map[int]bool{}
	for _, hours := range []int{-48, -24, 0, 24, 48} {
		_, offset := wall.Add(time.Duration(hours) * time.Hour).In(location).Zone()
		offsets[offset] = true
	}
	var first time.Time
	minimumOffset := 86400
	for offset := range offsets {
		if offset < minimumOffset {
			minimumOffset = offset
		}
		candidate := wall.Add(-time.Duration(offset) * time.Second).In(location)
		if candidate.Format("20060102T150405") == wall.Format("20060102T150405") && (first.IsZero() || candidate.Before(first)) {
			first = candidate
		}
	}
	if !first.IsZero() {
		return first
	}
	// RFC 5545 selects the first overlap and the pre-transition offset for a gap.
	return wall.Add(-time.Duration(minimumOffset) * time.Second).In(location)
}

var timeProperties = map[string]bool{
	"DTSTART": true, "DTEND": true, "DUE": true, "COMPLETED": true, "CREATED": true, "LAST-MODIFIED": true, "RECURRENCE-ID": true,
}

func componentTimes(component *ical.Component, location *time.Location) (map[string]calendarTime, error) {
	times := map[string]calendarTime{}
	for name := range timeProperties {
		entries := component.Props.Values(name)
		if len(entries) > 1 {
			return nil, errors.New("duplicate singleton date property")
		}
		if len(entries) == 1 {
			parsed, err := parseTime(&entries[0], location)
			if err != nil {
				return nil, err
			}
			times[name] = parsed
		}
	}
	return times, nil
}

func itemTimestamp(g *calendarGroup, masterTimes map[string]calendarTime) time.Time {
	if g.recurring() || g.master == nil {
		return time.Time{}
	}
	priority := []string{"DTSTART"}
	if g.kind == "VTODO" {
		priority = []string{"COMPLETED", "DUE", "DTSTART"}
	}
	for _, name := range priority {
		if candidate, exists := masterTimes[name]; exists {
			return candidate.instant
		}
	}
	return time.Time{}
}

func (g *calendarGroup) recurring() bool {
	return len(g.exceptions) > 0 || (g.master != nil && (g.master.Props.Get("RRULE") != nil || g.master.Props.Get("RDATE") != nil || g.master.Props.Get("EXDATE") != nil))
}

type seriesBounds struct {
	start, end               time.Time
	startUnknown, endUnknown bool
}

func (b *seriesBounds) include(start, end calendarTime) {
	if !start.known {
		b.startUnknown = true
	} else if b.start.IsZero() || start.instant.Before(b.start) {
		b.start = start.instant
	}
	if !end.known {
		b.endUnknown = true
	} else if b.end.IsZero() || end.instant.After(b.end) {
		b.end = end.instant
	}
}

func componentEnd(component *ical.Component, times map[string]calendarTime) (calendarTime, error) {
	for _, key := range []string{"DTEND", "DUE"} {
		if end, exists := times[key]; exists {
			start := times["DTSTART"]
			if component.Props.Get("DURATION") != nil || (start.known && end.known && (end.instant.Before(start.instant) || start.allDay != end.allDay)) {
				return calendarTime{}, errors.New("invalid calendar end or duration")
			}
			return end, nil
		}
	}
	start := times["DTSTART"]
	if duration := component.Props.Get("DURATION"); duration != nil {
		return addDuration(start, duration)
	}
	if start.allDay && component.Name == "VEVENT" && start.known {
		start.instant = addCalendarDays(start.instant, 1)
		if start.instant.UTC().Year() > 9999 {
			return calendarTime{}, nil
		}
	}
	return start, nil
}

var durationPattern = regexp.MustCompile(`^\+?P(?:(\d+)W|(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?)$`)

func durationParts(prop *ical.Prop) (int, time.Duration, error) {
	parts := durationPattern.FindStringSubmatch(strings.ToUpper(prop.Value))
	if _, err := prop.Duration(); err != nil || parts == nil {
		return 0, 0, errors.New("invalid calendar duration")
	}
	units := []int64{7 * 86400, 86400, 3600, 60, 1}
	var seconds, daySeconds int64
	populated := false
	for index, count := range parts[1:] {
		if count == "" {
			continue
		}
		populated = true
		number, err := strconv.ParseInt(count, 10, 64)
		if err != nil || number > (int64((1<<63-1)/time.Second)-seconds)/units[index] {
			return 0, 0, errors.New("calendar duration is out of range")
		}
		seconds += number * units[index]
		if index < 2 {
			daySeconds += number * units[index]
		}
	}
	if !populated || (strings.Contains(strings.ToUpper(prop.Value), "T") && parts[3] == "" && parts[4] == "" && parts[5] == "") {
		return 0, 0, errors.New("empty calendar duration")
	}
	return int(daySeconds / 86400), time.Duration(seconds-daySeconds) * time.Second, nil
}

func addDuration(start calendarTime, prop *ical.Prop) (calendarTime, error) {
	days, clock, err := durationParts(prop)
	if err != nil {
		return calendarTime{}, errors.New("invalid calendar duration")
	}
	if start.known {
		// Add calendar days before clock units, including mixed durations across DST.
		start.instant = addCalendarDays(start.instant, days).Add(clock)
		if start.instant.UTC().Year() > 9999 {
			start.instant, start.known = time.Time{}, false
		}
	}
	return start, nil
}

func addCalendarDays(instant time.Time, days int) time.Time {
	wall := time.Date(instant.Year(), instant.Month(), instant.Day()+days, instant.Hour(), instant.Minute(), instant.Second(), 0, time.UTC)
	return resolveWallTime(wall, instant.Location())
}

func (b *seriesBounds) includeRule(component *ical.Component, times map[string]calendarTime, location *time.Location) error {
	rule := component.Props.Get("RRULE")
	if rule == nil {
		return nil
	}
	parts, err := recurrenceParts(rule)
	if err != nil {
		return err
	}
	until := parts["UNTIL"]
	if until == "" {
		b.endUnknown = true
		return nil
	}
	startProp := component.Props.Get("DTSTART")
	if startProp == nil {
		b.startUnknown, b.endUnknown = true, true
		return nil
	}
	last := *startProp
	last.Value = until
	lastStart, err := parseTime(&last, location)
	if err != nil {
		return err
	}
	lastEnd, err := occurrenceEnd(lastStart, component, times)
	b.include(lastStart, lastEnd)
	return err
}

func occurrenceEnd(start calendarTime, component *ical.Component, times map[string]calendarTime) (calendarTime, error) {
	if first := times["DTSTART"]; first.known && start.known {
		start.instant = start.instant.In(first.instant.Location())
	}
	if duration := component.Props.Get("DURATION"); duration != nil {
		return addDuration(start, duration)
	}
	firstEnd, err := componentEnd(component, times)
	firstStart := times["DTSTART"]
	if err != nil || !firstStart.known || !firstEnd.known || !start.known {
		return calendarTime{}, err
	}
	if firstStart.allDay && firstEnd.allDay {
		dateOnly := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
		days := int((dateOnly(firstEnd.instant).Unix() - dateOnly(firstStart.instant).Unix()) / 86400)
		start.instant = addCalendarDays(start.instant, days)
	} else {
		seconds := firstEnd.instant.Unix() - firstStart.instant.Unix()
		if seconds > int64((1<<63-1)/time.Second) {
			return calendarTime{}, nil
		}
		start.instant = start.instant.Add(time.Duration(seconds) * time.Second)
	}
	if start.instant.UTC().Year() > 9999 {
		return calendarTime{}, nil
	}
	return start, nil
}

func (b *seriesBounds) includeDates(component *ical.Component, times map[string]calendarTime, location *time.Location) error {
	for _, key := range []string{"RDATE", "EXDATE"} {
		for _, prop := range component.Props.Values(key) {
			for _, raw := range strings.Split(prop.Value, ",") {
				start, end, err := datePeriod(prop, raw, location)
				if err != nil {
					return err
				}
				if key == "RDATE" {
					if !strings.Contains(raw, "/") {
						end, err = occurrenceEnd(start, component, times)
						if err != nil {
							return err
						}
					}
					b.include(start, end)
				}
			}
		}
	}
	return nil
}

func datePeriod(prop ical.Prop, raw string, location *time.Location) (calendarTime, calendarTime, error) {
	parts := strings.Split(raw, "/")
	if len(parts) > 2 || (len(parts) == 2 && prop.ValueType() != ical.ValuePeriod) || (len(parts) == 1 && prop.ValueType() == ical.ValuePeriod) {
		return calendarTime{}, calendarTime{}, errors.New("invalid date period")
	}
	prop.Value = parts[0]
	if prop.ValueType() == ical.ValuePeriod {
		params := ical.Params{}
		for key, vals := range prop.Params {
			params[key] = vals
		}
		params["VALUE"] = []string{"DATE-TIME"}
		prop.Params = params
	}
	start, err := parseTime(&prop, location)
	if err != nil || len(parts) == 1 {
		return start, start, err
	}
	prop.Value = parts[1]
	if strings.HasPrefix(parts[1], "P") {
		prop.Params = ical.Params{"VALUE": {"DURATION"}}
		end, err := addDuration(start, &prop)
		return start, end, err
	}
	end, err := parseTime(&prop, location)
	if err == nil && start.known && end.known && !end.instant.After(start.instant) {
		err = errors.New("invalid date period end")
	}
	return start, end, err
}

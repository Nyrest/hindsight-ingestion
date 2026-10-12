package webcal

import (
	"errors"
	"strconv"
	"strings"

	ical "github.com/emersion/go-ical"
)

func recurrenceParts(prop *ical.Prop) (map[string]string, error) {
	normalized := *prop
	normalized.Value = strings.ToUpper(prop.Value)
	if _, err := (ical.Props{"RRULE": {normalized}}).RecurrenceRule(); err != nil {
		return nil, errors.New("invalid recurrence rule")
	}
	parts := map[string]string{}
	for _, part := range strings.Split(normalized.Value, ";") {
		key, value, _ := strings.Cut(part, "=")
		if _, duplicate := parts[key]; duplicate {
			return nil, errors.New("duplicate recurrence rule part")
		}
		parts[key] = value
		if err := validateRecurrencePart(key, value); err != nil {
			return nil, err
		}
	}
	if parts["COUNT"] != "" && parts["UNTIL"] != "" {
		return nil, errors.New("recurrence COUNT and UNTIL are mutually exclusive")
	}
	return parts, nil
}

func validateRecurrencePart(key, value string) error {
	if key == "WKST" && len(value) != 2 {
		return errors.New("invalid recurrence week start")
	}
	if key == "BYDAY" {
		for _, weekday := range strings.Split(value, ",") {
			if len(weekday) > 2 {
				ordinal, err := strconv.Atoi(weekday[:len(weekday)-2])
				if err != nil || ordinal == 0 || ordinal < -53 || ordinal > 53 {
					return errors.New("invalid recurrence weekday ordinal")
				}
			}
		}
		return nil
	}
	bounds, bounded := recurrenceBounds[key]
	if !bounded {
		return nil
	}
	for _, entry := range strings.Split(value, ",") {
		number, err := strconv.ParseInt(entry, 10, 64)
		within := number >= bounds.min && number <= bounds.max
		if bounds.signed {
			within = within || (number <= -bounds.min && number >= -bounds.max)
		}
		if err != nil || !within {
			return errors.New("recurrence value is out of range")
		}
	}
	return nil
}

var recurrenceBounds = map[string]struct {
	min, max int64
	signed   bool
}{
	"COUNT": {1, 2147483647, false}, "INTERVAL": {1, 2147483647, false},
	"BYMONTH": {1, 12, false}, "BYMONTHDAY": {1, 31, true}, "BYYEARDAY": {1, 366, true},
	"BYWEEKNO": {1, 53, true}, "BYSETPOS": {1, 366, true},
	"BYHOUR": {0, 23, false}, "BYMINUTE": {0, 59, false}, "BYSECOND": {0, 60, false},
}

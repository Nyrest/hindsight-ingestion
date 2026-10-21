package webcal

import (
	"time"

	ical "github.com/emersion/go-ical"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

// Snapshot is a parsed calendar resource, including its nested component count.
type Snapshot struct {
	Items      []connectors.SourceItem
	Warnings   []string
	Components int
}

// ParseSnapshot shares calendar interpretation with CalDAV. An error means the
// resource cannot establish a complete inventory, even when some items are valid.
func ParseSnapshot(body []byte, maxComponents int, location *time.Location) (Snapshot, error) {
	calendar, err := parseCalendar(body, maxComponents)
	if err != nil {
		return Snapshot{}, err
	}
	var count func(*ical.Component) int
	count = func(component *ical.Component) int {
		n := 1
		for _, child := range component.Children {
			n += count(child)
		}
		return n
	}
	items, warnings, err := calendarItems(calendar, location)
	return Snapshot{Items: items, Warnings: warnings, Components: count(calendar.Component)}, err
}

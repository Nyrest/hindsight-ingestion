// Package webcal ingests iCalendar subscription snapshots.
package webcal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

// Bump when parsing, rendering or timestamp interpretation changes so cached bodies are reprocessed.
const cursorVersion = 1

type Source struct{}

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: "webcal", Name: "WebCAL", Description: "iCalendar subscription for events and to-dos",
		Fields: []connectors.FieldSpec{{Key: "feedUrl", Label: "Calendar subscription URL", Type: connectors.FieldURL, Required: true, Secret: true,
			Help: "The subscription URL is encrypted. HTTP, HTTPS and webcal URLs are supported."}},
	})
	connectors.RegisterSource(&Source{})
}

func (*Source) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type: "webcal", Name: "WebCAL", CredentialTypes: []string{"webcal"}, BrowseKinds: []string{},
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalConditionalInventory, DeletionMode: connectors.DeletionScanGeneration},
		Fields: []connectors.FieldSpec{
			{Key: "maxFeedSizeMB", Label: "Maximum calendar size (MiB)", Type: connectors.FieldNumber, Default: 20, Effect: connectors.EffectReconcile,
				Help: "Complete snapshots delete missing documents, including an empty calendar. A rolling subscription window also removes older documents."},
			{Key: "maxComponents", Label: "Maximum calendar components", Type: connectors.FieldNumber, Default: 20000, Effect: connectors.EffectReconcile,
				Help: "For a series time window, allow missing boundaries: earliest time is missing or before the window ends, and end boundary is missing or after the window starts."},
		},
		FilterFields: filterFields(),
	}
}

func filterFields() []connectors.FilterFieldSpec {
	fields := []connectors.FilterFieldSpec{
		{Key: "componentType", Label: "Calendar component", Type: connectors.FilterEnum, Operators: connectors.EnumOps,
			Options: []connectors.Option{{Value: "VEVENT", Label: "Event"}, {Value: "VTODO", Label: "To-do"}}},
		{Key: "recurring", Label: "Recurring", Type: connectors.FilterBoolean, Operators: connectors.BooleanOps},
	}
	for _, field := range []struct{ key, label string }{{"name", "Title"}, {"status", "Calendar status"}, {"location", "Location"}, {"categories", "Categories"}, {"attendees", "Attendees"}} {
		fields = append(fields, connectors.FilterFieldSpec{Key: field.key, Label: field.label, Type: connectors.FilterString, Operators: connectors.StringOps})
	}
	for _, field := range []struct{ key, label string }{
		{"startAt", "Original start"}, {"endAt", "Original end"}, {"dueAt", "Original due"}, {"completedAt", "Completed at"},
		{"seriesStartAt", "Series earliest time"}, {"seriesEndAt", "Series end boundary"},
	} {
		fields = append(fields, connectors.FilterFieldSpec{Key: field.key, Label: field.label, Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps})
	}
	return fields
}

type limits struct {
	bytes      int64
	components int
}

func calendarLimits(cfg map[string]any) (limits, error) {
	defaults := map[string]int64{"maxFeedSizeMB": 20, "maxComponents": 20000}
	for key := range defaults {
		configured, exists := cfg[key]
		if !exists {
			continue
		}
		number, valid := connectors.AsFloat(configured)
		// Bound conversions before multiplying the byte limit.
		if !valid || math.IsNaN(number) || math.IsInf(number, 0) || number < 1 || number != math.Trunc(number) || number > float64(math.MaxInt32) {
			return limits{}, &connectors.FieldError{Field: key, Message: "Must be a positive integer no greater than 2147483647"}
		}
		defaults[key] = int64(number)
	}
	return limits{bytes: defaults["maxFeedSizeMB"] << 20, components: int(defaults["maxComponents"])}, nil
}

func (s *Source) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	if _, err := calendarLimits(cfg); err != nil {
		return err
	}
	return connectors.ValidateFilter(filter, s.Info().FilterFields, false)
}

func (*Source) Browse(context.Context, connectors.Credential, connectors.BrowseRequest) (connectors.BrowseResult, error) {
	return connectors.BrowseResult{Items: []connectors.BrowseItem{}, Breadcrumbs: []connectors.Breadcrumb{}}, nil
}

func (*Source) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	response, err := fetch(ctx, fetchRequest{credential: cred, limits: limits{bytes: 20 << 20, components: 20000}})
	if err != nil {
		return "", err
	}
	calendar, err := parseCalendar(response.body, 20000)
	if err != nil {
		return "", err
	}
	_, warnings, incomplete := calendarItems(calendar, time.UTC)
	if incomplete != nil {
		return "", incomplete
	}
	if len(warnings) > 0 {
		return "Calendar is readable; some time boundaries are unknown", nil
	}
	return "Calendar subscription is readable", nil
}

type cursor struct {
	Version      int    `json:"version"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	ContentHash  string `json:"contentHash"`
	ContextHash  string `json:"contextHash"`
}

func digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func jsonDigest(payload any) string {
	encoded, _ := json.Marshal(payload)
	return digest(encoded)
}

// CredentialTarget compares the effective representation without exposing a URL or headers.
func CredentialTarget(cred connectors.Credential) string {
	address := cred.String("feedUrl")
	if normalized, err := subscriptionURL(address); err == nil {
		address = normalized.String()
	}
	headers := map[string]string{}
	for name, value := range cred.Headers {
		headers[strings.ToLower(name)] = value
	}
	return jsonDigest(struct {
		URL     string
		Headers map[string]string
	}{address, headers})
}

type scanSettings struct {
	budget      limits
	location    *time.Location
	contextHash string
}

func scanConfiguration(req connectors.ScanRequest) (scanSettings, error) {
	budget, err := calendarLimits(req.Config)
	if err != nil {
		return scanSettings{}, err
	}
	zone := req.Timezone
	if zone == "" {
		zone = "UTC"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return scanSettings{}, errors.New("webcal: invalid configured timezone")
	}
	contextHash := jsonDigest(struct {
		Target, Timezone string
		Proxy            any
		Limits           map[string]any
	}{CredentialTarget(req.Credential), zone, req.Credential.Proxy, req.Config})
	return scanSettings{budget, location, contextHash}, nil
}

func (*Source) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	settings, err := scanConfiguration(req)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	var previous cursor
	usable := json.Unmarshal(req.Cursor, &previous) == nil && previous.Version == cursorVersion && previous.ContextHash == settings.contextHash && previous.ContentHash != ""
	fetching := fetchRequest{credential: req.Credential, limits: settings.budget}
	if usable && !req.Full {
		fetching.previous = &previous
	}
	response, err := fetch(ctx, fetching)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	if response.notModified {
		return connectors.ScanResult{Cursor: req.Cursor}, nil
	}
	if usable && !req.Full && digest(response.body) == previous.ContentHash {
		previous.ETag, previous.LastModified = response.etag, response.lastModified
		next, _ := json.Marshal(previous)
		return connectors.ScanResult{Cursor: next}, nil
	}
	return scanSnapshot(ctx, req, response, settings)
}

func scanSnapshot(ctx context.Context, req connectors.ScanRequest, response feedResponse, settings scanSettings) (connectors.ScanResult, error) {
	calendar, err := parseCalendar(response.body, settings.budget.components)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	logRefreshHint(calendar, req.Log)
	items, warnings, incomplete := calendarItems(calendar, settings.location)
	for _, warning := range warnings {
		if req.Log != nil {
			req.Log("warn", warning)
		}
	}
	for _, calendarItem := range items {
		if err := ctx.Err(); err != nil {
			return connectors.ScanResult{}, err
		}
		if err := req.Emit(calendarItem); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	if incomplete != nil {
		return connectors.ScanResult{Cursor: req.Cursor, Error: incomplete}, nil
	}
	next, _ := json.Marshal(cursor{Version: cursorVersion, ETag: response.etag, LastModified: response.lastModified, ContentHash: digest(response.body), ContextHash: settings.contextHash})
	return connectors.ScanResult{Cursor: next, Complete: true}, nil
}

func logRefreshHint(calendar *ical.Calendar, log func(string, string)) {
	if log == nil {
		return
	}
	for _, key := range []string{"REFRESH-INTERVAL", "X-PUBLISHED-TTL"} {
		if hint := calendar.Props.Get(key); hint != nil {
			duration := &ical.Prop{Name: "DURATION", Value: hint.Value}
			if days, clock, err := durationParts(duration); err == nil && (days > 0 || clock > 0) {
				log("info", fmt.Sprintf("Calendar publisher suggests a refresh interval of %d day(s) and %s; task schedule is unchanged", days, clock))
				return
			}
		}
	}
}

func (*Source) OpenContent(_ context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	var content connectors.SourceContent
	if err := json.Unmarshal(req.Item.Opaque, &content); err != nil {
		return content, errors.New("webcal: invalid parsed content")
	}
	return content, nil
}

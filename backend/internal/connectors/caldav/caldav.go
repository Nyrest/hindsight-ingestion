// Package caldav reads calendar collections through CalDAV.
package caldav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/webcal"
)

type Source struct{}

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: "caldav", Name: "CalDAV", Description: "CalDAV calendars for events and to-dos",
		Fields: []connectors.FieldSpec{
			{Key: "baseUrl", Label: "Server URL", Type: connectors.FieldURL, Required: true,
				Help: "Use a CalDAV service, calendar home or calendar URL on the final server host."},
			{Key: "username", Label: "Username", Type: connectors.FieldString},
			{Key: "password", Label: "Password / app password", Type: connectors.FieldPassword, Secret: true},
			{Key: "bearerToken", Label: "Bearer token", Type: connectors.FieldPassword, Secret: true,
				Help: "Use instead of username/password if the server expects a token."},
		},
		Validate: func(cred connectors.Credential) error { _, err := newClient(cred); return err },
	})
	connectors.RegisterSource(&Source{})
}

func (*Source) Info() connectors.SourceInfo {
	calendar := (&webcal.Source{}).Info()
	return connectors.SourceInfo{
		Type: "caldav", Name: "CalDAV", CredentialTypes: []string{"caldav"}, BrowseKinds: []string{connectors.KindFolder},
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalInventory, DeletionMode: connectors.DeletionScanGeneration},
		Fields: []connectors.FieldSpec{
			{Key: "calendarUrl", Label: "Calendar URL", Type: connectors.FieldString, Required: true, Browse: true, BrowseKind: connectors.KindFolder,
				Effect: connectors.EffectRebaseline, Help: "Select one calendar. Reads all available events and to-dos; complete scans remove missing documents."},
			{Key: "maxFeedSizeMB", Label: "Maximum CalDAV response size (MiB)", Type: connectors.FieldNumber, Default: 20, Effect: connectors.EffectReconcile},
			{Key: "maxComponents", Label: "Maximum calendar components", Type: connectors.FieldNumber, Default: 20000, Effect: connectors.EffectReconcile},
		},
		FilterFields: calendar.FilterFields,
	}
}

func (s *Source) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	if strings.TrimSpace(connectors.AsString(cfg["calendarUrl"])) == "" {
		return &connectors.FieldError{Field: "calendarUrl", Message: "Select a calendar"}
	}
	if _, err := endpoint(connectors.AsString(cfg["calendarUrl"]), true); err != nil {
		return &connectors.FieldError{Field: "calendarUrl", Message: "Use an HTTP(S) calendar URL or an absolute server path without credentials, query or fragment"}
	}
	return (&webcal.Source{}).ValidateConfig(cfg, filter)
}

func (*Source) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	c, err := newClient(cred)
	if err != nil {
		return "", err
	}
	if _, err := c.calendars(ctx); err != nil {
		return "", err
	}
	return "CalDAV calendars are readable", nil
}

func (*Source) Browse(ctx context.Context, cred connectors.Credential, _ connectors.BrowseRequest) (connectors.BrowseResult, error) {
	c, err := newClient(cred)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	calendars, err := c.calendars(ctx)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	result := connectors.BrowseResult{Items: []connectors.BrowseItem{}, Breadcrumbs: []connectors.Breadcrumb{}}
	for _, calendar := range calendars {
		result.Items = append(result.Items, connectors.BrowseItem{ID: calendar.url.String(), Path: calendar.url.Path,
			Name: calendar.name, Kind: connectors.KindFolder, Selectable: true})
	}
	return result, nil
}

// CredentialTarget fences scans when the endpoint, account or authorization changes.
func CredentialTarget(cred connectors.Credential) string {
	headers := map[string]string{}
	for key, value := range cred.Headers {
		headers[strings.ToLower(key)] = value
	}
	data, _ := json.Marshal([]any{cred.String("baseUrl"), cred.String("username"), cred.String("password"), cred.String("bearerToken"), headers})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type scanSettings struct {
	location      *time.Location
	maxBytes      int64
	maxComponents int
}

func scanConfiguration(cfg map[string]any, zone string) (scanSettings, error) {
	settings := scanSettings{location: time.UTC, maxBytes: 20 << 20, maxComponents: 20000}
	if zone != "" {
		location, err := time.LoadLocation(zone)
		if err != nil {
			return settings, errors.New("caldav: invalid configured timezone")
		}
		settings.location = location
	}
	if value, ok := cfg["maxFeedSizeMB"]; ok {
		n, _ := connectors.AsFloat(value)
		settings.maxBytes = int64(n) << 20
	}
	if value, ok := cfg["maxComponents"]; ok {
		n, _ := connectors.AsFloat(value)
		settings.maxComponents = int(n)
	}
	return settings, nil
}

func (s *Source) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	if err := s.ValidateConfig(req.Config, req.Filter); err != nil {
		return connectors.ScanResult{}, err
	}
	settings, err := scanConfiguration(req.Config, req.Timezone)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	c, err := newClient(req.Credential)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	report, err := c.calendarReport(ctx, connectors.AsString(req.Config["calendarUrl"]), settings.maxBytes)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	items, err := c.reportItems(ctx, report, settings, req.Log)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	// Parse the entire response before emitting any items so partial reads cannot change the inventory.
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return connectors.ScanResult{}, err
		}
		if err := req.Emit(item); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	return connectors.ScanResult{Complete: true}, nil
}

func (c *client) calendarReport(ctx context.Context, address string, maxBytes int64) (multistatus, error) {
	target, err := c.resolve(c.base, address)
	if err != nil {
		return multistatus{}, err
	}
	// Verify the selected collection before accepting an empty REPORT as a deletion baseline.
	properties, err := c.propfind(ctx, target, "0")
	if err != nil {
		return multistatus{}, err
	}
	selected, ok, err := c.calendar(properties, properties.url)
	if err != nil {
		return multistatus{}, err
	}
	if !ok {
		return multistatus{}, errors.New("caldav: selected URL is not a readable calendar collection")
	}
	return c.request(ctx, davRequest{method: "REPORT", target: selected.url, depth: "1", body: queryBody, maxBytes: maxBytes})
}

func (c *client) reportItems(ctx context.Context, report multistatus, settings scanSettings, log func(string, string)) ([]connectors.SourceItem, error) {
	items := []connectors.SourceItem{}
	seen := map[string]bool{}
	for _, response := range report.Responses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resource, err := c.resolve(report.url, response.Href)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(resource.Path, strings.TrimRight(report.url.Path, "/")+"/") {
			return nil, errors.New("caldav: calendar resource is outside the selected collection")
		}
		snapshot, err := response.snapshot(settings)
		if err != nil {
			return nil, err
		}
		settings.maxComponents -= snapshot.Components
		for _, warning := range snapshot.Warnings {
			if log != nil {
				log("warn", warning)
			}
		}
		for _, item := range snapshot.Items {
			if seen[item.ID] {
				return nil, errors.New("caldav: duplicate UID across calendar resources; deletion disabled")
			}
			seen[item.ID] = true
			item.Metadata["caldav_calendar_url"] = report.url.String()
			item.Metadata["caldav_resource_url"] = resource.String()
			items = append(items, item)
		}
	}
	return items, nil
}

func (response response) snapshot(settings scanSettings) (webcal.Snapshot, error) {
	calendarData := ""
	for _, ps := range response.Propstat {
		if success(ps.Status) && ps.Prop.CalendarData != nil {
			calendarData = *ps.Prop.CalendarData
		}
	}
	if calendarData == "" {
		return webcal.Snapshot{}, errors.New("caldav: incomplete calendar-data response; deletion disabled")
	}
	snapshot, err := webcal.ParseSnapshot([]byte(calendarData), settings.maxComponents, settings.location)
	if err != nil {
		return snapshot, errors.New(strings.ReplaceAll(err.Error(), "webcal:", "caldav:"))
	}
	return snapshot, nil
}

func (*Source) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	return (&webcal.Source{}).OpenContent(ctx, req)
}

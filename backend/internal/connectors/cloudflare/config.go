package cloudflare

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: connectors.SourceCloudflareCrawl, Name: "Cloudflare Browser Run",
		Description: "Cloudflare account and API token with Browser Run Edit permission",
		Fields: []connectors.FieldSpec{
			{Key: "accountId", Label: "Account ID", Type: connectors.FieldString, Required: true},
			{Key: "apiToken", Label: "API token", Type: connectors.FieldPassword, Required: true, Secret: true},
		},
	})
	connectors.RegisterSource(&Connector{})
}

var resourceTypes = []string{"document", "stylesheet", "image", "media", "font", "script", "texttrack", "xhr", "fetch", "prefetch", "eventsource", "websocket", "manifest", "signedexchange", "ping", "cspviolationreport", "preflight", "other"}

func options(values ...string) []connectors.Option {
	choices := make([]connectors.Option, len(values))
	for index, label := range values {
		choices[index] = connectors.Option{Value: label, Label: label}
	}
	return choices
}

func (c *Connector) Info() connectors.SourceInfo {
	fields := []connectors.FieldSpec{
		{Key: "url", Label: "Website URL", Type: connectors.FieldURL, Required: true},
		{Key: "source", Label: "URL discovery", Type: connectors.FieldSelect, Required: true, Default: "all", Options: options("all", "sitemaps", "links")},
		{Key: "render", Label: "JavaScript rendering", Type: connectors.FieldBoolean, Default: false, Help: "Enable for websites whose content loads in the browser. Browser time may be billed."},
		{Key: "limit", Label: "Maximum pages", Type: connectors.FieldNumber, Default: 100, Help: "Free accounts allow up to 100 pages per crawl. Reaching the page limit prevents deletion reconciliation."},
		{Key: "depth", Label: "Maximum link depth", Type: connectors.FieldNumber, Default: 20},
		{Key: "maxAge", Label: "Cache age (seconds)", Type: connectors.FieldNumber, Default: 3600, Help: "Full reconciliation always fetches fresh content."},
		{Key: "includeExternalLinks", Label: "Include external websites", Type: connectors.FieldBoolean, Default: false},
		{Key: "includeSubdomains", Label: "Include subdomains", Type: connectors.FieldBoolean, Default: false},
		{Key: "includePatterns", Label: "Include URL patterns", Type: connectors.FieldStringList, Default: []string{}, Help: "One pattern per line. * matches within a path segment; ** matches across segments."},
		{Key: "excludePatterns", Label: "Exclude URL patterns", Type: connectors.FieldStringList, Default: []string{}, Help: "Exclusion takes precedence over inclusion."},
		{Key: "maxPageSizeMB", Label: "Maximum page size (MiB)", Type: connectors.FieldNumber, Default: 5},
		{Key: "waitUntil", Label: "Navigation wait condition", Type: connectors.FieldSelect, Required: true, Default: "domcontentloaded", Options: options("load", "domcontentloaded", "networkidle0", "networkidle2"), RenderOnly: true},
		{Key: "navigationTimeout", Label: "Navigation timeout (ms)", Type: connectors.FieldNumber, Default: 60000, RenderOnly: true},
		{Key: "waitForSelector", Label: "Content CSS selector", Type: connectors.FieldString, RenderOnly: true},
		{Key: "selectorTimeout", Label: "Selector timeout (ms)", Type: connectors.FieldNumber, Default: 30000, RenderOnly: true},
		{Key: "selectorVisible", Label: "Wait for visible content", Type: connectors.FieldBoolean, Default: true, RenderOnly: true},
		{Key: "waitForTimeout", Label: "Additional wait (ms)", Type: connectors.FieldNumber, Default: 0, RenderOnly: true},
		{Key: "rejectResourceTypes", Label: "Blocked resource types", Type: connectors.FieldMultiSelect, Default: []string{"image", "media", "font"}, Options: options(resourceTypes...), RenderOnly: true},
	}
	for index := range fields {
		fields[index].Effect = connectors.EffectRebaseline
	}
	return connectors.SourceInfo{
		Type: connectors.SourceCloudflareCrawl, Name: "Cloudflare Browser Run", CredentialTypes: []string{connectors.SourceCloudflareCrawl},
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalHighWater, DeletionMode: connectors.DeletionFullReconcile},
		Fields:       fields, BrowseKinds: []string{}, FilterFields: []connectors.FilterFieldSpec{
			{Key: "url", Label: "Page URL", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "name", Label: "Page title", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "size", Label: "Size (bytes)", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
		},
	}
}

func (c *Connector) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	if _, err := NormalizeURL(connectors.AsString(cfg["url"])); err != nil {
		return &connectors.FieldError{Field: "url", Message: err.Error()}
	}
	if filter.Mode == "advanced" {
		return fmt.Errorf("advanced filters are unavailable for Cloudflare Crawl")
	}
	for _, field := range c.Info().Fields {
		if field.RenderOnly && !connectors.AsBool(cfg["render"]) {
			continue
		}
		if err := validateField(cfg, field); err != nil {
			return err
		}
	}
	return connectors.ValidateFilter(filter, c.Info().FilterFields, false)
}

func validateField(cfg map[string]any, field connectors.FieldSpec) error {
	raw, exists := cfg[field.Key]
	if !exists {
		raw = field.Default
	}
	invalid := func(message string) error { return &connectors.FieldError{Field: field.Key, Message: message} }
	switch field.Type {
	case connectors.FieldBoolean:
		if _, ok := raw.(bool); !ok {
			return invalid("Expected a boolean")
		}
	case connectors.FieldString, connectors.FieldURL:
		if raw != nil {
			if _, ok := raw.(string); !ok {
				return invalid("Expected text")
			}
		}
	case connectors.FieldNumber:
		number, ok := connectors.AsFloat(raw)
		minimum, maximum := numberBounds(field.Key)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < minimum || number > maximum {
			return invalid(fmt.Sprintf("Expected an integer between %.0f and %.0f", minimum, maximum))
		}
	case connectors.FieldSelect:
		for _, choice := range field.Options {
			if choice.Value == raw {
				return nil
			}
		}
		return invalid("Select a valid option")
	case connectors.FieldStringList, connectors.FieldMultiSelect:
		entries, err := stringList(raw)
		if err != nil {
			return invalid(err.Error())
		}
		if field.Type == connectors.FieldMultiSelect {
			for _, entry := range entries {
				valid := false
				for _, choice := range field.Options {
					valid = valid || entry == choice.Value
				}
				if !valid {
					return invalid("Unknown resource type: " + entry)
				}
			}
		}
	}
	return nil
}

func numberBounds(key string) (float64, float64) {
	switch key {
	case "limit", "depth":
		return 1, 100000
	case "maxAge":
		return 0, 604800
	case "maxPageSizeMB":
		return 1, 100
	case "navigationTimeout":
		return 1, 60000
	case "selectorTimeout":
		return 1, 120000
	default:
		return 0, 120000
	}
}

func stringList(raw any) ([]string, error) {
	switch entries := raw.(type) {
	case nil:
		return []string{}, nil
	case []string:
		texts := make([]string, len(entries))
		for index, entry := range entries {
			if strings.TrimSpace(entry) == "" {
				return nil, fmt.Errorf("Expected an array of non-empty strings")
			}
			texts[index] = strings.TrimSpace(entry)
		}
		return texts, nil
	case []any:
		texts := make([]string, len(entries))
		for index, entry := range entries {
			text, ok := entry.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("Expected an array of non-empty strings")
			}
			texts[index] = strings.TrimSpace(text)
		}
		return texts, nil
	default:
		return nil, fmt.Errorf("Expected an array of strings")
	}
}

func numberConfig(cfg map[string]any, key string, fallback int64) int64 {
	if raw, exists := cfg[key]; exists {
		if number, ok := connectors.AsFloat(raw); ok {
			return int64(number)
		}
	}
	return fallback
}

func NormalizeURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return "", fmt.Errorf("Enter an HTTP(S) URL without embedded credentials")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port != "" && !(parsed.Scheme == "https" && port == "443") && !(parsed.Scheme == "http" && port == "80") {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	parsed.Host, parsed.Fragment, parsed.RawFragment = host, "", ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

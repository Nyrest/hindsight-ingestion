package notion

import (
	"math"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

const defaultMaxDepth = 3

func notionFields() []connectors.FieldSpec {
	return []connectors.FieldSpec{
		{Key: "mode", Label: "Source mode", Type: connectors.FieldSelect, Default: "database", Effect: connectors.EffectRebaseline,
			Options: []connectors.Option{{Value: "database", Label: "Database"}, {Value: "pages", Label: "Pages"}}},
		{Key: "dataSourceId", Label: "Data source", Type: connectors.FieldString, Browse: true, BrowseKind: connectors.KindDataSource,
			Help: "Notion data source ID (not the database ID). Use Browse to pick one shared with the integration.", Effect: connectors.EffectRebaseline},
		{Key: "pageIds", Label: "Pages", Type: connectors.FieldStringList, Browse: true, BrowseKind: connectors.KindPage,
			Help: "Select shared pages with Browse, or enter one page ID per line.", Effect: connectors.EffectRebaseline},
		{Key: "includeSubpages", Label: "Include subpages", Type: connectors.FieldBoolean, Default: false,
			Help: "Sync child pages and their content.", Effect: connectors.EffectRebaseline},
		{Key: "maxDepth", Label: "Maximum traversal depth", Type: connectors.FieldNumber, Default: defaultMaxDepth, Nullable: true,
			Help: "Root pages have depth 0; direct children have depth 1. Default: 3.", Effect: connectors.EffectRebaseline},
	}
}

type config struct {
	mode            string
	pageIDs         []string
	includeSubpages bool
	maxDepth        int
}

func parseConfig(cfg map[string]any) (config, error) {
	parsed := config{mode: connectors.AsString(cfg["mode"]), includeSubpages: connectors.AsBool(cfg["includeSubpages"]), maxDepth: defaultMaxDepth}
	if parsed.mode == "" {
		parsed.mode = "database"
	}
	switch parsed.mode {
	case "database":
		if err := connectors.Require(cfg, "dataSourceId"); err != nil {
			return parsed, err
		}
	case "pages":
		var err error
		parsed.pageIDs, err = pageIDs(cfg["pageIds"])
		if err != nil {
			return parsed, err
		}
	default:
		return parsed, &connectors.FieldError{Field: "mode", Message: "Select Database or Pages"}
	}
	if raw, exists := cfg["maxDepth"]; exists {
		if raw == nil {
			parsed.maxDepth = -1
		} else {
			depth, ok := connectors.AsFloat(raw)
			if !ok || math.IsNaN(depth) || math.IsInf(depth, 0) || depth < 0 || depth > math.MaxInt32 || depth != math.Trunc(depth) {
				return parsed, &connectors.FieldError{Field: "maxDepth", Message: "Enter a non-negative integer up to 2147483647, or null for unlimited"}
			}
			parsed.maxDepth = int(depth)
		}
	}
	return parsed, nil
}

func pageIDs(raw any) ([]string, error) {
	var entries []string
	switch ids := raw.(type) {
	case []string:
		entries = ids
	case []any:
		for _, entry := range ids {
			id, ok := entry.(string)
			if !ok {
				return nil, &connectors.FieldError{Field: "pageIds", Message: "Page IDs must be strings"}
			}
			entries = append(entries, id)
		}
	default:
		return nil, &connectors.FieldError{Field: "pageIds", Message: "Select at least one page"}
	}
	var unique []string
	seen := map[string]bool{}
	for _, entry := range entries {
		id := strings.TrimSpace(entry)
		if id != "" && !seen[pageKey(id)] {
			unique = append(unique, id)
			seen[pageKey(id)] = true
		}
	}
	if len(unique) == 0 {
		return nil, &connectors.FieldError{Field: "pageIds", Message: "Select at least one page"}
	}
	return unique, nil
}

func pageKey(id string) string {
	return strings.ToLower(strings.ReplaceAll(id, "-", ""))
}

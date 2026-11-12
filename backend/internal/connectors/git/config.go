// Package git implements read-only Git tree snapshots using shallow bare repositories.
package git

import (
	"encoding/hex"
	"math"
	"net/url"
	"path"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

type sourceConfig struct {
	URL        string
	RefType    string
	Ref        string
	KeepCache  bool
	CacheLimit int64
	FileLimit  int64
	TagPattern string
}

func invalid(field, message string) error {
	return &connectors.FieldError{Field: field, Message: message}
}

func parseURL(raw string) (*transport.Endpoint, error) {
	if strings.ContainsAny(raw, "\x00\r\n") || raw == "" {
		return nil, invalid("repositoryUrl", "Enter an HTTP(S) or SSH Git repository URL")
	}
	raw = strings.TrimSpace(raw)
	endpoint, err := transport.NewEndpoint(raw)
	if err != nil || endpoint.Host == "" || endpoint.Path == "" {
		return nil, invalid("repositoryUrl", "Enter an HTTP(S) or SSH Git repository URL")
	}
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	if err := normalizeURLPath(raw, endpoint); err != nil {
		return nil, err
	}
	return endpoint, nil
}

func validateEndpoint(endpoint *transport.Endpoint) error {
	switch endpoint.Protocol {
	case "http", "https", "ssh":
	default:
		return invalid("repositoryUrl", "Only HTTP(S) and SSH Git repositories are supported")
	}
	if endpoint.Password != "" || (endpoint.Protocol != "ssh" && endpoint.User != "") {
		return invalid("repositoryUrl", "Store authentication in a Git credential, not in the repository URL")
	}
	if endpoint.Protocol == "ssh" && (strings.HasPrefix(endpoint.Path, ":") || strings.HasPrefix(endpoint.Path, "-") || strings.ContainsAny(endpoint.Path, "\x00\r\n")) {
		return invalid("repositoryUrl", "Enter a valid SSH repository path")
	}
	if endpoint.Port < 0 || endpoint.Port > 65535 {
		return invalid("repositoryUrl", "Enter a valid Git server port")
	}
	return nil
}

func normalizeURLPath(raw string, endpoint *transport.Endpoint) error {
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return invalid("repositoryUrl", "Repository URLs must not contain query parameters or fragments")
		}
		if endpoint.Protocol != "ssh" {
			endpoint.Path = parsed.EscapedPath()
		}
	}
	return nil
}

func parseConfig(cfg map[string]any) (sourceConfig, error) {
	repository, _ := cfg["repositoryUrl"].(string)
	_, err := parseURL(repository)
	if err != nil {
		return sourceConfig{}, err
	}
	// Re-serializing an SCP-style endpoint changes its relative path to an absolute one.
	parsed := sourceConfig{URL: strings.TrimSpace(repository), RefType: connectors.AsString(cfg["refType"]), Ref: connectors.AsString(cfg["ref"]), KeepCache: connectors.BoolDefault(cfg, "keepCache", true), TagPattern: connectors.AsString(cfg["tagPattern"])}
	if parsed.RefType == "" {
		parsed.RefType = "branch"
	}
	if parsed.RefType == "latest_tag" && parsed.TagPattern == "" {
		parsed.TagPattern = "*"
	}
	if err := validateReference(parsed); err != nil {
		return parsed, err
	}
	parsed.CacheLimit, err = sizeLimit(cfg, "maxCacheSizeMB", 2048)
	if err != nil {
		return parsed, err
	}
	parsed.FileLimit, err = sizeLimit(cfg, "maxFileSizeMB", 0)
	return parsed, err
}

func validateReference(parsed sourceConfig) error {
	switch parsed.RefType {
	case "branch", "tag":
		return validateNamedReference(parsed)
	case "commit":
		decoded, err := hex.DecodeString(parsed.Ref)
		if err != nil || len(decoded) != 20 {
			return invalid("ref", "Enter a full 40-character commit SHA")
		}
	case "latest_tag":
		if _, err := path.Match(parsed.TagPattern, ""); err != nil {
			return invalid("tagPattern", "Enter a valid tag glob pattern")
		}
	default:
		return invalid("refType", "Select branch, tag, latest tag or commit")
	}
	return nil
}

func validateNamedReference(parsed sourceConfig) error {
	if parsed.Ref == "" {
		if parsed.RefType == "tag" {
			return invalid("ref", "Enter a tag name")
		}
		return nil
	}
	name := plumbing.NewBranchReferenceName(parsed.Ref)
	if parsed.RefType == "tag" {
		name = plumbing.NewTagReferenceName(parsed.Ref)
	}
	if name.Validate() != nil {
		return invalid("ref", "Enter a valid short branch or tag name")
	}
	return nil
}

func sizeLimit(cfg map[string]any, key string, fallback int64) (int64, error) {
	if cfg[key] == nil || connectors.AsString(cfg[key]) == "" {
		return fallback << 20, nil
	}
	size, ok := connectors.AsFloat(cfg[key])
	if !ok || size < 1 || size > 1048576 || math.Trunc(size) != size {
		return 0, invalid(key, "Enter a whole number of MiB between 1 and 1048576")
	}
	return int64(size) << 20, nil
}

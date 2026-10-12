package email

import (
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

const SourceType = "email"
const CredentialType = "imap"
const defaultBodyLimit = 10 << 20

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: CredentialType, Name: "IMAP", Description: "Email account using a password or app password", Validate: ValidateCredentialConfig,
		Fields: []connectors.FieldSpec{
			{Key: "host", Label: "Host", Type: connectors.FieldString, Required: true},
			{Key: "port", Label: "Port", Type: connectors.FieldNumber, Required: true, Default: 993},
			{Key: "username", Label: "Username", Type: connectors.FieldString, Required: true},
			{Key: "password", Label: "Password / app password", Type: connectors.FieldPassword, Required: true, Secret: true},
			{Key: "security", Label: "Connection security", Type: connectors.FieldSelect, Required: true, Default: "tls", Options: []connectors.Option{{Value: "tls", Label: "TLS"}, {Value: "starttls", Label: "STARTTLS"}}},
			{Key: "connectionTimeout", Label: "Connection timeout (seconds)", Type: connectors.FieldNumber, Default: 30},
			{Key: "commandTimeout", Label: "Command timeout (seconds)", Type: connectors.FieldNumber, Default: 60},
		},
	})
}

func (*Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{Type: SourceType, Name: "Email", CredentialTypes: []string{CredentialType}, BrowseKinds: []string{connectors.KindFolder},
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalHighWater, DeletionMode: connectors.DeletionScanGeneration},
		Fields: []connectors.FieldSpec{
			{Key: "folders", Label: "Mail folders", Type: connectors.FieldStringList, Required: true, Default: []string{}, Browse: true, BrowseKind: connectors.KindFolder, Effect: connectors.EffectReconcile, Help: "Missing logical emails are deleted after a successful complete scan. Default folders: INBOX and server-designated Sent."},
			{Key: "includeSubfolders", Label: "Include sub-folders", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
			{Key: "excludeFolders", Label: "Excluded mail folders", Type: connectors.FieldStringList, Default: []string{}, Browse: true, BrowseKind: connectors.KindFolder, Effect: connectors.EffectReconcile},
			{Key: "stripQuotedText", Label: "Remove quoted reply history", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
			{Key: "maxBodyBytes", Label: "Maximum decoded body size (bytes)", Type: connectors.FieldNumber, Default: defaultBodyLimit, Effect: connectors.EffectReconcile, Help: "Attachments are listed without downloading. Oversized bodies retain headers and a warning."},
		}, FilterFields: []connectors.FilterFieldSpec{
			{Key: "subject", Label: "Subject", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "from", Label: "From", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "to", Label: "To", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "cc", Label: "Cc", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "date", Label: "Mail date", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
			{Key: "size", Label: "Size (bytes)", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
			{Key: "hasAttachments", Label: "Has attachments", Type: connectors.FilterBoolean, Operators: connectors.BooleanOps},
		}}
}

func ValidateCredentialConfig(cred connectors.Credential) error {
	for _, key := range []string{"username", "password"} {
		if cred.String(key) == "" {
			return &connectors.FieldError{Field: key, Message: "Required"}
		}
	}
	host := cred.String("host")
	if host == "" || strings.ContainsAny(host, "/\\?#@ \t\r\n") {
		return &connectors.FieldError{Field: "host", Message: "Host must be a hostname or IP address"}
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return &connectors.FieldError{Field: "host", Message: "Host must not include a port"}
	}
	for _, field := range []string{"port", "connectionTimeout", "commandTimeout"} {
		max := float64(3600)
		if field == "port" {
			max = 65535
		}
		n, ok := connectors.AsFloat(cred.Config[field])
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > max || n != math.Trunc(n) {
			return &connectors.FieldError{Field: field, Message: "Must be a positive integer within the supported range"}
		}
	}
	if cred.String("security") != "tls" && cred.String("security") != "starttls" {
		return &connectors.FieldError{Field: "security", Message: "Select TLS or STARTTLS"}
	}
	return nil
}

func AccountKey(cred connectors.Credential) string {
	return digest(strings.ToLower(cred.String("host")), cred.String("port"), cred.String("username"))
}

func folderNames(cfg map[string]any, key string) ([]string, error) {
	var names []string
	switch list := cfg[key].(type) {
	case []string:
		names = list
	case []any:
		for _, entry := range list {
			name, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be an array of mailbox names", key)
			}
			names = append(names, name)
		}
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("%s must be an array of mailbox names", key)
	}
	seen := map[string]bool{}
	unique := []string{}
	for _, name := range names {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n\x00") {
			return nil, fmt.Errorf("%s contains an invalid mailbox name", key)
		}
		if !seen[name] {
			unique = append(unique, name)
			seen[name] = true
		}
	}
	return unique, nil
}

func (c *Connector) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	for _, key := range []string{"includeSubfolders", "stripQuotedText"} {
		if cfg[key] != nil {
			if _, ok := cfg[key].(bool); !ok {
				return &connectors.FieldError{Field: key, Message: "Must be a boolean"}
			}
		}
	}
	for _, key := range []string{"folders", "excludeFolders"} {
		names, err := folderNames(cfg, key)
		if err != nil {
			return &connectors.FieldError{Field: key, Message: err.Error()}
		}
		if key == "folders" && len(names) == 0 {
			return &connectors.FieldError{Field: key, Message: "Select at least one mail folder"}
		}
	}
	if _, err := bodyLimit(cfg); err != nil {
		return &connectors.FieldError{Field: "maxBodyBytes", Message: err.Error()}
	}
	return connectors.ValidateFilter(filter, c.Info().FilterFields, false)
}

func bodyLimit(cfg map[string]any) (int64, error) {
	if cfg["maxBodyBytes"] == nil {
		return defaultBodyLimit, nil
	}
	n, ok := connectors.AsFloat(cfg["maxBodyBytes"])
	if !ok || math.IsNaN(n) || n < 1 || n > 32<<20 || n != math.Trunc(n) {
		return 0, fmt.Errorf("Body limit must be an integer between 1 and %s bytes", strconv.Itoa(32<<20))
	}
	return int64(n), nil
}

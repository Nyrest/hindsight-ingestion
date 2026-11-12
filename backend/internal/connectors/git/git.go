package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{Type: "git", Name: "Git", Description: "Git authentication for HTTP(S) or SSH repositories", Validate: validateCredential, Fields: []connectors.FieldSpec{
		{Key: "username", Label: "Username", Type: connectors.FieldString},
		{Key: "password", Label: "Password / access token", Type: connectors.FieldPassword, Secret: true},
		{Key: "privateKey", Label: "SSH private key", Type: connectors.FieldTextarea, Secret: true},
		{Key: "passphrase", Label: "Private key passphrase", Type: connectors.FieldPassword, Secret: true},
		{Key: "hostKey", Label: "SSH host key fingerprint", Type: connectors.FieldString, Placeholder: "SHA256:…", Help: "Required for SSH. Use the SHA256 fingerprint supplied by your server administrator."},
		{Key: "caCert", Label: "TLS CA certificate (PEM)", Type: connectors.FieldTextarea},
	}})
	connectors.RegisterSource(&Source{})
}

type Source struct {
	CacheDir string
	locks    sync.Map
	sessions sync.Map
}

func (*Source) Info() connectors.SourceInfo {
	return connectors.SourceInfo{Type: "git", Name: "Git", CredentialTypes: []string{"git"}, CredentialOptional: true,
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalConditionalInventory, DeletionMode: connectors.DeletionScanGeneration, SupportsFiles: true},
		Fields: []connectors.FieldSpec{
			{Key: "repositoryUrl", Label: "Repository URL", Type: connectors.FieldString, Required: true, Effect: connectors.EffectRebaseline, Placeholder: "https://example.com/org/repo.git", Help: "HTTP(S), ssh:// and git@host:path URLs. Public HTTP(S) repositories need no credential."},
			{Key: "refType", Label: "Reference type", Type: connectors.FieldSelect, Default: "branch", Effect: connectors.EffectRebaseline, Options: []connectors.Option{{Value: "branch", Label: "Branch"}, {Value: "tag", Label: "Tag"}, {Value: "latest_tag", Label: "Latest semantic version tag"}, {Value: "commit", Label: "Commit SHA"}}},
			{Key: "ref", Label: "Branch / tag / commit", Type: connectors.FieldString, Effect: connectors.EffectRebaseline, Help: "Leave blank for the default branch. Tags follow the source if moved; commit mode requires a full SHA. All downloads use depth 1."},
			{Key: "tagPattern", Label: "Tag pattern", Type: connectors.FieldString, Effect: connectors.EffectRebaseline, Help: "Used only for latest semantic version tag, for example v*. Defaults to *."},
			{Key: "keepCache", Label: "Keep local Git cache", Type: connectors.FieldBoolean, Default: true},
			{Key: "maxCacheSizeMB", Label: "Maximum Git cache size (MiB)", Type: connectors.FieldNumber, Default: 2048, Help: "Old caches are rebuilt at the limit. A snapshot that exceeds the limit fails without deletion reconciliation."},
			{Key: "maxFileSizeMB", Label: "Maximum Git file size (MiB)", Type: connectors.FieldNumber, Effect: connectors.EffectReconcile, Help: "Optional ingestion limit, capped by the global file limit. Filters do not reduce Git pack downloads. LFS pointers and submodules are skipped."},
		}, FilterFields: []connectors.FilterFieldSpec{
			{Key: "name", Label: "File name", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "extension", Label: "Extension", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "size", Label: "Size (bytes)", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
		}}
}

func (s *Source) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	if _, err := parseConfig(cfg); err != nil {
		return err
	}
	return connectors.ValidateFilter(filter, s.Info().FilterFields, false)
}
func (*Source) ValidateCredential(_ context.Context, cred connectors.Credential) (string, error) {
	if err := validateCredential(cred); err != nil {
		return "", err
	}
	return "Git authentication configuration is valid; repository connectivity is checked when the task runs", nil
}
func (*Source) Browse(context.Context, connectors.Credential, connectors.BrowseRequest) (connectors.BrowseResult, error) {
	return connectors.BrowseResult{}, errors.New("configure the repository URL and reference in the task")
}

func (s *Source) cacheRoot() (string, error) {
	root := s.CacheDir
	if root == "" {
		root = os.Getenv("GIT_CACHE_DIR")
	}
	if root == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		root = filepath.Join(base, "hindsight-ingestion", "git")
	}
	return filepath.Abs(root)
}

package git

import (
	"math"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestGitConfigurationBoundaries(t *testing.T) {
	for _, repository := range []string{"https://example.com/org/repo.git", "http://localhost:3000/org/repo.git", "ssh://git@example.com:2222/org/repo.git", "git@example.com:org/repo.git"} {
		if _, err := parseConfig(map[string]any{"repositoryUrl": repository}); err != nil {
			t.Errorf("valid URL %q: %v", repository, err)
		}
	}
	for _, repository := range []string{"file:///etc/git", "/local/repo", "ext::sh -c anything", "git://example.com/repo.git", "https://user:secret@example.com/repo.git", "https://example.com/repo?token=secret", "https://example.com/repo#fragment", "ssh://git:secret@example.com/repo.git", "https://example.com/repo\n"} {
		if _, err := parseConfig(map[string]any{"repositoryUrl": repository}); err == nil {
			t.Errorf("invalid repository %q was accepted", repository)
		}
	}
	cases := []map[string]any{
		{"refType": "tag"}, {"refType": "commit", "ref": "abc"}, {"refType": "branch", "ref": "bad..ref"}, {"refType": "latest_tag", "tagPattern": "["}, {"maxCacheSizeMB": 0}, {"maxFileSizeMB": -1}, {"maxFileSizeMB": 1.5}, {"maxFileSizeMB": math.NaN()}, {"maxFileSizeMB": math.Inf(1)},
	}
	for _, cfg := range cases {
		cfg["repositoryUrl"] = "https://example.com/repo.git"
		if _, err := parseConfig(cfg); err == nil {
			t.Errorf("invalid config accepted: %v", cfg)
		}
	}
	if err := (&Source{}).ValidateConfig(map[string]any{"repositoryUrl": "https://example.com/repo.git"}, connectors.Filter{}); err != nil {
		t.Fatal(err)
	}
}

func TestGitLFSPointerRecognition(t *testing.T) {
	valid := "version https://git-lfs.github.com/spec/v1\noid sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nsize 100\n"
	if !isLFSPointer([]byte(valid)) {
		t.Fatal("valid LFS pointer was not recognized")
	}
	for _, body := range []string{"", "version https://git-lfs.github.com/spec/v1\nThis is documentation, not an LFS pointer.", "version https://git-lfs.github.com/spec/v1\noid sha256:invalid\nsize 100\n"} {
		if isLFSPointer([]byte(body)) {
			t.Fatalf("ordinary or invalid content was skipped: %q", body)
		}
	}
}

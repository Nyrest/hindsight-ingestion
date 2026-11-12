// Package testutil provides local protocol fixtures for integration tests.
package testutil

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type GitRepository struct {
	T      *testing.T
	Root   string
	Git    string
	Server *httptest.Server
}

func NewGitRepository(t *testing.T) *GitRepository {
	t.Helper()
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git executable is required for protocol integration tests")
	}
	fixture := &GitRepository{T: t, Root: filepath.Join(t.TempDir(), "repository"), Git: binary}
	fixture.Command("init", "--initial-branch=main", fixture.Root)
	fixture.Command("config", "user.name", "Git fixture")
	fixture.Command("config", "user.email", "fixture@example.invalid")
	fixture.Command("config", "uploadpack.allowReachableSHA1InWant", "true")
	fixture.Server = httptest.NewServer(fixture)
	t.Cleanup(fixture.Server.Close)
	return fixture
}
func (f *GitRepository) URL() string { return f.Server.URL + "/repository" }
func (f *GitRepository) Command(args ...string) string {
	f.T.Helper()
	cmd := exec.Command(f.Git, args...)
	if _, err := os.Stat(f.Root); err == nil {
		cmd.Dir = f.Root
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		f.T.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
func (f *GitRepository) Write(name, body string) {
	f.T.Helper()
	p := filepath.Join(f.Root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		f.T.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		f.T.Fatal(err)
	}
}
func (f *GitRepository) Commit(message string) string {
	f.T.Helper()
	f.Command("add", "--all")
	f.Command("commit", "--allow-empty", "-m", message)
	return f.Command("rev-parse", "HEAD")
}
func (f *GitRepository) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cmd := exec.CommandContext(r.Context(), f.Git, "http-backend")
	cmd.Env = append(os.Environ(), "GIT_PROJECT_ROOT="+filepath.Dir(f.Root), "GIT_HTTP_EXPORT_ALL=1", "PATH_INFO="+r.URL.Path, "REQUEST_METHOD="+r.Method, "QUERY_STRING="+r.URL.RawQuery, "CONTENT_TYPE="+r.Header.Get("Content-Type"), "CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10))
	cmd.Stdin = r.Body
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		http.Error(w, "Git fixture backend failed: "+stderr.String(), 500)
		return
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	headers, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	status := 200
	for key, values := range headers {
		if key == "Status" {
			status, _ = strconv.Atoi(strings.Fields(values[0])[0])
		} else {
			w.Header()[key] = values
		}
	}
	w.WriteHeader(status)
	_, _ = io.Copy(w, reader)
}

func (f *GitRepository) UploadPack(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, f.Git, "upload-pack", f.Root)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

package caldav

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

// Opt in with CALDAV_TEST_RADICALE=1. uvx runs a pinned server in temporary storage.
func TestRadicaleInteroperability(t *testing.T) {
	if os.Getenv("CALDAV_TEST_RADICALE") != "1" {
		t.Skip("set CALDAV_TEST_RADICALE=1 to run with a local Radicale server (requires uvx)")
	}
	base := startRadicale(t)
	request := func(method, path, body string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("alice", "test-password")
		req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
		if method == "MKCALENDAR" {
			req.Header.Set("Content-Type", "application/xml; charset=utf-8")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, body)
		}
	}
	request("MKCALENDAR", "/alice/work/", `<c:mkcalendar xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:set><d:prop><d:displayname>Work</d:displayname></d:prop></d:set></c:mkcalendar>`)
	series := strings.Replace(event("series", "Weekly"), "END:VEVENT", "RRULE:FREQ=WEEKLY\r\nEND:VEVENT", 1)
	request("PUT", "/alice/work/series.ics", ics(series))
	request("PUT", "/alice/work/todo.ics", ics("BEGIN:VTODO\r\nUID:todo\r\nSUMMARY:Completed task\r\nSTATUS:COMPLETED\r\nCOMPLETED:20261020T120000Z\r\nEND:VTODO"))
	cred := connectors.Credential{Config: map[string]any{"baseUrl": base, "username": "alice"}, Secrets: map[string]string{"password": "test-password"}}
	source := &Source{}
	listing, err := source.Browse(t.Context(), cred, connectors.BrowseRequest{})
	if err != nil || len(listing.Items) != 1 || listing.Items[0].Name != "Work" {
		t.Fatalf("Radicale discovery: %+v %v", listing, err)
	}
	read := func() []connectors.SourceItem {
		t.Helper()
		items := []connectors.SourceItem{}
		result, err := source.Scan(t.Context(), connectors.ScanRequest{
			Credential: cred, Config: map[string]any{"calendarUrl": listing.Items[0].ID}, Timezone: "Asia/Singapore",
			Emit: func(item connectors.SourceItem) error { items = append(items, item); return nil },
		})
		if err != nil || !result.Complete {
			t.Fatalf("Radicale scan: %+v %v", result, err)
		}
		return items
	}
	items := read()
	if len(items) != 2 {
		t.Fatalf("Radicale returned %d documents", len(items))
	}
	repeated := read()
	if repeated[0].ID != items[0].ID || repeated[0].Revision != items[0].Revision {
		t.Fatal("identical report changed identity or revision")
	}
	request("PUT", "/alice/work/series.ics", ics(strings.Replace(series, "Weekly", "Edited", 1)))
	changed := read()
	if changed[0].Revision == items[0].Revision && changed[1].Revision == items[1].Revision {
		t.Fatal("update did not change revision")
	}
	request("DELETE", "/alice/work/series.ics", "")
	if len(read()) != 1 {
		t.Fatal("deleted resource remained in inventory")
	}
	request("DELETE", "/alice/work/todo.ics", "")
	if len(read()) != 0 {
		t.Fatal("empty collection did not yield a complete empty inventory")
	}
}

func startRadicale(t *testing.T) string {
	t.Helper()
	uvx, err := exec.LookPath("uvx")
	if err != nil {
		t.Fatal(err)
	}
	// Launch the real interpreter directly so teardown also stops the server on
	// Windows, where uvx and virtualenv launchers otherwise leave child processes.
	output, err := exec.CommandContext(t.Context(), uvx, "--quiet", "--python", "3.13", "--from", "radicale==3.8.3", "python", "-c",
		`import json, sys, sysconfig; print(json.dumps([sys._base_executable, sysconfig.get_path('purelib')]))`).Output()
	if err != nil {
		t.Fatal(err)
	}
	var pythonEnvironment []string
	if err := json.Unmarshal(output, &pythonEnvironment); err != nil || len(pythonEnvironment) != 2 {
		t.Fatalf("Radicale environment: %s %v", output, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	dir := t.TempDir()
	users := filepath.Join(dir, "users")
	if err := os.WriteFile(users, []byte("alice:test-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[server]\nhosts = %s\n[auth]\ntype = htpasswd\nhtpasswd_filename = %s\nhtpasswd_encryption = plain\n[storage]\nfilesystem_folder = %s\n[logging]\nlevel = warning\n", address, users, filepath.Join(dir, "collections"))
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), pythonEnvironment[0], "-m", "radicale", "--config", configPath)
	command.Env = append(os.Environ(), "PYTHONPATH="+pythonEnvironment[1])
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { command.Process.Kill(); command.Wait() })
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			return "http://" + address
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Radicale did not start within 30 seconds")
	return ""
}

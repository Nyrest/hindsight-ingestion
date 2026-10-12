package proxy

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCONNECTAuthenticationAndBufferedGreeting(t *testing.T) {
	checked := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checked <- r.Method == http.MethodConnect && r.Host == "imap.example.com:993" && r.Header.Get("Proxy-Authorization") == "Basic dXNlcjpwYXNz" && r.Header.Get("Authorization") == ""
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n* OK IMAP ready\r\n")
		_, _ = io.Copy(conn, conn)
	}))
	defer server.Close()
	cfg := Config{Type: "http", Address: strings.TrimPrefix(server.URL, "http://"), Username: "user", Password: "pass"}
	conn, err := cfg.DialContext(t.Context(), "imap.example.com:993", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "* OK IMAP ready\r\n" || !<-checked {
		t.Fatal(line, err)
	}
}

func TestCONNECTCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	defer server.Close()
	defer close(release)
	cfg := Config{Type: "http", Address: strings.TrimPrefix(server.URL, "http://")}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		conn, err := cfg.DialContext(ctx, "imap.example.com:143", time.Second)
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled CONNECT succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock CONNECT")
	}
}

package git

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
	"github.com/Nyrest/hindsight-ingestion/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func startSSHGit(t *testing.T, repository *testutil.GitRepository) (string, connectors.Credential) {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	_, userKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	userSigner, err := ssh.NewSignerFromKey(userKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "git" || string(key.Marshal()) != string(userSigner.PublicKey().Marshal()) {
			return nil, errors.New("invalid key")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go serveGitSSH(t.Context(), raw, config, repository)
		}
	}()
	private, err := ssh.MarshalPrivateKeyWithPassphrase(userKey, "test", []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	cred := connectors.Credential{Config: map[string]any{"hostKey": ssh.FingerprintSHA256(signer.PublicKey())}, Secrets: map[string]string{"privateKey": string(pem.EncodeToMemory(private)), "passphrase": "passphrase"}, Proxy: proxy.Config{Type: "none"}}
	return "ssh://git@" + listener.Addr().String() + "/repository", cred
}

func serveGitSSH(ctx context.Context, raw net.Conn, config *ssh.ServerConfig, repository *testutil.GitRepository) {
	defer raw.Close()
	conn, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(requests)
	for request := range channels {
		if request.ChannelType() != "session" {
			_ = request.Reject(ssh.UnknownChannelType, "session required")
			continue
		}
		channel, requests, err := request.Accept()
		if err != nil {
			return
		}
		go func() {
			defer channel.Close()
			for req := range requests {
				var command struct{ Command string }
				accepted := req.Type == "exec" && ssh.Unmarshal(req.Payload, &command) == nil &&
					(command.Command == "git-upload-pack '/repository'" || command.Command == "git-upload-pack 'relative-repository'")
				_ = req.Reply(accepted, nil)
				if !accepted {
					continue
				}
				status := uint32(0)
				if err := repository.UploadPack(ctx, channel, channel, channel.Stderr()); err != nil {
					status = 1
				}
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				return
			}
		}()
	}
}

func scanCredential(t *testing.T, source *Source, cfg map[string]any, cred connectors.Credential) (connectors.ScanResult, error) {
	t.Helper()
	return source.Scan(t.Context(), connectors.ScanRequest{TaskID: "auth-task", Credential: cred, Config: cfg, Emit: func(item connectors.SourceItem) error {
		content, err := source.OpenContent(t.Context(), connectors.ContentRequest{Config: cfg, Item: item})
		if err != nil {
			return err
		}
		if content.Body != nil {
			defer content.Body.Close()
			_, err = io.Copy(io.Discard, content.Body)
		}
		return err
	}})
}

func TestGitSSHPrivateKeyAndHostValidation(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.Write("a.txt", "hello")
	repository.Commit("first")
	url, cred := startSSHGit(t, repository)
	source := &Source{CacheDir: t.TempDir()}
	cfg := map[string]any{"repositoryUrl": url}
	res, err := scanCredential(t, source, cfg, cred)
	if err != nil || !res.Complete {
		t.Fatalf("SSH scan=%+v err=%v", res, err)
	}
	cfg["repositoryUrl"] = strings.TrimSuffix(strings.TrimPrefix(url, "ssh://"), "/repository") + ":relative-repository"
	res, err = scanCredential(t, source, cfg, cred)
	if err != nil || !res.Complete {
		t.Fatalf("SCP-style SSH scan=%+v err=%v", res, err)
	}
	cred.Config["hostKey"] = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	res, err = scanCredential(t, source, cfg, cred)
	if err == nil || res.Complete || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("wrong host=%+v err=%v", res, err)
	}
}

func connectProxy(t *testing.T) (proxy.Config, *atomic.Int32) {
	t.Helper()
	tunnels := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer upstream.Close()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		tunnels.Add(1)
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close(); close(done) }()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
		<-done
	}))
	t.Cleanup(server.Close)
	return proxy.Config{Type: "http", Address: strings.TrimPrefix(server.URL, "http://")}, tunnels
}

func TestGitSSHThroughHTTPProxy(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.Write("a.txt", "hello")
	repository.Commit("first")
	url, cred := startSSHGit(t, repository)
	config, tunnels := connectProxy(t)
	cred.Proxy = config
	source := &Source{CacheDir: t.TempDir()}
	res, err := scanCredential(t, source, map[string]any{"repositoryUrl": url}, cred)
	if err != nil || !res.Complete || tunnels.Load() < 2 {
		t.Fatalf("proxied SSH=%+v err=%v tunnels=%d", res, err, tunnels.Load())
	}
}

func TestGitHTTPSAuthenticationCAAndProxy(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.Write("a.txt", "hello")
	repository.Commit("first")
	var authenticated atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "alice" || password != "token" || r.Header.Get("X-Source") != "git" {
			w.WriteHeader(401)
			return
		}
		authenticated.Add(1)
		repository.ServeHTTP(w, r)
	}))
	defer server.Close()
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	config, tunnels := connectProxy(t)
	cred := connectors.Credential{Config: map[string]any{"username": "alice", "caCert": certificate}, Secrets: map[string]string{"password": "token"}, Headers: map[string]string{"X-Source": "git"}, Proxy: config}
	source := &Source{CacheDir: t.TempDir()}
	cfg := map[string]any{"repositoryUrl": server.URL + "/repository"}
	res, err := scanCredential(t, source, cfg, cred)
	if err != nil || !res.Complete || authenticated.Load() < 2 || tunnels.Load() == 0 {
		t.Fatalf("HTTPS=%+v err=%v requests=%d tunnels=%d", res, err, authenticated.Load(), tunnels.Load())
	}
	delete(cred.Config, "caCert")
	res, err = scanCredential(t, source, cfg, cred)
	if err == nil || res.Complete {
		t.Fatal("untrusted TLS certificate was accepted")
	}
	cred.Config["caCert"] = certificate
	cred.Secrets["password"] = "wrong"
	res, err = scanCredential(t, source, cfg, cred)
	if err == nil || res.Complete {
		t.Fatal("invalid authentication was accepted")
	}
}

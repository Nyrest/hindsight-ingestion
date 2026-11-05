package sftp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
	sftpclient "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func newSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer, key
}

func startSFTP(t *testing.T) (connectors.Credential, *atomic.Int32) {
	t.Helper()
	host, _ := newSigner(t)
	user, key := newSigner(t)
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if string(password) != "secret" {
			return nil, errors.New("bad password")
		}
		return nil, nil
	}, PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(user.PublicKey().Marshal()) {
			return nil, errors.New("bad key")
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	handlers := sftpclient.InMemHandler()
	accepted := &atomic.Int32{}
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go serveSSH(raw, config, handlers)
		}
	}()
	address, port, _ := net.SplitHostPort(listener.Addr().String())
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "test", []byte("key-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	cred := connectors.Credential{ID: "sftp-test", Config: map[string]any{"host": address, "port": port, "username": "alice", "hostKey": ssh.FingerprintSHA256(host.PublicKey())}, Secrets: map[string]string{"password": "secret", "privateKey": string(pem.EncodeToMemory(block)), "passphrase": "key-passphrase"}, Proxy: proxy.Config{Type: "none"}}
	connection, err := dial(context.Background(), cred)
	if err != nil {
		t.Fatal(err)
	}
	files := connection.(*client).sftp
	if err := files.Mkdir("/docs"); err != nil {
		t.Fatal(err)
	}
	file, err := files.Create("/docs/报告.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	return cred, accepted
}

func serveSSH(raw net.Conn, config *ssh.ServerConfig, handlers sftpclient.Handlers) {
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
				var subsystem struct{ Name string }
				accepted := req.Type == "subsystem" && ssh.Unmarshal(req.Payload, &subsystem) == nil && subsystem.Name == "sftp"
				_ = req.Reply(accepted, nil)
				if accepted {
					server := sftpclient.NewRequestServer(channel, handlers)
					_ = server.Serve()
					_ = server.Close()
					return
				}
			}
		}()
	}
}

func TestSFTPAuthenticationAndInventory(t *testing.T) {
	cred, connections := startSFTP(t)
	for _, method := range []string{"password", "privateKey"} {
		t.Run(method, func(t *testing.T) {
			selected := cred
			selected.Secrets = map[string]string{}
			if method == "password" {
				selected.Secrets["password"] = cred.Secrets["password"]
			} else {
				selected.Secrets["privateKey"] = cred.Secrets["privateKey"]
				selected.Secrets["passphrase"] = cred.Secrets["passphrase"]
			}
			before := connections.Load()
			source, _ := connectors.Source("sftp")
			count := 0
			res, err := source.Scan(context.Background(), connectors.ScanRequest{Credential: selected, Config: map[string]any{"rootPath": "/docs"}, Emit: func(item connectors.SourceItem) error {
				count++
				if item.Path != "/docs/报告.txt" || item.Size != 5 {
					t.Errorf("item=%+v", item)
				}
				content, err := source.OpenContent(context.Background(), connectors.ContentRequest{Credential: selected, Item: item})
				if err != nil {
					return err
				}
				defer content.Body.Close()
				body, err := io.ReadAll(content.Body)
				if string(body) != "hello" {
					t.Errorf("body=%q", body)
				}
				return err
			}})
			if err != nil || !res.Complete || count != 1 {
				t.Fatalf("res=%+v err=%v count=%d", res, err, count)
			}
			if connections.Load()-before != 1 {
				t.Fatalf("scan opened %d connections", connections.Load()-before)
			}
		})
	}
}

func TestSFTPRejectsWrongHostAndAuthentication(t *testing.T) {
	cred, _ := startSFTP(t)
	source, _ := connectors.Source("sftp")
	wrongHost := cred
	wrongHost.Config = map[string]any{}
	for key, value := range cred.Config {
		wrongHost.Config[key] = value
	}
	other, _ := newSigner(t)
	wrongHost.Config["hostKey"] = ssh.FingerprintSHA256(other.PublicKey())
	if _, err := source.ValidateCredential(context.Background(), wrongHost); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("host error=%v", err)
	}
	cred.Secrets = map[string]string{"password": "wrong"}
	if _, err := source.ValidateCredential(context.Background(), cred); err == nil {
		t.Fatal("invalid password accepted")
	}
}

func TestSFTPCancellationAndMissingFile(t *testing.T) {
	cred, _ := startSFTP(t)
	ctx, cancel := context.WithCancel(context.Background())
	connection, err := dial(ctx, cred)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Open(ctx, "/missing.txt"); err == nil || errors.Is(err, connectors.ErrSkip) {
		t.Fatalf("missing file error=%v", err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := connection.List(ctx, "/docs"); err != nil {
			return
		}
	}
	t.Fatal("cancel did not close SSH connection")
}

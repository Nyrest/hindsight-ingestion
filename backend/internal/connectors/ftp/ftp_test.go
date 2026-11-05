package ftp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

type ftpServer struct {
	tls         *tls.Config
	mlsd        bool
	malformed   bool
	denied      bool
	connections atomic.Int32
	transfers   atomic.Int32
	commands    chan string
}

func testCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certificate, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	return pair, string(certificate)
}

func startFTP(t *testing.T, security string, mlsd bool) (*ftpServer, connectors.Credential) {
	t.Helper()
	pair, ca := testCertificate(t)
	server := &ftpServer{tls: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, mlsd: mlsd, commands: make(chan string, 100)}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.connections.Add(1)
			go server.serve(conn, security)
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	return server, connectors.Credential{ID: "ftp-test", Config: map[string]any{"host": host, "port": port, "security": security, "username": "alice", "caCert": ca}, Secrets: map[string]string{"password": "secret"}, Proxy: proxy.Config{Type: "none"}}
}

func (s *ftpServer) serve(conn net.Conn, security string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if security == "implicit" {
		secured := tls.Server(conn, s.tls)
		if secured.Handshake() != nil {
			return
		}
		conn = secured
	}
	writer := bufio.NewWriter(conn)
	reader := bufio.NewReader(conn)
	reply := func(line string) { _, _ = writer.WriteString(line + "\r\n"); _ = writer.Flush() }
	reply("220 test FTP ready")
	var passive net.Listener
	defer func() {
		if passive != nil {
			_ = passive.Close()
		}
	}()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		command, arg, _ := strings.Cut(line, " ")
		if command != "PASS" {
			s.commands <- line
		}
		switch command {
		case "AUTH":
			reply("234 Proceed with TLS")
			secured := tls.Server(conn, s.tls)
			if secured.Handshake() != nil {
				return
			}
			conn = secured
			writer = bufio.NewWriter(conn)
			reader = bufio.NewReader(conn)
		case "USER":
			reply("331 Password required")
		case "PASS":
			if arg == "secret" {
				reply("230 Logged in")
			} else {
				reply("530 Wrong password")
			}
		case "FEAT":
			if s.mlsd {
				reply("211-Features\r\n MLST type*;size*;modify*;\r\n MDTM\r\n UTF8\r\n211 End")
			} else {
				reply("211-Features\r\n MDTM\r\n211 End")
			}
		case "CWD":
			if s.denied {
				reply("550 Permission denied")
			} else {
				reply("250 Directory changed")
			}
		case "TYPE", "OPTS", "PBSZ", "PROT":
			reply("200 OK")
		case "EPSV", "PASV":
			if passive != nil {
				_ = passive.Close()
			}
			passive, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return
			}
			port := passive.Addr().(*net.TCPAddr).Port
			if command == "EPSV" {
				reply(fmt.Sprintf("229 Entering Extended Passive Mode (|||%d|)", port))
			} else {
				reply(fmt.Sprintf("227 Entering Passive Mode (127,0,0,1,%d,%d)", port/256, port%256))
			}
		case "MLSD", "LIST", "RETR":
			if s.denied {
				reply("550 Permission denied")
				continue
			}
			reply("150 Opening data connection")
			data, err := passive.Accept()
			if err != nil {
				return
			}
			_ = data.SetDeadline(time.Now().Add(5 * time.Second))
			if security != "none" {
				secured := tls.Server(data, s.tls)
				if secured.Handshake() != nil {
					_ = data.Close()
					return
				}
				data = secured
			}
			payload := ""
			if command == "RETR" {
				payload = "hello"
			} else if s.malformed {
				payload = "invalid directory record\r\n"
			} else if s.mlsd {
				payload = "type=file;size=5;modify=20261101000000; 报告.txt\r\n"
			} else {
				payload = "-rw-r--r-- 1 alice users 5 Nov 01 00:00 report.txt\r\n"
			}
			_, _ = io.WriteString(data, payload)
			_ = data.Close()
			s.transfers.Add(1)
			reply("226 Transfer complete")
		case "MDTM":
			reply("213 20261101000000")
		case "QUIT":
			reply("221 Bye")
			return
		default:
			reply("502 Unsupported")
		}
	}
}

func TestFTPAndFTPSProtocol(t *testing.T) {
	for _, security := range []string{"none", "explicit", "implicit"} {
		for _, mlsd := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/MLSD=%v", security, mlsd), func(t *testing.T) {
				server, cred := startFTP(t, security, mlsd)
				source, _ := connectors.Source("ftp")
				count := 0
				res, err := source.Scan(context.Background(), connectors.ScanRequest{Credential: cred, Emit: func(item connectors.SourceItem) error {
					count++
					if item.Revision == "" || item.Size != 5 {
						t.Errorf("item=%+v", item)
					}
					content, err := source.OpenContent(context.Background(), connectors.ContentRequest{Credential: cred, Item: item})
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
					t.Fatalf("scan=%+v err=%v count=%d", res, err, count)
				}
				if server.connections.Load() != 1 || server.transfers.Load() != 2 {
					t.Fatalf("connections=%d transfers=%d", server.connections.Load(), server.transfers.Load())
				}
			})
		}
	}
}

func TestFTPListingFailures(t *testing.T) {
	for _, malformed := range []bool{true, false} {
		t.Run(strconv.FormatBool(malformed), func(t *testing.T) {
			server, cred := startFTP(t, "none", true)
			server.malformed = malformed
			server.denied = !malformed
			source, _ := connectors.Source("ftp")
			res, err := source.Scan(context.Background(), connectors.ScanRequest{Credential: cred, Emit: func(connectors.SourceItem) error { t.Error("must not emit failed listing"); return nil }})
			if err == nil || res.Complete {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}
}

func TestFTPSRejectsUntrustedCertificate(t *testing.T) {
	_, cred := startFTP(t, "explicit", true)
	delete(cred.Config, "caCert")
	source, _ := connectors.Source("ftp")
	if _, err := source.ValidateCredential(context.Background(), cred); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestFTPProxyIncludesDataConnections(t *testing.T) {
	_, cred := startFTP(t, "explicit", true)
	var tunnels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" {
			w.WriteHeader(405)
			return
		}
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
	defer server.Close()
	cred.Proxy = proxy.Config{Type: "http", Address: strings.TrimPrefix(server.URL, "http://")}
	source, _ := connectors.Source("ftp")
	if _, err := source.ValidateCredential(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	if tunnels.Load() != 2 {
		t.Fatalf("tunnels=%d, expected control + data", tunnels.Load())
	}
}

package credentials

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/ftp"
	_ "github.com/Nyrest/hindsight-ingestion/internal/connectors/sftp"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"golang.org/x/crypto/ssh"
)

func TestSFTPPrivateKeyEncryptedAndPreserved(t *testing.T) {
	service := newService(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "credential test")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	private := strings.TrimSpace(string(pem.EncodeToMemory(block)))
	config := map[string]any{"host": "example.com", "username": "alice", "hostKey": ssh.FingerprintSHA256(signer.PublicKey()), "privateKey": private}
	stored := models.Credential{ID: "sftp-credential"}
	if err := service.Apply(&stored, Input{Name: ptr("SFTP"), Type: "sftp", Config: config}, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.ConfigJSON, "PRIVATE KEY") || strings.Contains(stored.EncryptedSecret, private) {
		t.Fatal("private key was stored in plaintext")
	}
	view, err := service.MaskedView(&stored)
	if err != nil {
		t.Fatal(err)
	}
	if view.Config["privateKey"] != Mask {
		t.Fatalf("private key is not masked")
	}
	config["privateKey"] = Mask
	if err := service.Apply(&stored, Input{Name: ptr("updated"), Config: config}, false); err != nil {
		t.Fatal(err)
	}
	decrypted, err := service.Decrypt(&stored)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted.String("privateKey") != private {
		t.Fatal("masked update changed the stored private key")
	}
}

func TestRemoteCredentialsRejectHTTPHeaders(t *testing.T) {
	service := newService(t)
	for _, protocol := range []string{"ftp", "sftp"} {
		t.Run(protocol, func(t *testing.T) {
			config := map[string]any{"host": "example.com", "username": "alice", "password": "secret"}
			if protocol == "sftp" {
				config["hostKey"] = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			}
			err := service.Apply(&models.Credential{ID: protocol}, Input{Name: ptr(protocol), Type: protocol, Config: config, HeadersSet: true, CustomHeaders: map[string]string{"X-Test": "unused"}}, true)
			if err == nil || !strings.Contains(err.Error(), "HTTP headers") {
				t.Fatalf("headers error=%v", err)
			}
		})
	}
}

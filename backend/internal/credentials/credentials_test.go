package credentials

import (
	"bytes"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/crypto"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: "testcred",
		Fields: []connectors.FieldSpec{
			{Key: "baseUrl", Label: "Base URL", Type: connectors.FieldURL, Required: true},
			{Key: "token", Label: "Token", Type: connectors.FieldPassword, Required: true},
			{Key: "region", Label: "Region", Type: connectors.FieldString},
		},
	})
}

func newService(t *testing.T) *Service {
	t.Helper()
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "c.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s, _ := db.DB(); s.Close() })
	c, _ := crypto.New(bytes.Repeat([]byte{4}, 32))
	return NewService(db, c)
}

func ptr(s string) *string { return &s }

func TestSecretsEncryptedAndMasked(t *testing.T) {
	s := newService(t)
	m := models.Credential{ID: "c1"}
	err := s.Apply(&m, Input{Name: ptr("x"), Type: "testcred", HeadersSet: true,
		Config:        map[string]any{"baseUrl": "https://a.example/", "token": "tok-123", "region": "eu"},
		CustomHeaders: map[string]string{"X-Api-Key": "hdr-secret"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.ConfigJSON, "tok-123") || strings.Contains(m.EncryptedSecret, "tok-123") ||
		strings.Contains(m.HeaderNamesJSON, "hdr-secret") {
		t.Fatal("secret stored in plaintext")
	}
	if !strings.Contains(m.ConfigJSON, "https://a.example") {
		t.Fatal("non-secret config should stay plaintext")
	}
	view, err := s.MaskedView(&m)
	if err != nil {
		t.Fatal(err)
	}
	if view.Config["token"] != Mask || view.CustomHeaders["X-Api-Key"] != Mask {
		t.Fatalf("not masked: %v %v", view.Config, view.CustomHeaders)
	}

	// Masked round trip keeps secrets; rotation replaces them.
	if err := s.Apply(&m, Input{Config: map[string]any{"baseUrl": "https://a.example", "token": Mask},
		HeadersSet: true, CustomHeaders: map[string]string{"X-Api-Key": Mask, "X-Other": "v"}}, false); err != nil {
		t.Fatal(err)
	}
	cred, _ := s.Decrypt(&m)
	if cred.String("token") != "tok-123" || cred.Headers["X-Api-Key"] != "hdr-secret" || cred.Headers["X-Other"] != "v" {
		t.Fatalf("round trip lost data: %v %v", cred.Secrets, cred.Headers)
	}
	if cred.String("region") != "eu" {
		t.Fatal("omitting a field in a partial update should keep it")
	}
	if err := s.Apply(&m, Input{Config: map[string]any{"region": ""}}, false); err != nil {
		t.Fatal(err)
	}
	if cred, _ = s.Decrypt(&m); cred.String("region") != "" || cred.String("token") != "tok-123" {
		t.Fatal("empty string should clear a field without touching others")
	}
	if err := s.Apply(&m, Input{Config: map[string]any{"baseUrl": "https://a.example", "token": "new"}}, false); err != nil {
		t.Fatal(err)
	}
	cred, _ = s.Decrypt(&m)
	if cred.String("token") != "new" || cred.Headers["X-Api-Key"] != "hdr-secret" {
		t.Fatal("rotation failed or headers lost")
	}
}

func TestHeaderValidation(t *testing.T) {
	s := newService(t)
	m := models.Credential{ID: "c2"}
	err := s.Apply(&m, Input{Name: ptr("x"), Type: "testcred", HeadersSet: true,
		Config:        map[string]any{"baseUrl": "https://a", "token": "t"},
		CustomHeaders: map[string]string{"X-Key": "a", "x-key": "b"}}, true)
	var ve *ValidationError
	if err == nil || !errorsAs(err, &ve) || ve.Fields["customHeaders"] == "" {
		t.Fatalf("expected duplicate header error, got %v", err)
	}
	err = s.Apply(&m, Input{Name: ptr("x"), Type: "testcred", HeadersSet: true,
		Config:        map[string]any{"baseUrl": "https://a", "token": "t"},
		CustomHeaders: map[string]string{"Bad Header": "a"}}, true)
	if err == nil {
		t.Fatal("expected invalid header name error")
	}
	err = s.Apply(&m, Input{Name: ptr("x"), Type: "testcred", Config: map[string]any{"baseUrl": "https://a"}}, true)
	if err == nil {
		t.Fatal("expected required token error")
	}
}

func errorsAs(err error, ve **ValidationError) bool {
	v, ok := err.(*ValidationError)
	if ok {
		*ve = v
	}
	return ok
}

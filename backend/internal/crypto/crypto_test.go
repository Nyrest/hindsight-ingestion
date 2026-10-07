package crypto

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	c, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := c.Encrypt([]byte("super-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "super-secret") {
		t.Fatal("ciphertext leaks plaintext")
	}
	enc2, _ := c.Encrypt([]byte("super-secret"))
	if enc == enc2 {
		t.Fatal("nonce reuse: identical ciphertexts")
	}
	dec, err := c.Decrypt(enc)
	if err != nil || string(dec) != "super-secret" {
		t.Fatalf("decrypt = %q, %v", dec, err)
	}
}

func TestWrongKeyFails(t *testing.T) {
	a, _ := New(bytes.Repeat([]byte{1}, 32))
	b, _ := New(bytes.Repeat([]byte{2}, 32))
	enc, _ := a.Encrypt([]byte("x"))
	if _, err := b.Decrypt(enc); err == nil {
		t.Fatal("expected authentication failure")
	}
}

func TestJSON(t *testing.T) {
	c, _ := New(bytes.Repeat([]byte{3}, 32))
	enc, err := c.EncryptJSON(map[string]string{"token": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := c.DecryptJSON(enc, &out); err != nil || out["token"] != "abc" {
		t.Fatalf("got %v, %v", out, err)
	}
}

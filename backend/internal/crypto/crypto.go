// Package crypto encrypts credential secrets at rest with AES-256-GCM.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

const version = "v1:"

// Cipher seals and opens secret payloads.
type Cipher struct {
	aead cipher.AEAD
}

// New creates a Cipher from a 32-byte key.
func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns a versioned base64 string of nonce||ciphertext.
func (c *Cipher) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, plaintext, nil)
	return version + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. An empty input decrypts to nil.
func (c *Cipher) Decrypt(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) < len(version) || encoded[:len(version)] != version {
		return nil, errors.New("unsupported secret encoding")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded[len(version):])
	if err != nil {
		return nil, fmt.Errorf("decode secret: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("secret ciphertext too short")
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return nil, errors.New("decrypt secret: authentication failed (wrong CREDENTIAL_ENCRYPTION_KEY?)")
	}
	return plain, nil
}

// EncryptJSON marshals v and encrypts it.
func (c *Cipher) EncryptJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return c.Encrypt(b)
}

// DecryptJSON decrypts into v. An empty input leaves v untouched.
func (c *Cipher) DecryptJSON(encoded string, v any) error {
	b, err := c.Decrypt(encoded)
	if err != nil || b == nil {
		return err
	}
	return json.Unmarshal(b, v)
}

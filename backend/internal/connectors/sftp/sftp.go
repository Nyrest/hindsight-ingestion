// Package sftp implements SSH file inventories with host identity verification.
package sftp

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/remotefs"
	sftpclient "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: "sftp", Name: "SFTP", Description: "Remote files over SSH with password or private-key authentication", Validate: validateCredential,
		Fields: []connectors.FieldSpec{
			{Key: "host", Label: "Host", Type: connectors.FieldString, Required: true},
			{Key: "port", Label: "Port", Type: connectors.FieldNumber, Placeholder: "22"},
			{Key: "username", Label: "Username", Type: connectors.FieldString, Required: true},
			{Key: "password", Label: "Password", Type: connectors.FieldPassword, Secret: true},
			{Key: "privateKey", Label: "SSH private key", Type: connectors.FieldTextarea, Secret: true},
			{Key: "passphrase", Label: "Private key passphrase", Type: connectors.FieldPassword, Secret: true},
			{Key: "hostKey", Label: "SSH host key fingerprint", Type: connectors.FieldString, Required: true, Placeholder: "SHA256:…", Help: "SHA256 fingerprint supplied by your server administrator. Connections reject a different host key."},
		},
	})
	connectors.RegisterSource(&remotefs.Source{Type: "sftp", Name: "SFTP", Dial: dial})
}

func authentication(cred connectors.Credential) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if key := cred.String("privateKey"); key != "" {
		var signer ssh.Signer
		var err error
		if passphrase := cred.String("passphrase"); passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(key))
		}
		if err != nil {
			return nil, &connectors.FieldError{Field: "privateKey", Message: "Invalid SSH private key or passphrase"}
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if password := cred.String("password"); password != "" {
		methods = append(methods, ssh.Password(password))
	}
	if len(methods) == 0 {
		return nil, &connectors.FieldError{Field: "password", Message: "SFTP requires a password or SSH private key"}
	}
	return methods, nil
}

func validateCredential(cred connectors.Credential) error {
	_, err := clientConfiguration(cred)
	return err
}

func clientConfiguration(cred connectors.Credential) (*ssh.ClientConfig, error) {
	if _, err := remotefs.Endpoint(cred, 22); err != nil {
		return nil, err
	}
	if cred.String("username") == "" {
		return nil, &connectors.FieldError{Field: "username", Message: "SFTP username is required"}
	}
	fingerprint := cred.String("hostKey")
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(fingerprint, "SHA256:"))
	if !strings.HasPrefix(fingerprint, "SHA256:") || err != nil || len(decoded) != 32 {
		return nil, &connectors.FieldError{Field: "hostKey", Message: "SSH host key must be a SHA256 fingerprint"}
	}
	methods, err := authentication(cred)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{User: cred.String("username"), Auth: methods, HostKeyCallback: verifyHostKey(fingerprint)}, nil
}

func verifyHostKey(expected string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if subtle.ConstantTimeCompare([]byte(ssh.FingerprintSHA256(key)), []byte(expected)) != 1 {
			return errors.New("SSH host key fingerprint mismatch")
		}
		return nil
	}
}

type client struct {
	sftp *sftpclient.Client
	ssh  *ssh.Client
	raw  net.Conn
}

func dial(ctx context.Context, cred connectors.Credential) (remotefs.Client, error) {
	config, err := clientConfiguration(cred)
	if err != nil {
		return nil, err
	}
	address, _ := remotefs.Endpoint(cred, 22)
	raw, err := cred.Proxy.DialContext(ctx, address, 10*time.Second)
	if err != nil {
		return nil, err
	}
	watched := remotefs.Watch(ctx, raw)
	if err := watched.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		_ = watched.Close()
		return nil, err
	}
	conn, channels, requests, err := ssh.NewClientConn(watched, address, config)
	if err != nil {
		_ = watched.Close()
		return nil, err
	}
	sshClient := ssh.NewClient(conn, channels, requests)
	files, err := sftpclient.NewClient(sshClient, sftpclient.UseConcurrentReads(false))
	if err != nil {
		_ = sshClient.Close()
		_ = watched.Close()
		return nil, err
	}
	if err := watched.SetDeadline(time.Time{}); err != nil {
		_ = files.Close()
		_ = sshClient.Close()
		_ = watched.Close()
		return nil, err
	}
	return &client{sftp: files, ssh: sshClient, raw: watched}, nil
}

func (c *client) Close() error { return errors.Join(c.sftp.Close(), c.ssh.Close(), c.raw.Close()) }
func (c *client) List(ctx context.Context, p string) ([]remotefs.Entry, error) {
	finish := c.operation(ctx)
	defer finish()
	entries, err := c.sftp.ReadDirContext(ctx, p)
	if err != nil {
		return nil, err
	}
	listed := make([]remotefs.Entry, 0, len(entries))
	for _, entry := range entries {
		listed = append(listed, remotefs.Entry{Name: entry.Name(), Directory: entry.IsDir(), Regular: entry.Mode().IsRegular(), Size: entry.Size(), Modified: entry.ModTime()})
	}
	return listed, nil
}
func (c *client) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	finish := c.operation(ctx)
	defer finish()
	file, err := c.sftp.Open(p)
	if err != nil {
		return nil, err
	}
	return &fileBody{ReadCloser: file, client: c, ctx: ctx}, nil
}

func (c *client) operation(ctx context.Context) func() {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(bounded, func() { _ = c.raw.Close() })
	return func() { stop(); cancel() }
}

type fileBody struct {
	io.ReadCloser
	client *client
	ctx    context.Context
}

func (f *fileBody) Read(p []byte) (int, error) {
	finish := f.client.operation(f.ctx)
	defer finish()
	return f.ReadCloser.Read(p)
}

func (f *fileBody) Close() error {
	finish := f.client.operation(f.ctx)
	defer finish()
	return f.ReadCloser.Close()
}

// Package ftp implements FTP and explicit/implicit FTPS file sources.
package ftp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"path"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/remotefs"
	ftpclient "github.com/jlaffaye/ftp"
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: "ftp", Name: "FTP / FTPS", Description: "Remote files over FTP or TLS-encrypted FTPS", Validate: validateCredential,
		Fields: []connectors.FieldSpec{
			{Key: "host", Label: "Host", Type: connectors.FieldString, Required: true},
			{Key: "port", Label: "Port", Type: connectors.FieldNumber, Help: "Default: 21 (FTP / explicit FTPS), 990 (implicit FTPS)."},
			{Key: "security", Label: "Connection security", Type: connectors.FieldSelect, Default: "explicit", Required: true, Options: []connectors.Option{{Value: "explicit", Label: "Explicit TLS"}, {Value: "implicit", Label: "Implicit TLS"}, {Value: "none", Label: "None (plain FTP)"}}},
			{Key: "username", Label: "Username", Type: connectors.FieldString, Required: true},
			{Key: "password", Label: "Password", Type: connectors.FieldPassword, Secret: true},
			{Key: "caCert", Label: "TLS CA certificate (PEM)", Type: connectors.FieldTextarea, Help: "Optional CA certificate for a private FTPS server. Server certificates and hostnames are always verified."},
		},
	})
	connectors.RegisterSource(&remotefs.Source{Type: "ftp", Name: "FTP / FTPS", Dial: dial})
}

func endpoint(cred connectors.Credential) (string, error) {
	port := 21
	if cred.String("security") == "implicit" {
		port = 990
	}
	return remotefs.Endpoint(cred, port)
}

func validateCredential(cred connectors.Credential) error {
	if _, err := endpoint(cred); err != nil {
		return err
	}
	switch cred.String("security") {
	case "", "none", "explicit", "implicit":
	default:
		return &connectors.FieldError{Field: "security", Message: "Invalid FTP connection security"}
	}
	if cred.String("username") == "" {
		return &connectors.FieldError{Field: "username", Message: "FTP username is required"}
	}
	_, err := tlsConfiguration(cred)
	return err
}

func tlsConfiguration(cred connectors.Credential) (*tls.Config, error) {
	address, err := endpoint(cred)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(address)
	config := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if certificate := cred.String("caCert"); certificate != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM([]byte(certificate)) {
			return nil, &connectors.FieldError{Field: "caCert", Message: "Invalid TLS CA certificate"}
		}
		config.RootCAs = pool
	}
	return config, nil
}

type client struct {
	conn    *ftpclient.ServerConn
	listing *listingCount
}

func dial(ctx context.Context, cred connectors.Credential) (remotefs.Client, error) {
	if err := validateCredential(cred); err != nil {
		return nil, err
	}
	address, _ := endpoint(cred)
	host, _, _ := net.SplitHostPort(address)
	security := cred.String("security")
	if security == "" {
		security = "explicit"
	}
	tlsConfig, _ := tlsConfiguration(cred)
	listing := &listingCount{}
	transport := &transport{ctx: ctx, credential: cred, host: host, security: security, tls: tlsConfig, listing: listing}
	options := []ftpclient.DialOption{ftpclient.DialWithDialFunc(transport.dial), ftpclient.DialWithShutTimeout(5 * time.Second)}
	if security == "explicit" {
		options = append(options, ftpclient.DialWithExplicitTLS(tlsConfig))
	}
	if security == "implicit" {
		options = append(options, ftpclient.DialWithTLS(tlsConfig))
	}
	conn, err := ftpclient.Dial(address, options...)
	if err != nil {
		return nil, err
	}
	if err = conn.Login(cred.String("username"), cred.String("password")); err != nil {
		_ = conn.Quit()
		return nil, err
	}
	return &client{conn: conn, listing: listing}, nil
}

func (c *client) Close() error { return c.conn.Quit() }
func (c *client) List(ctx context.Context, p string) ([]remotefs.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// LIST can also succeed for a single file; inventories require a directory.
	if err := c.conn.ChangeDir(p); err != nil {
		return nil, err
	}
	c.listing.active = true
	c.listing.lines = 0
	entries, err := c.conn.List(p)
	c.listing.active = false
	if err != nil {
		return nil, err
	}
	if c.listing.lines != len(entries) {
		return nil, errors.New("FTP directory listing contains unparseable entries")
	}
	listed := make([]remotefs.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Size > uint64(^uint64(0)>>1) {
			return nil, errors.New("remote file size exceeds int64")
		}
		modified := entry.Time
		if entry.Type == ftpclient.EntryTypeFile && !c.conn.IsTimePreciseInList() {
			// LIST timestamps can lack a year or seconds; MDTM is authoritative when available.
			modified = time.Time{}
			if c.conn.IsGetTimeSupported() {
				modified, err = c.conn.GetTime(path.Join(p, entry.Name))
				if err != nil {
					return nil, err
				}
			}
		}
		listed = append(listed, remotefs.Entry{Name: entry.Name, Directory: entry.Type == ftpclient.EntryTypeFolder, Regular: entry.Type == ftpclient.EntryTypeFile, Size: int64(entry.Size), Modified: modified})
	}
	return listed, nil
}
func (c *client) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.conn.Retr(p)
}

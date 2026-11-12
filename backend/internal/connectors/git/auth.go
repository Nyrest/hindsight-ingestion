package git

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"golang.org/x/crypto/ssh"
)

const internalURL = "ingestiongit://source/repository"

func init() { gitclient.InstallProtocol("ingestiongit", sourceTransport{}) }

type access struct {
	ctx        context.Context
	endpoint   *transport.Endpoint
	credential connectors.Credential
}

func (*access) Name() string   { return "ingestion-git" }
func (*access) String() string { return "Git source authentication" }

func newAccess(ctx context.Context, cred connectors.Credential, repository string) (*access, error) {
	endpoint, err := parseURL(repository)
	if err != nil {
		return nil, err
	}
	if err := validateCredential(cred); err != nil {
		return nil, err
	}
	if endpoint.Protocol == "ssh" && cred.String("privateKey") == "" {
		return nil, invalid("sourceCredentialId", "SSH repositories require a Git credential with a private key and host fingerprint")
	}
	return &access{ctx: ctx, endpoint: endpoint, credential: cred}, nil
}

func validateCredential(cred connectors.Credential) error {
	if cred.String("privateKey") != "" {
		if _, err := sshSigner(cred); err != nil {
			return err
		}
		if err := validateHostKey(cred.String("hostKey")); err != nil {
			return err
		}
	} else if cred.String("hostKey") != "" {
		if err := validateHostKey(cred.String("hostKey")); err != nil {
			return err
		}
	}
	if certificate := cred.String("caCert"); certificate != "" {
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(certificate)) {
			return invalid("caCert", "Invalid TLS CA certificate")
		}
	}
	return nil
}

func sshSigner(cred connectors.Credential) (ssh.Signer, error) {
	var signer ssh.Signer
	var err error
	if password := cred.String("passphrase"); password != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cred.String("privateKey")), []byte(password))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(cred.String("privateKey")))
	}
	if err != nil {
		return nil, invalid("privateKey", "Invalid SSH private key or passphrase")
	}
	return signer, nil
}

func validateHostKey(fingerprint string) error {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(fingerprint, "SHA256:"))
	if !strings.HasPrefix(fingerprint, "SHA256:") || err != nil || len(decoded) != 32 {
		return invalid("hostKey", "SSH host key must be a SHA256 fingerprint")
	}
	return nil
}

func (a *access) sshConfig() (*ssh.ClientConfig, error) {
	signer, err := sshSigner(a.credential)
	if err != nil {
		return nil, err
	}
	user := a.credential.String("username")
	if user == "" {
		user = a.endpoint.User
	}
	if user == "" {
		user = "git"
	}
	return &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if subtle.ConstantTimeCompare([]byte(ssh.FingerprintSHA256(key)), []byte(a.credential.String("hostKey"))) != 1 {
			return errors.New("SSH host key fingerprint mismatch")
		}
		return nil
	}}, nil
}

type sourceTransport struct{}

func (sourceTransport) NewUploadPackSession(_ *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	access, ok := auth.(*access)
	if !ok {
		return nil, transport.ErrInvalidAuthMethod
	}
	if access.endpoint.Protocol == "ssh" {
		return newSSHSession(access)
	}
	endpoint := *access.endpoint
	upstream := httpx.New(nil, nil, access.credential.Proxy).HTTP
	upstream.Timeout = 0
	var ownedTransport *http.Transport
	if certificate := access.credential.String("caCert"); certificate != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		pool.AppendCertsFromPEM([]byte(certificate))
		base := httpx.Transport(access.credential.Proxy).Clone()
		base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
		upstream.Transport = base
		ownedTransport = base
	}
	origin, err := url.Parse(endpoint.String())
	if err != nil {
		return nil, invalid("repositoryUrl", "Enter a valid Git HTTP(S) URL")
	}
	upstream.Transport = &headerTransport{RoundTripper: upstream.Transport, origin: origin, headers: access.credential.Headers}
	var basic transport.AuthMethod
	if password := access.credential.String("password"); password != "" {
		user := access.credential.String("username")
		if user == "" {
			user = "git"
		}
		basic = &githttp.BasicAuth{Username: user, Password: password}
	}
	session, err := githttp.NewClient(upstream).NewUploadPackSession(&endpoint, basic)
	if err != nil {
		if ownedTransport != nil {
			ownedTransport.CloseIdleConnections()
		}
		return nil, err
	}
	return &httpUploadSession{UploadPackSession: session, owned: ownedTransport}, nil
}

type httpUploadSession struct {
	transport.UploadPackSession
	owned *http.Transport
}

func (s *httpUploadSession) Close() error {
	err := s.UploadPackSession.Close()
	if s.owned != nil {
		s.owned.CloseIdleConnections()
	}
	return err
}
func (sourceTransport) NewReceivePackSession(*transport.Endpoint, transport.AuthMethod) (transport.ReceivePackSession, error) {
	return nil, errors.New("Git sources only read repositories")
}

type headerTransport struct {
	http.RoundTripper
	origin  *url.URL
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if strings.EqualFold(req.URL.Host, t.origin.Host) && req.URL.Scheme == t.origin.Scheme {
		for key, value := range t.headers {
			if cloned.Header.Get(key) == "" {
				cloned.Header.Set(key, value)
			}
		}
	}
	return t.RoundTripper.RoundTrip(cloned)
}

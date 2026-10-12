package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-sasl"
	"golang.org/x/sync/semaphore"
	"gorm.io/gorm"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

type Connector struct {
	DB       *gorm.DB
	mu       sync.Mutex
	accounts map[string]*accountSlot
	sessions sync.Map
}
type accountSlot struct {
	sem   *semaphore.Weighted
	users int
}
type session struct {
	client  *imapclient.Client
	conn    net.Conn
	stop    func() bool
	timeout time.Duration
}

func (c *Connector) reserve(ctx context.Context, cred connectors.Credential, count int64) (func(), error) {
	key := AccountKey(cred)
	c.mu.Lock()
	if c.accounts == nil {
		c.accounts = map[string]*accountSlot{}
	}
	slot := c.accounts[key]
	if slot == nil {
		slot = &accountSlot{sem: semaphore.NewWeighted(3)}
		c.accounts[key] = slot
	}
	slot.users++
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		slot.users--
		if slot.users == 0 {
			delete(c.accounts, key)
		}
		c.mu.Unlock()
	}
	if err := slot.sem.Acquire(ctx, count); err != nil {
		forget()
		return nil, err
	}
	return func() { slot.sem.Release(count); forget() }, nil
}

func openSession(ctx context.Context, cred connectors.Credential) (*session, error) {
	if err := ValidateCredentialConfig(cred); err != nil {
		return nil, err
	}
	dialTimeout, _ := connectors.AsFloat(cred.Config["connectionTimeout"])
	cmdTimeout, _ := connectors.AsFloat(cred.Config["commandTimeout"])
	conn, err := cred.Proxy.DialContext(ctx, net.JoinHostPort(cred.String("host"), cred.String("port")), time.Duration(dialTimeout)*time.Second)
	if err != nil {
		return nil, err
	}
	s := &session{conn: conn, timeout: time.Duration(cmdTimeout) * time.Second}
	s.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	failed := true
	defer func() {
		if failed {
			s.close()
		}
	}()
	options := &imapclient.Options{TLSConfig: &tls.Config{ServerName: cred.String("host"), MinVersion: tls.VersionTLS12}, WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}}
	_, err = command(s, func() (struct{}, error) {
		if cred.String("security") == "tls" {
			secured := tls.Client(conn, options.TLSConfig)
			if err := secured.HandshakeContext(ctx); err != nil {
				return struct{}{}, err
			}
			s.client = imapclient.New(secured, options)
		} else {
			var err error
			s.client, err = imapclient.NewStartTLS(conn, options)
			if err != nil {
				return struct{}{}, err
			}
		}
		if err := s.client.WaitGreeting(); err != nil {
			return struct{}{}, err
		}
		if s.client.Caps().Has(imap.AuthCap("PLAIN")) {
			return struct{}{}, s.client.Authenticate(sasl.NewPlainClient("", cred.String("username"), cred.String("password")))
		}
		return struct{}{}, s.client.Login(cred.String("username"), cred.String("password")).Wait()
	})
	if err != nil {
		return nil, err
	}
	failed = false
	return s, nil
}

func command[T any](s *session, wait func() (T, error)) (T, error) {
	timer := time.AfterFunc(s.timeout, func() { _ = s.conn.Close() })
	defer timer.Stop()
	return wait()
}

func (s *session) close() { s.stop(); _ = s.conn.Close() }

func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	release, err := c.reserve(ctx, cred, 1)
	if err != nil {
		return "", err
	}
	defer release()
	s, err := openSession(ctx, cred)
	if err != nil {
		return "", redactError(err, cred)
	}
	defer s.close()
	if _, err = mailboxes(s); err != nil {
		return "", redactError(err, cred)
	}
	return "Connected to IMAP", nil
}

func redactError(err error, cred connectors.Credential) error {
	if err == nil {
		return nil
	}
	message := cred.Proxy.Redact(err.Error())
	for _, secret := range cred.Secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return errors.New(fmt.Sprintf("IMAP: %s", message))
}

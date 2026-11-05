package ftp

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors/remotefs"
)

type transport struct {
	ctx        context.Context
	credential connectors.Credential
	host       string
	security   string
	tls        *tls.Config
	listing    *listingCount
	started    bool
}

func (t *transport) dial(_ string, target string) (net.Conn, error) {
	control := !t.started
	t.started = true
	_, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	// Passive connections use the configured server, including through TCP proxies.
	raw, err := t.credential.Proxy.DialContext(t.ctx, net.JoinHostPort(t.host, port), 10*time.Second)
	if err != nil {
		return nil, err
	}
	conn := &remotefs.TimedConn{Conn: remotefs.Watch(t.ctx, raw)}
	if control {
		return t.control(conn)
	}
	return t.data(conn), nil
}

func (t *transport) control(conn net.Conn) (net.Conn, error) {
	if t.security != "implicit" {
		return conn, nil
	}
	secured := tls.Client(conn, t.tls)
	if err := secured.HandshakeContext(t.ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return secured, nil
}

func (t *transport) data(conn net.Conn) net.Conn {
	// Data TLS starts after RETR/LIST; many servers wait for that command.
	if t.security != "none" {
		conn = tls.Client(conn, t.tls)
	}
	if t.listing.active {
		conn = &listingConn{Conn: conn, count: t.listing}
	}
	return conn
}

package remotefs

import (
	"context"
	"net"
	"sync"
	"time"
)

// Conn closes immediately on cancellation, including during protocol handshakes.
type Conn struct {
	net.Conn
	stop func() bool
	once sync.Once
}

func Watch(ctx context.Context, conn net.Conn) *Conn {
	watched := &Conn{Conn: conn}
	watched.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	return watched
}

type TimedConn struct{ net.Conn }

func (c *TimedConn) Read(p []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}
func (c *TimedConn) Write(p []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}
func (c *Conn) Close() error {
	c.once.Do(func() { c.stop() })
	return c.Conn.Close()
}

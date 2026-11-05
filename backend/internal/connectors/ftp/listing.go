package ftp

import (
	"bytes"
	"net"
	"strconv"
	"strings"
)

// The FTP library silently omits malformed listing lines. Count the plaintext
// records so a parse failure cannot be reported as a complete inventory.
type listingCount struct {
	active bool
	lines  int
}
type listingConn struct {
	net.Conn
	count   *listingCount
	partial []byte
}

func (c *listingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	for _, b := range p[:n] {
		if b == '\n' {
			c.countLine()
			continue
		}
		c.partial = append(c.partial, b)
	}
	if err != nil && len(c.partial) > 0 {
		c.countLine()
	}
	return n, err
}

func (c *listingConn) countLine() {
	line := strings.TrimSpace(string(bytes.TrimSuffix(c.partial, []byte{'\r'})))
	c.partial = c.partial[:0]
	if line == "" {
		return
	}
	if strings.HasPrefix(line, "total ") {
		if _, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "total ")), 10, 64); err == nil {
			return
		}
	}
	c.count.lines++
}

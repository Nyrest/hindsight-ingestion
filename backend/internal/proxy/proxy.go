package proxy

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const Mask = "********"

type Config struct {
	Type     string `json:"type" enum:"default,none,http,https,socks5"`
	Address  string `json:"address"`
	Username string `json:"username"`
	Password string `json:"password"`
}

var Default = Config{Type: "default"}

func (c Config) Normalize() Config {
	if c.Type == "" {
		c.Type = "default"
	}
	c.Address = strings.TrimSpace(c.Address)
	if c.Type == "default" || c.Type == "none" {
		return Config{Type: c.Type}
	}
	return c
}

func (c Config) Validate() error {
	c = c.Normalize()
	switch c.Type {
	case "default", "none":
		return nil
	case "http", "https", "socks5":
	default:
		return errors.New("Select a valid proxy type")
	}
	host, port, err := net.SplitHostPort(c.Address)
	if err != nil || host == "" || strings.ContainsAny(host, "/?#@ \t\r\n") {
		return errors.New("Proxy address must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("Proxy port must be between 1 and 65535")
	}
	return nil
}

func (c Config) Masked() Config {
	if c.Password != "" {
		c.Password = Mask
	}
	return c.Normalize()
}

func (c Config) ProxyFunc() func(*http.Request) (*url.URL, error) {
	c = c.Normalize()
	if c.Type == "default" {
		return http.ProxyFromEnvironment
	}
	if c.Type == "none" {
		return nil
	}
	u := &url.URL{Scheme: c.Type, Host: c.Address}
	if c.Username != "" || c.Password != "" {
		u.User = url.UserPassword(c.Username, c.Password)
	}
	return http.ProxyURL(u)
}

func (c Config) LogValue() slog.Value { return slog.StringValue("proxy:" + c.Normalize().Type) }

// Redact also covers proxy authentication echoed in upstream errors.
func (c Config) Redact(message string) string {
	if c.Username != "" || c.Password != "" {
		message = strings.ReplaceAll(message, base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password)), "[redacted]")
		message = strings.ReplaceAll(message, url.UserPassword(c.Username, c.Password).String(), "[redacted]")
	}
	if c.Password != "" {
		message = strings.ReplaceAll(message, url.QueryEscape(c.Password), "[redacted]")
		message = strings.ReplaceAll(message, c.Password, "[redacted]")
	}
	return message
}

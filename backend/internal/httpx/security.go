package httpx

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

func origin(endpoint *url.URL) string {
	port := endpoint.Port()
	if port == "" {
		port = "80"
		if endpoint.Scheme == "https" {
			port = "443"
		}
	}
	return strings.ToLower(endpoint.Scheme + "://" + endpoint.Hostname() + ":" + port)
}

// CheckRedirect protects configured credentials when following redirects.
func (c *Client) CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("redirect limit exceeded")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return errors.New("invalid redirect scheme")
	}
	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return errors.New("HTTPS downgrade refused")
	}
	for _, previous := range via {
		if origin(req.URL) != origin(previous.URL) {
			c.stripCredentials(req)
			break
		}
	}
	return nil
}

func (c *Client) stripCredentials(req *http.Request) {
	// net/http can restore initial headers on a later hop back to the source.
	for header := range c.CustomHeaders {
		req.Header.Del(header)
	}
	for header := range c.AuthHeaders {
		req.Header.Del(header)
	}
	for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
		req.Header.Del(header)
	}
	req.URL.User = nil
}

var diagnosticURLs = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

func redactURL(address string) string {
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Hostname() == "" {
		return "[redacted URL]"
	}
	// Subscription and download URLs can contain secrets in their paths as well.
	return endpoint.Scheme + "://" + endpoint.Host + "/[redacted]"
}

func (c *Client) redact(message string, sent ...http.Header) string {
	message = diagnosticURLs.ReplaceAllStringFunc(message, redactURL)
	for _, headers := range []map[string]string{c.CustomHeaders, c.AuthHeaders} {
		message = redactHeaderValues(message, headers)
	}
	for _, headers := range sent {
		message = redactAuthentication(message, headers)
	}
	return c.proxy.Redact(message)
}

func redactHeaderValues(message string, headers map[string]string) string {
	for _, secret := range headers {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func redactAuthentication(message string, headers http.Header) string {
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
		for _, secret := range headers.Values(name) {
			if secret == "" {
				continue
			}
			message = strings.ReplaceAll(message, secret, "[redacted]")
			if name != "Cookie" {
				_, token, ok := strings.Cut(secret, " ")
				if ok && token != "" {
					message = strings.ReplaceAll(message, token, "[redacted]")
				}
			}
		}
	}
	return message
}

func (c *Client) safeError(err error, sent ...http.Header) error {
	return &proxyError{cause: err, message: c.redact(err.Error(), sent...)}
}

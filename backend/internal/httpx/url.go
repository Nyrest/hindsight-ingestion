package httpx

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func NormalizeURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return "", fmt.Errorf("Enter an HTTP(S) URL without embedded credentials")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port != "" && !(parsed.Scheme == "https" && port == "443") && !(parsed.Scheme == "http" && port == "80") {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	parsed.Host, parsed.Fragment, parsed.RawFragment = host, "", ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

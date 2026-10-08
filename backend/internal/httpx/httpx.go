// Package httpx provides a shared, connection-reusing HTTP client with
// retry/backoff for rate limits and transient failures.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

// Version is reported in the User-Agent header.
const Version = "0.1.0"

// UserAgent identifies the service to upstream APIs.
var UserAgent = "hindsight-ingestion/" + Version

// Shared is the process-wide transport so connections are reused across
// connectors and runs.
var Shared = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          200,
	MaxIdleConnsPerHost:   32,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   15 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 5 * time.Minute,
}

// Client wraps an http.Client with header injection and retries.
type Client struct {
	HTTP  *http.Client
	proxy proxy.Config
	// CustomHeaders are applied first; AuthHeaders override any conflicting
	// custom header (connector-generated auth headers take precedence).
	CustomHeaders map[string]string
	AuthHeaders   map[string]string
	// MaxRetries for retryable failures (429, 5xx, network errors).
	MaxRetries int
	// Pace, when non-nil, is awaited before every attempt (rate pacing).
	Pace func(ctx context.Context) error
	// Token, when non-nil, supplies a fresh bearer token for every attempt
	// (OAuth access tokens can expire during long runs).
	Token func(ctx context.Context) (string, error)
}

func (c *Client) applyToken(ctx context.Context, req *http.Request) error {
	if c.Token == nil {
		return nil
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// New returns a Client using the shared transport.
func New(custom, auth map[string]string, config proxy.Config) *Client {
	return &Client{
		proxy:         config.Normalize(),
		HTTP:          &http.Client{Transport: Transport(config)},
		CustomHeaders: custom,
		AuthHeaders:   auth,
		MaxRetries:    4,
	}
}

// HTTPError is returned for non-2xx responses.
type HTTPError struct {
	Method string
	URL    string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 500 {
		body = body[:500] + "…"
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, redactURL(e.URL), e.Status, body)
}

// IsStatus reports whether err is an HTTPError with the given status.
func IsStatus(err error, status int) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == status
}

// ApplyHeaders sets custom headers, then auth headers, on req.
func (c *Client) ApplyHeaders(req *http.Request) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent)
	}
	for k, v := range c.CustomHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range c.AuthHeaders {
		req.Header.Set(k, v)
	}
}

// Request describes a request whose body can be replayed on retry.
type Request struct {
	Method string
	URL    string
	Header http.Header
	// Body is replayable. For streaming uploads use DoStream instead.
	Body []byte
}

// Do performs a request with retries and returns the (2xx) response. The
// caller must close the body.
func (c *Client) Do(ctx context.Context, r Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if c.Pace != nil {
			if err := c.Pace(ctx); err != nil {
				return nil, err
			}
		}
		var body io.Reader
		if r.Body != nil {
			body = bytes.NewReader(r.Body)
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
		if err != nil {
			return nil, err
		}
		for k, vs := range r.Header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		c.ApplyHeaders(req)
		if err := c.applyToken(ctx, req); err != nil {
			return nil, err
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = &proxyError{err, c.proxy.Redact(err.Error())}
			if attempt >= c.MaxRetries {
				return nil, lastErr
			}
			if err := sleep(ctx, backoff(attempt, 0)); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		lastErr = &HTTPError{Method: r.Method, URL: r.URL, Status: resp.StatusCode, Body: c.proxy.Redact(string(b))}
		if !retryable(resp.StatusCode) || attempt >= c.MaxRetries {
			return nil, lastErr
		}
		if err := sleep(ctx, backoff(attempt, retryAfter(resp))); err != nil {
			return nil, err
		}
	}
}

// DoStream performs a single non-retried request with a streaming body.
func (c *Client) DoStream(ctx context.Context, method, url string, header http.Header, body io.Reader) (*http.Response, error) {
	if c.Pace != nil {
		if err := c.Pace(ctx); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	c.ApplyHeaders(req)
	if err := c.applyToken(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &proxyError{err, c.proxy.Redact(err.Error())}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, &HTTPError{Method: method, URL: url, Status: resp.StatusCode, Body: c.proxy.Redact(string(b))}
	}
	return resp, nil
}

type proxyError struct {
	cause   error
	message string
}

func (e *proxyError) Error() string { return e.message }
func (e *proxyError) Unwrap() error { return e.cause }

// JSON performs a request with an optional JSON body and decodes a JSON
// response into out (when non-nil).
func (c *Client) JSON(ctx context.Context, method, url string, in, out any) error {
	r := Request{Method: method, URL: url, Header: http.Header{"Accept": {"application/json"}}}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		r.Body = b
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, redactURL(url), err)
	}
	return nil
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusRequestTimeout ||
		status == http.StatusBadGateway || status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout || status == http.StatusInternalServerError
}

func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

func backoff(attempt int, hint time.Duration) time.Duration {
	if hint > 0 {
		if hint > 2*time.Minute {
			hint = 2 * time.Minute
		}
		return hint
	}
	d := time.Duration(1<<attempt) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d + time.Duration(rand.Int64N(int64(500*time.Millisecond)))
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Pacer returns a function enforcing a minimum interval between calls.
func Pacer(interval time.Duration) func(ctx context.Context) error {
	ch := make(chan struct{}, 1)
	ch <- struct{}{}
	var next time.Time
	return func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
		now := time.Now()
		wait := next.Sub(now)
		if wait < 0 {
			wait = 0
			next = now
		}
		next = next.Add(interval)
		ch <- struct{}{}
		if wait > 0 {
			return sleep(ctx, wait)
		}
		return nil
	}
}

// redactURL removes query strings, which may carry tokens or signatures.
func redactURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

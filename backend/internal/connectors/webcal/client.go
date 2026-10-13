package webcal

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

func subscriptionURL(address string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || u.Hostname() == "" || u.Opaque != "" {
		return nil, errors.New("webcal: invalid subscription URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme == "webcal" {
		u.Scheme = "https"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("webcal: URL must use HTTP, HTTPS or webcal")
	}
	u.Fragment = ""
	return u, nil
}

func newClient(cred connectors.Credential) *httpx.Client {
	custom := map[string]string{}
	for header, val := range cred.Headers {
		if !strings.EqualFold(header, "If-None-Match") && !strings.EqualFold(header, "If-Modified-Since") {
			custom[header] = val
		}
	}
	client := httpx.New(custom, nil, cred.Proxy)
	client.HTTP.Timeout = 30 * time.Second
	return client
}

type fetchRequest struct {
	credential connectors.Credential
	limits     limits
	previous   *cursor
}

type feedResponse struct {
	body         []byte
	etag         string
	lastModified string
	notModified  bool
}

func fetch(ctx context.Context, fetching fetchRequest) (feedResponse, error) {
	address, err := subscriptionURL(fetching.credential.String("feedUrl"))
	if err != nil {
		return feedResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	header := http.Header{"Accept": {"text/calendar"}}
	if fetching.previous == nil {
		header.Set("Cache-Control", "no-cache")
	}
	if fetching.previous != nil {
		if fetching.previous.ETag != "" {
			header.Set("If-None-Match", fetching.previous.ETag)
		}
		if fetching.previous.LastModified != "" {
			header.Set("If-Modified-Since", fetching.previous.LastModified)
		}
	}
	response, err := newClient(fetching.credential).Do(ctx, httpx.Request{Method: http.MethodGet, URL: address.String(), Header: header, AcceptStatuses: []int{http.StatusNotModified}})
	if err != nil {
		return feedResponse{}, fetchError(address.Hostname(), err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		if fetching.previous == nil || (fetching.previous.ETag == "" && fetching.previous.LastModified == "") {
			return feedResponse{}, errors.New("webcal: unsolicited HTTP 304")
		}
		return feedResponse{notModified: true}, nil
	}
	if response.StatusCode != http.StatusOK {
		return feedResponse{}, fmt.Errorf("webcal: %s returned HTTP %d, expected 200", address.Hostname(), response.StatusCode)
	}
	body, err := readFeed(response, fetching.limits.bytes)
	if err != nil {
		return feedResponse{}, err
	}
	return feedResponse{body: body, etag: response.Header.Get("ETag"), lastModified: response.Header.Get("Last-Modified")}, nil
}

func fetchError(host string, err error) error {
	var failure *httpx.HTTPError
	if errors.As(err, &failure) {
		return fmt.Errorf("webcal: %s returned HTTP %d", host, failure.Status)
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("webcal: %s request timed out", host)
	}
	return fmt.Errorf("webcal: %s request failed (connection, TLS or redirect)", host)
}

func readFeed(response *http.Response, maxBytes int64) ([]byte, error) {
	var reader io.Reader = response.Body
	switch strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		decoded, err := gzip.NewReader(reader)
		if err != nil {
			return nil, errors.New("webcal: invalid compressed response")
		}
		defer decoded.Close()
		reader = decoded
	default:
		return nil, errors.New("webcal: unsupported response encoding")
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, errors.New("webcal: calendar body could not be read completely")
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("webcal: calendar exceeds download size limit")
	}
	return body, nil
}

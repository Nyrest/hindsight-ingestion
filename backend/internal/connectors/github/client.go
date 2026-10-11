package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

const defaultBaseURL = "https://api.github.com"

type client struct {
	base *url.URL
	http *httpx.Client
}

func newClient(cred connectors.Credential) (*client, error) {
	base := cred.String("baseUrl")
	if base == "" {
		base = defaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("GitHub API base URL must be an http(s) URL without credentials, query or fragment")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	h := httpx.New(cred.Headers, map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}, cred.Proxy)
	if tok := cred.String("token"); tok != "" {
		h.AuthHeaders["Authorization"] = "Bearer " + tok
	}
	c := &client{base: u, http: h}
	h.RetryResponse = retryResponse
	h.Pace = httpx.Pacer(100 * time.Millisecond)
	h.HTTP.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many GitHub API redirects")
		}
		if !c.allowed(req.URL) {
			req.Header = make(http.Header)
			return errors.New("GitHub API redirect is outside the configured API base URL")
		}
		return nil
	}
	return c, nil
}

func (c *client) allowed(u *url.URL) bool {
	return u.User == nil && u.Fragment == "" && u.Scheme == c.base.Scheme && strings.EqualFold(u.Host, c.base.Host) &&
		path.Clean(u.Path) == u.Path &&
		(c.base.Path == "" || u.Path == c.base.Path || strings.HasPrefix(u.Path, c.base.Path+"/"))
}

func (c *client) endpoint(p string, q url.Values) string {
	result := c.base.String() + "/" + strings.TrimLeft(p, "/")
	if len(q) > 0 {
		result += "?" + q.Encode()
	}
	return result
}

func (c *client) get(ctx context.Context, p string, q url.Values, out any) error {
	resp, err := c.http.Do(ctx, httpx.Request{Method: http.MethodGet, URL: c.endpoint(p, q)})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func list[T any](ctx context.Context, c *client, p string, q url.Values) ([]T, error) {
	if q == nil {
		q = url.Values{}
	} else {
		q = cloneQuery(q)
	}
	q.Set("per_page", "100")
	next := c.endpoint(p, q)
	seen := map[string]bool{}
	var all []T
	for next != "" {
		if seen[next] {
			return nil, errors.New("GitHub pagination loop")
		}
		seen[next] = true
		u, err := url.Parse(next)
		if err != nil || !c.allowed(u) {
			return nil, errors.New("GitHub pagination is outside the configured API base URL")
		}
		resp, err := c.http.Do(ctx, httpx.Request{Method: http.MethodGet, URL: next})
		if err != nil {
			return nil, err
		}
		var page []T
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode GitHub page: %w", err)
		}
		if page == nil {
			return nil, errors.New("GitHub listing must be an array, not null")
		}
		all = append(all, page...)
		next = ""
		for _, link := range strings.Split(resp.Header.Get("Link"), ",") {
			parts := strings.Split(strings.TrimSpace(link), ";")
			for _, attr := range parts[1:] {
				if strings.TrimSpace(attr) == `rel="next"` {
					ref, err := url.Parse(strings.Trim(strings.TrimSpace(parts[0]), "<>"))
					if err != nil {
						return nil, errors.New("invalid GitHub pagination link")
					}
					next = u.ResolveReference(ref).String()
				}
			}
		}
	}
	return all, nil
}

func cloneQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func retryResponse(resp *http.Response, body []byte, attempt int) (bool, time.Duration) {
	status := resp.StatusCode
	rateLimited := status == 429 || (status == 403 && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(string(body)), "rate limit")))
	if rateLimited {
		if hint := resp.Header.Get("Retry-After"); hint != "" {
			if seconds, err := strconv.Atoi(hint); err == nil && seconds >= 0 {
				return true, time.Duration(seconds) * time.Second
			}
			if t, err := http.ParseTime(hint); err == nil {
				return true, max(time.Until(t), time.Duration(0))
			}
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				return true, max(time.Until(time.Unix(reset, 0))+time.Second, time.Second)
			}
		}
		return true, time.Minute * time.Duration(1<<min(attempt, 4))
	}
	return status == 408 || status == 500 || status == 502 || status == 503 || status == 504, time.Second * time.Duration(1<<min(attempt, 5))
}

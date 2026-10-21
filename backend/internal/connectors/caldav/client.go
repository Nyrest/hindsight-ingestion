package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

const propertiesBody = `<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:resourcetype/><d:displayname/><d:current-user-principal/><c:calendar-home-set/></d:prop></d:propfind>`
const queryBody = `<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-data/></d:prop><c:filter><c:comp-filter name="VCALENDAR"/></c:filter></c:calendar-query>`

type client struct {
	base *url.URL
	http *httpx.Client
}

func endpoint(address string, relative bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || u == nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(!(relative && u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/")) &&
			(u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http"))) {
		return nil, errors.New("caldav: invalid URL; use HTTP(S) without embedded credentials, query or fragment")
	}
	return u, nil
}

func newClient(cred connectors.Credential) (*client, error) {
	u, err := endpoint(cred.String("baseUrl"), false)
	if err != nil {
		return nil, err
	}
	auth := map[string]string{}
	if token := cred.String("bearerToken"); token != "" {
		auth["Authorization"] = "Bearer " + token
	} else if user := cred.String("username"); user != "" {
		r := &http.Request{Header: http.Header{}}
		r.SetBasicAuth(user, cred.String("password"))
		auth["Authorization"] = r.Header.Get("Authorization")
	}
	headers := map[string]string{}
	for name, value := range cred.Headers {
		if !strings.EqualFold(name, "Depth") && !strings.EqualFold(name, "Content-Type") {
			headers[name] = value
		}
	}
	c := httpx.New(headers, auth, cred.Proxy)
	// Follow redirects explicitly to preserve PROPFIND/REPORT bodies and to avoid
	// forwarding custom authentication headers to another origin.
	c.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client{base: u, http: c}, nil
}

func port(u *url.URL) string {
	if u.Port() != "" {
		return u.Port()
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func (c *client) resolve(base *url.URL, address string) (*url.URL, error) {
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("caldav: missing resource URL")
	}
	reference, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return nil, errors.New("caldav: invalid resource URL")
	}
	u, err := endpoint(base.ResolveReference(reference).String(), false)
	if err != nil {
		return nil, err
	}
	if u.Scheme != c.base.Scheme || !strings.EqualFold(u.Hostname(), c.base.Hostname()) || port(u) != port(c.base) {
		return nil, errors.New("caldav: cross-origin URL refused; configure the final CalDAV server URL")
	}
	return u, nil
}

type properties struct {
	Name         string `xml:"DAV: displayname"`
	ResourceType struct {
		Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
	} `xml:"DAV: resourcetype"`
	Principal struct {
		Href string `xml:"DAV: href"`
	} `xml:"DAV: current-user-principal"`
	Homes struct {
		Hrefs []string `xml:"DAV: href"`
	} `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set"`
	CalendarData *string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
}

type response struct {
	Href     string    `xml:"DAV: href"`
	Status   string    `xml:"DAV: status"`
	Error    *struct{} `xml:"DAV: error"`
	Propstat []struct {
		Status string     `xml:"DAV: status"`
		Prop   properties `xml:"DAV: prop"`
		Error  *struct{}  `xml:"DAV: error"`
	} `xml:"DAV: propstat"`
}

type multistatus struct {
	XMLName   xml.Name   `xml:"DAV: multistatus"`
	Responses []response `xml:"DAV: response"`
	Error     *struct{}  `xml:"DAV: error"`
	url       *url.URL
}

func success(status string) bool {
	parts := strings.Fields(status)
	return len(parts) >= 2 && strings.HasPrefix(parts[0], "HTTP/") && parts[1] == "200"
}

type davRequest struct {
	method, depth, body string
	target              *url.URL
	maxBytes            int64
}

func (c *client) request(ctx context.Context, request davRequest) (multistatus, error) {
	for redirects := 0; redirects < 6; redirects++ {
		resp, err := c.http.Do(ctx, httpx.Request{Method: request.method, URL: request.target.String(), Body: []byte(request.body),
			Header:         http.Header{"Depth": {request.depth}, "Content-Type": {"application/xml; charset=utf-8"}},
			AcceptStatuses: []int{301, 302, 303, 307, 308}})
		if err != nil {
			return multistatus{}, requestError(ctx, request.method, err)
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			resp.Body.Close()
			request.target, err = c.resolve(request.target, resp.Header.Get("Location"))
			if err != nil {
				return multistatus{}, err
			}
			continue
		}
		result, err := readMultistatus(resp, request.maxBytes)
		result.url = request.target
		return result, err
	}
	return multistatus{}, errors.New("caldav: redirect limit exceeded")
}

func requestError(ctx context.Context, method string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *httpx.HTTPError
	if errors.As(err, &failure) {
		return fmt.Errorf("caldav: %s returned HTTP %d", method, failure.Status)
	}
	return errors.New("caldav: request failed (connection, TLS or proxy)")
}

func readMultistatus(resp *http.Response, maxBytes int64) (multistatus, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		return multistatus{}, fmt.Errorf("caldav: expected HTTP 207, received %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return multistatus{}, errors.New("caldav: incomplete response body")
	}
	if int64(len(body)) > maxBytes {
		return multistatus{}, errors.New("caldav: response exceeds size limit")
	}
	var result multistatus
	decoder := xml.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&result); err != nil {
		return multistatus{}, errors.New("caldav: malformed multistatus response")
	}
	if err := checkTrailingXML(decoder); err != nil {
		return multistatus{}, err
	}
	return result, result.validate()
}

func checkTrailingXML(decoder *xml.Decoder) error {
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("caldav: malformed trailing XML")
		}
		switch value := token.(type) {
		case xml.CharData:
			if len(bytes.TrimSpace(value)) == 0 {
				continue
			}
		case xml.Comment:
			continue
		}
		return errors.New("caldav: unexpected trailing XML")
	}
}

func (result multistatus) validate() error {
	if result.Error != nil {
		return errors.New("caldav: server returned an incomplete multistatus")
	}
	for _, response := range result.Responses {
		if response.Error != nil || (response.Status != "" && !success(response.Status)) {
			return errors.New("caldav: resource failed in multistatus; deletion disabled")
		}
		for _, ps := range response.Propstat {
			if ps.Error != nil {
				return errors.New("caldav: property error in multistatus")
			}
		}
	}
	return nil
}

func (c *client) propfind(ctx context.Context, target *url.URL, depth string) (multistatus, error) {
	return c.request(ctx, davRequest{method: "PROPFIND", target: target, depth: depth, body: propertiesBody, maxBytes: 4 << 20})
}

type calendar struct {
	url  *url.URL
	name string
}

func (c *client) calendar(result multistatus, target *url.URL) (calendar, bool, error) {
	for _, response := range result.Responses {
		u, err := c.resolve(result.url, response.Href)
		if err != nil {
			return calendar{}, false, err
		}
		if strings.TrimRight(u.Path, "/") != strings.TrimRight(target.Path, "/") {
			continue
		}
		for _, ps := range response.Propstat {
			if success(ps.Status) && ps.Prop.ResourceType.Calendar != nil {
				name := ps.Prop.Name
				if name == "" {
					name = path.Base(strings.TrimRight(u.Path, "/"))
				}
				return calendar{u, name}, true, nil
			}
		}
	}
	return calendar{}, false, nil
}

func (c *client) calendars(ctx context.Context) ([]calendar, error) {
	initial, err := c.propfind(ctx, c.base, "0")
	if err != nil && (c.base.Path == "" || c.base.Path == "/") {
		wellKnown := c.base.ResolveReference(&url.URL{Path: "/.well-known/caldav"})
		initial, err = c.propfind(ctx, wellKnown, "0")
	}
	if err != nil {
		return nil, err
	}
	// Principal discovery is bounded, including servers that point to themselves.
	for attempts := 0; attempts < 3; attempts++ {
		if selected, ok, err := c.calendar(initial, initial.url); err != nil {
			return nil, err
		} else if ok {
			return []calendar{selected}, nil
		}
		homes, principal := initial.locations()
		if len(homes) > 0 {
			return c.homeCalendars(ctx, initial.url, homes)
		}
		if principal == "" {
			return c.listCalendars(ctx, initial.url)
		}
		u, err := c.resolve(initial.url, principal)
		if err != nil {
			return nil, err
		}
		if u.String() == initial.url.String() {
			return c.listCalendars(ctx, initial.url)
		}
		initial, err = c.propfind(ctx, u, "0")
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("caldav: calendar discovery did not resolve a calendar home")
}

func (result multistatus) locations() ([]string, string) {
	homes, principal := []string{}, ""
	for _, response := range result.Responses {
		for _, ps := range response.Propstat {
			if !success(ps.Status) {
				continue
			}
			homes = append(homes, ps.Prop.Homes.Hrefs...)
			if ps.Prop.Principal.Href != "" {
				principal = ps.Prop.Principal.Href
			}
		}
	}
	return homes, principal
}

func (c *client) homeCalendars(ctx context.Context, base *url.URL, homes []string) ([]calendar, error) {
	calendars := []calendar{}
	seen := map[string]bool{}
	for _, home := range homes {
		target, err := c.resolve(base, home)
		if err != nil {
			return nil, err
		}
		found, err := c.listCalendars(ctx, target)
		if err != nil {
			return nil, err
		}
		for _, calendar := range found {
			if !seen[calendar.url.String()] {
				calendars = append(calendars, calendar)
				seen[calendar.url.String()] = true
			}
		}
	}
	sort.Slice(calendars, func(i, j int) bool { return calendars[i].url.String() < calendars[j].url.String() })
	return calendars, nil
}

func (c *client) listCalendars(ctx context.Context, home *url.URL) ([]calendar, error) {
	result, err := c.propfind(ctx, home, "1")
	if err != nil {
		return nil, err
	}
	calendars := []calendar{}
	for _, entry := range result.Responses {
		u, err := c.resolve(result.url, entry.Href)
		if err != nil {
			return nil, err
		}
		calendar, ok, err := c.calendar(multistatus{url: result.url, Responses: []response{entry}}, u)
		if err != nil {
			return nil, err
		}
		if ok {
			calendars = append(calendars, calendar)
		}
	}
	return calendars, nil
}

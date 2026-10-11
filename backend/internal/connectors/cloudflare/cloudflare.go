package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

type Connector struct{ APIBase string }

var accountIDPattern = regexp.MustCompile("^[a-fA-F0-9]{32}$")

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type envelope[T any] struct {
	Success bool       `json:"success"`
	Result  T          `json:"result"`
	Errors  []apiError `json:"errors"`
}
type crawlPage struct {
	ID                 string          `json:"id"`
	Status             string          `json:"status"`
	Total              int             `json:"total"`
	Finished           int             `json:"finished"`
	Skipped            *int            `json:"skipped"`
	BrowserSecondsUsed float64         `json:"browserSecondsUsed"`
	Cursor             json.RawMessage `json:"cursor"`
	Records            []crawlRecord   `json:"records"`
}
type crawlRecord struct {
	URL      string `json:"url"`
	Status   string `json:"status"`
	Markdown string `json:"markdown"`
	Metadata struct {
		URL    string `json:"url"`
		Title  string `json:"title"`
		Status int    `json:"status"`
	} `json:"metadata"`
}
type watermark struct {
	StartedAt time.Time `json:"startedAt"`
	MaxAge    int64     `json:"maxAge"`
}

func (c *Connector) baseURL() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return "https://api.cloudflare.com/client/v4"
}

func (c *Connector) client(cred connectors.Credential) *httpx.Client {
	return httpx.New(cred.Headers, map[string]string{"Authorization": "Bearer " + cred.String("apiToken")}, cred.Proxy)
}

func checkResponse[T any](response envelope[T]) error {
	if response.Success {
		return nil
	}
	if len(response.Errors) > 0 {
		return fmt.Errorf("Cloudflare error %d: %s", response.Errors[0].Code, response.Errors[0].Message)
	}
	return errors.New("Cloudflare returned an unsuccessful response")
}

func (c *Connector) jobURL(cred connectors.Credential, job connectors.SourceJob) (string, error) {
	if cred.String("apiToken") == "" {
		return "", errors.New("Cloudflare API token is required")
	}
	account := cred.String("accountId")
	if !accountIDPattern.MatchString(account) {
		return "", errors.New("Cloudflare Account ID must contain 32 hexadecimal characters")
	}
	if job.AccountID != "" && job.AccountID != account {
		return "", errors.New("Cloudflare account changed; the existing job belongs to another account")
	}
	endpoint := c.baseURL() + "/accounts/" + account + "/browser-rendering/crawl"
	if job.ID != "" {
		endpoint += "/" + url.PathEscape(job.ID)
	}
	return endpoint, nil
}

func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	if _, err := c.jobURL(cred, connectors.SourceJob{}); err != nil {
		return "", err
	}
	var response envelope[struct {
		Status string `json:"status"`
	}]
	err := c.client(cred).JSON(ctx, http.MethodGet, c.baseURL()+"/user/tokens/verify", nil, &response)
	if err == nil {
		err = checkResponse(response)
	}
	if err != nil && (httpx.IsStatus(err, 400) || httpx.IsStatus(err, 401) || httpx.IsStatus(err, 403) || httpx.IsStatus(err, 404) || len(response.Errors) > 0) {
		err = c.client(cred).JSON(ctx, http.MethodGet, c.baseURL()+"/accounts/"+cred.String("accountId")+"/tokens/verify", nil, &response)
	}
	if err != nil {
		return "", err
	}
	if err := checkResponse(response); err != nil {
		return "", err
	}
	if response.Result.Status != "active" {
		return "", errors.New("Cloudflare API token is not active")
	}
	return "Token is active; Browser Run account access and Edit permission are checked when a crawl starts", nil
}

func (c *Connector) Browse(context.Context, connectors.Credential, connectors.BrowseRequest) (connectors.BrowseResult, error) {
	return connectors.BrowseResult{}, errors.New("Enter the website URL directly")
}

func (c *Connector) crawlRequest(req connectors.ScanRequest) (map[string]any, connectors.SourceJob, error) {
	if err := c.ValidateConfig(req.Config, req.Filter); err != nil {
		return nil, connectors.SourceJob{}, err
	}
	job := connectors.SourceJob{AccountID: req.Credential.String("accountId"), StartedAt: time.Now().UTC(), Full: req.Full, MaxAge: numberConfig(req.Config, "maxAge", 3600), Status: "running"}
	pageURL, err := NormalizeURL(connectors.AsString(req.Config["url"]))
	if err != nil {
		return nil, job, err
	}
	body := map[string]any{
		"url": pageURL, "source": sourceConfig(req.Config), "render": connectors.AsBool(req.Config["render"]),
		"limit": numberConfig(req.Config, "limit", 100), "depth": numberConfig(req.Config, "depth", 20),
		"formats": []string{"markdown"}, "crawlPurposes": []string{"ai-input"}, "contentUse": "reference",
	}
	var cursor watermark
	if !req.Full && len(req.Cursor) > 0 {
		if err := json.Unmarshal(req.Cursor, &cursor); err != nil {
			return nil, job, fmt.Errorf("invalid crawl cursor: %w", err)
		}
		since := cursor.StartedAt.Add(-time.Duration(cursor.MaxAge)*time.Second - 10*time.Minute)
		if cursor.StartedAt.IsZero() || since.Before(job.StartedAt.AddDate(-1, 0, 0)) || since.After(job.StartedAt) {
			job.Full = true
		} else {
			body["modifiedSince"] = since.Unix()
		}
	} else {
		job.Full = true
	}
	if job.Full {
		job.MaxAge = 0
	}
	body["maxAge"] = job.MaxAge
	include, _ := stringList(req.Config["includePatterns"])
	exclude, _ := stringList(req.Config["excludePatterns"])
	body["options"] = map[string]any{
		"includeExternalLinks": connectors.AsBool(req.Config["includeExternalLinks"]), "includeSubdomains": connectors.AsBool(req.Config["includeSubdomains"]),
		"includePatterns": include, "excludePatterns": exclude,
	}
	if connectors.AsBool(req.Config["render"]) {
		addBrowserOptions(body, req.Config)
	}
	return body, job, nil
}

func sourceConfig(cfg map[string]any) string {
	if configured := connectors.AsString(cfg["source"]); configured != "" {
		return configured
	}
	return "all"
}

func addBrowserOptions(body, cfg map[string]any) {
	wait := connectors.AsString(cfg["waitUntil"])
	if wait == "" {
		wait = "domcontentloaded"
	}
	body["gotoOptions"] = map[string]any{"waitUntil": wait, "timeout": numberConfig(cfg, "navigationTimeout", 60000)}
	if selector := connectors.AsString(cfg["waitForSelector"]); selector != "" {
		body["waitForSelector"] = map[string]any{"selector": selector, "timeout": numberConfig(cfg, "selectorTimeout", 30000), "visible": connectors.BoolDefault(cfg, "selectorVisible", true)}
	}
	body["waitForTimeout"] = numberConfig(cfg, "waitForTimeout", 0)
	blocked := cfg["rejectResourceTypes"]
	if blocked == nil {
		blocked = []string{"image", "media", "font"}
	}
	body["rejectResourceTypes"], _ = stringList(blocked)
}

func (c *Connector) StartJob(ctx context.Context, req connectors.ScanRequest) (connectors.SourceJob, error) {
	body, job, err := c.crawlRequest(req)
	if err != nil {
		return job, err
	}
	endpoint, err := c.jobURL(req.Credential, job)
	if err != nil {
		return job, err
	}
	client := c.client(req.Credential)
	// Creation has no idempotency key; transport retries could create a second paid job.
	client.MaxRetries = 0
	var response envelope[string]
	if err := client.JSON(ctx, http.MethodPost, endpoint, body, &response); err != nil {
		return job, fmt.Errorf("create crawl (not automatically retried): %w", err)
	}
	if err := checkResponse(response); err != nil {
		return job, err
	}
	if response.Result == "" {
		return job, errors.New("Cloudflare did not return a crawl job ID")
	}
	job.ID = response.Result
	return job, nil
}

func (c *Connector) fetch(ctx context.Context, cred connectors.Credential, job connectors.SourceJob, query url.Values) (crawlPage, error) {
	endpoint, err := c.jobURL(cred, job)
	if err != nil {
		return crawlPage{}, err
	}
	var response envelope[crawlPage]
	if err := c.client(cred).JSON(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil, &response); err != nil {
		return crawlPage{}, err
	}
	if err := checkResponse(response); err != nil {
		return crawlPage{}, err
	}
	if response.Result.ID != job.ID {
		return crawlPage{}, errors.New("Cloudflare returned a different crawl job ID")
	}
	return response.Result, nil
}

func (c *Connector) PollJob(ctx context.Context, cred connectors.Credential, job connectors.SourceJob) (connectors.SourceJob, error) {
	page, err := c.fetch(ctx, cred, job, url.Values{"limit": {"1"}, "cacheTTL": {"0"}})
	if err != nil {
		if httpx.IsStatus(err, 404) || httpx.IsStatus(err, 410) {
			job.Status = "expired"
		}
		return job, err
	}
	job.Status, job.Total, job.Finished, job.BrowserSecondsUsed = page.Status, page.Total, page.Finished, page.BrowserSecondsUsed
	switch job.Status {
	case "running", "completed":
		return job, nil
	case "cancelled_due_to_timeout", "cancelled_due_to_limits", "cancelled_by_user", "errored":
		return job, fmt.Errorf("Cloudflare crawl ended with status %s", job.Status)
	default:
		return job, fmt.Errorf("unknown Cloudflare crawl status %q", job.Status)
	}
}

func (c *Connector) CancelJob(ctx context.Context, cred connectors.Credential, job connectors.SourceJob) error {
	endpoint, err := c.jobURL(cred, job)
	if err != nil {
		return err
	}
	var response envelope[json.RawMessage]
	err = c.client(cred).JSON(ctx, http.MethodDelete, endpoint, nil, &response)
	if httpx.IsStatus(err, 404) || httpx.IsStatus(err, 410) {
		return nil
	}
	if err != nil {
		return err
	}
	return checkResponse(response)
}

func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	job, err := c.StartJob(ctx, req)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.CancelJob(cleanup, req.Credential, job); err != nil && req.Log != nil {
			req.Log("warn", err.Error())
		}
	}()
	for {
		job, err = c.PollJob(ctx, req.Credential, job)
		if err != nil {
			return connectors.ScanResult{}, err
		}
		if job.Status == "completed" {
			return c.ReadJob(ctx, req, job)
		}
		if err := wait(ctx, 15*time.Second); err != nil {
			return connectors.ScanResult{}, err
		}
	}
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func paginationCursor(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return "", fmt.Errorf("invalid crawl pagination cursor")
	}
	return number.String(), nil
}

func (c *Connector) ReadJob(ctx context.Context, req connectors.ScanRequest, job connectors.SourceJob) (outcome connectors.ScanResult, err error) {
	failures := pageFailures{}
	defer func() { outcome.Error = errors.Join(outcome.Error, failures.Err()) }()
	visited := map[string]bool{}
	query := url.Values{"limit": {"100"}, "cacheTTL": {"0"}}
	recordCount := 0
	skippedCount := 0
	for {
		page, err := c.fetch(ctx, req.Credential, job, query)
		if err != nil {
			return outcome, err
		}
		if page.Status != "completed" {
			return outcome, fmt.Errorf("cannot ingest crawl job in status %s", page.Status)
		}
		for _, record := range page.Records {
			if record.Status == "skipped" {
				skippedCount++
			}
			if record.Status == "skipped" && outsideScope(req.Config, record.URL) {
				continue
			}
			recordCount++
			normalized, err := recordItem(record, req.Config)
			if err != nil {
				failures.Add(err.Error())
				continue
			}
			if normalized.ContentError != "" {
				failures.Add(normalized.Name + ": " + normalized.ContentError)
			}
			if err := req.Emit(normalized); err != nil {
				return outcome, err
			}
		}
		cursor, err := paginationCursor(page.Cursor)
		if err != nil {
			return outcome, err
		}
		if cursor == "" {
			complete := page.Finished == page.Total && recordCount == page.Total && int64(page.Total) < numberConfig(req.Config, "limit", 100) && failures.count == 0
			if page.Skipped != nil && skippedCount != *page.Skipped {
				complete = false
			}
			outcome.Complete = job.Full && complete
			if !complete {
				outcome.Error = errors.Join(outcome.Error, errors.New("crawl inventory is incomplete (page limit, missing results or failed pages); deletion and cursor commit are disabled"))
			}
			outcome.Cursor, _ = json.Marshal(watermark{StartedAt: job.StartedAt, MaxAge: job.MaxAge})
			return outcome, nil
		}
		if visited[cursor] {
			return outcome, errors.New("Cloudflare repeated a pagination cursor")
		}
		visited[cursor] = true
		query.Set("cursor", cursor)
	}
}

type pageFailures struct {
	count    int
	examples []string
}

func (f *pageFailures) Add(message string) {
	f.count++
	if len(f.examples) < 5 {
		characters := []rune(message)
		if len(characters) > 300 {
			message = string(characters[:300]) + "…"
		}
		f.examples = append(f.examples, message)
	}
}

func (f pageFailures) Err() error {
	if f.count == 0 {
		return nil
	}
	return fmt.Errorf("%d crawl page(s) failed: %s", f.count, strings.Join(f.examples, "; "))
}

func recordItem(record crawlRecord, cfg map[string]any) (connectors.SourceItem, error) {
	pageURL := record.Metadata.URL
	if pageURL == "" {
		pageURL = record.URL
	}
	normalized, err := NormalizeURL(pageURL)
	if err != nil {
		return connectors.SourceItem{}, fmt.Errorf("crawl returned an invalid page URL")
	}
	identity := sha256.Sum256([]byte(normalized))
	revision := sha256.Sum256([]byte(record.Markdown))
	parsed, _ := url.Parse(normalized)
	title := record.Metadata.Title
	if title == "" {
		title = normalized
	}
	item := connectors.SourceItem{
		ID: hex.EncodeToString(identity[:]), Name: title, Path: parsed.EscapedPath(), Kind: connectors.KindPage,
		Revision: hex.EncodeToString(revision[:]), MIMEType: "text/markdown", Size: int64(len(record.Markdown)),
		Metadata:   map[string]string{"url": normalized, "title": title, "website_url": connectors.AsString(cfg["url"])},
		Attributes: map[string]any{"url": normalized},
	}
	switch {
	case record.Status != "completed":
		item.ContentError = "page status: " + record.Status
	case record.Metadata.Status < 200 || record.Metadata.Status >= 300:
		item.ContentError = fmt.Sprintf("page HTTP status: %d", record.Metadata.Status)
	case strings.TrimSpace(record.Markdown) == "":
		item.ContentError = "page returned empty Markdown"
	case int64(len(record.Markdown)) > numberConfig(cfg, "maxPageSizeMB", 5)<<20:
		item.ContentError = "page exceeds configured size limit"
	}
	if item.ContentError == "" {
		item.Opaque, _ = json.Marshal(record.Markdown)
	}
	return item, nil
}

func (c *Connector) OpenContent(_ context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	if req.Item.ContentError != "" {
		return connectors.SourceContent{}, errors.New(req.Item.ContentError)
	}
	var markdown string
	if err := json.Unmarshal(req.Item.Opaque, &markdown); err != nil {
		return connectors.SourceContent{}, err
	}
	return connectors.SourceContent{Text: markdown, Context: fmt.Sprintf("Web page %q (%s)", req.Item.Name, req.Item.Metadata["url"])}, nil
}

func outsideScope(cfg map[string]any, rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	rootURL, rootErr := NormalizeURL(connectors.AsString(cfg["url"]))
	root, _ := url.Parse(rootURL)
	if err != nil || rootErr != nil || parsed.Hostname() == "" {
		return false
	}
	if !connectors.AsBool(cfg["includeExternalLinks"]) && !strings.EqualFold(parsed.Hostname(), root.Hostname()) {
		if !connectors.AsBool(cfg["includeSubdomains"]) || !strings.HasSuffix(strings.ToLower(parsed.Hostname()), "."+strings.ToLower(root.Hostname())) {
			return true
		}
	}
	excluded, _ := stringList(cfg["excludePatterns"])
	for _, pattern := range excluded {
		if patternMatches(pattern, rawURL) {
			return true
		}
	}
	included, _ := stringList(cfg["includePatterns"])
	if len(included) == 0 {
		return false
	}
	for _, pattern := range included {
		if patternMatches(pattern, rawURL) {
			return false
		}
	}
	return true
}

func patternMatches(pattern, target string) bool {
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(quoted, "\\*\\*", "\x00")
	quoted = strings.ReplaceAll(quoted, "\\*", "[^/]*")
	quoted = strings.ReplaceAll(quoted, "\x00", ".*")
	matched, _ := regexp.MatchString("^"+quoted+"$", target)
	return matched
}

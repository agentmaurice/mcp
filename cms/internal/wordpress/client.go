// Package wordpress is a small HTTP client for the native WordPress REST API
// (`/wp-json/wp/v2`: posts, pages, media) authenticated with the core
// Application Passwords (HTTP Basic). It is written from the public
// documentation only, needs no WordPress plugin and shares no code with it.
package wordpress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Bounds of the client. Every request has a deadline, no redirect is followed and
// no answer is read beyond maxBody.
const (
	jsonTimeout   = 30 * time.Second
	uploadTimeout = 120 * time.Second
	maxBody       = 4 << 20 // 2xx answers
	maxErrBody    = 64 << 10
	minUser       = 4  // the user name is masked everywhere: a shorter one would corrupt results
	minPassword   = 16 // WordPress generates 24 characters; a shorter secret cannot be masked reliably
	restPath      = "/wp-json/wp/v2"
)

// Stable error values mapped to tool error codes by the server.
var (
	// ErrTransport: the request may have been sent but no answer was read.
	ErrTransport = errors.New("wordpress transport failure")
	// ErrBadBody: a 2xx answer whose body is unreadable, truncated, too large or not the documented form.
	ErrBadBody = errors.New("wordpress answered 2xx with an unexpected body")
)

// RedirectError is a 3xx answer. Redirects are never followed and the
// destination is deliberately not kept.
type RedirectError struct{ Code int }

func (e *RedirectError) Error() string {
	return fmt.Sprintf("wordpress answered with redirect %d", e.Code)
}

// APIError is a non-2xx answer: the HTTP status and the WordPress error body
// (`{"code","message","data":{"status"}}`) when it could be read.
type APIError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration // 429 only
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("wordpress error %d %s", e.Status, e.Code)
	}
	return fmt.Sprintf("wordpress error %d", e.Status)
}

// Config is the validated connection to one site.
type Config struct {
	BaseURL  string // scheme://host[:port][/subpath], no trailing slash
	User     string
	Password string
}

// ParseConfig validates the three secrets. When it returns a non-empty issue
// the adapter is not configured; the issue never contains a value. A missing
// value gives issue "missing" with the variable names in missing.
func ParseConfig(rawURL, user, password string) (cfg Config, issue string, missing []string) {
	rawURL, user, password = strings.TrimSpace(rawURL), strings.TrimSpace(user), strings.TrimSpace(password)
	if rawURL == "" {
		missing = append(missing, "CMS_WORDPRESS_URL")
	}
	if user == "" {
		missing = append(missing, "CMS_WORDPRESS_USER")
	}
	if password == "" {
		missing = append(missing, "CMS_WORDPRESS_APP_PASSWORD")
	}
	if len(missing) > 0 {
		return Config{}, "missing", missing
	}
	u, err := url.Parse(rawURL)
	switch {
	case err != nil || u.Host == "" || u.Hostname() == "":
		return Config{}, "url_invalid", nil
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(rawURL, "?") || strings.Contains(rawURL, "#"):
		return Config{}, "url_invalid", nil
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
		// Plain HTTP is accepted only for a loopback test host: WordPress itself only
		// enables Application Passwords on SSL sites or local environments.
	case u.Scheme == "http":
		return Config{}, "url_not_https", nil
	default:
		return Config{}, "url_invalid", nil
	}
	if len(user) < minUser {
		return Config{}, "user_too_short", nil
	}
	if len(password) < minPassword {
		return Config{}, "app_password_too_short", nil
	}
	return Config{BaseURL: strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/"), User: user, Password: password}, "", nil
}

func isLoopbackHost(h string) bool {
	h = strings.ToLower(h)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Client calls the WordPress REST API. The credentials are only ever placed in
// the Authorization header: never in an error, a log line or a result.
type Client struct {
	cfg        Config
	http       *http.Client
	uploadHTTP *http.Client
}

// New builds a client from a validated configuration.
func New(cfg Config) *Client {
	mk := func(d time.Duration) *http.Client {
		return &http.Client{Timeout: d, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	return &Client{cfg: cfg, http: mk(jsonTimeout), uploadHTTP: mk(uploadTimeout)}
}

// Secrets lists the credential values (and their derived forms) to mask in any text.
func (c *Client) Secrets() []string {
	return []string{c.cfg.User, c.cfg.Password, strings.ReplaceAll(c.cfg.Password, " ", "")}
}

// BaseURL returns the site base (used to mask it in error text, never returned).
func (c *Client) BaseURL() string { return c.cfg.BaseURL }

// Post is the subset of a post, page or attachment object the tools return.
type Post struct {
	ID        int    `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Link      string `json:"link"`
	Slug      string `json:"slug"`
	Date      string `json:"date"`
	DateGMT   string `json:"date_gmt"`
	Modified  string `json:"modified"`
	SourceURL string `json:"source_url"` // media only
	MimeType  string `json:"mime_type"`  // media only
	AltText   string `json:"alt_text"`   // media only
}

const postFields = "id,type,status,link,slug,date,date_gmt,modified"

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, query url.Values, headers map[string]string, body io.Reader) ([]byte, error) {
	target := c.cfg.BaseURL + restPath + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, errors.New("wordpress request could not be built")
	}
	req.SetBasicAuth(c.cfg.User, c.cfg.Password)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		// url.Error embeds the URL: keep the message generic.
		return nil, fmt.Errorf("%w: network error", ErrTransport)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 3 {
		return nil, &RedirectError{Code: resp.StatusCode}
	}
	if resp.StatusCode/100 != 2 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		e := &APIError{Status: resp.StatusCode}
		var wp struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(bytes.TrimSpace(payload), &wp) == nil {
			e.Code, e.Message = wp.Code, wp.Message
			if len([]rune(e.Message)) > 300 {
				e.Message = string([]rune(e.Message)[:300])
			}
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
		return nil, e
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(payload) > maxBody {
		return nil, fmt.Errorf("%w: unreadable or too large", ErrBadBody)
	}
	return payload, nil
}

const (
	defaultRetryAfter = time.Hour
	maxRetryAfter     = 24 * time.Hour
)

// parseRetryAfter reads seconds or an HTTP date, bounded to [1 s, 24 h]; anything
// negative, unreadable or beyond the bound gives one hour.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 1 || n > int64(maxRetryAfter/time.Second) {
			return defaultRetryAfter
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d < time.Second || d > maxRetryAfter {
			return defaultRetryAfter
		}
		return d
	}
	return defaultRetryAfter
}

// decodePost accepts a post, page or trashed item only when it carries every
// field the tools return, with a valid type: id > 0 and non-empty type, status,
// link, date and modified. A partial object cannot confirm a write.
func decodePost(payload []byte) (Post, error) {
	var p Post
	if err := json.Unmarshal(bytes.TrimSpace(payload), &p); err != nil || p.ID <= 0 ||
		p.Type == "" || p.Status == "" || p.Link == "" || p.Date == "" || p.Modified == "" {
		return Post{}, fmt.Errorf("%w: incomplete or invalid WordPress object", ErrBadBody)
	}
	return p, nil
}

// forTarget rejects an answer that is not about the requested target: another id
// (when wantID > 0) or another type (when wantType is known). Such an answer
// cannot confirm a write, nor a read.
func forTarget(p Post, err error, wantType string, wantID int) (Post, error) {
	if err != nil {
		return p, err
	}
	if (wantID > 0 && p.ID != wantID) || (wantType != "" && p.Type != wantType) {
		return Post{}, fmt.Errorf("%w: the answer is about another item", ErrBadBody)
	}
	return p, nil
}

// decodeMedia requires id > 0, type attachment and non-empty source_url, link and mime_type.
func decodeMedia(payload []byte) (Post, error) {
	var p Post
	if err := json.Unmarshal(bytes.TrimSpace(payload), &p); err != nil || p.ID <= 0 ||
		p.Type != "attachment" || p.SourceURL == "" || p.Link == "" || p.MimeType == "" {
		return Post{}, fmt.Errorf("%w: incomplete or invalid WordPress media", ErrBadBody)
	}
	return p, nil
}

// wantType is the post type a collection must answer with.
func wantType(postType string) string {
	if postType == "page" {
		return "page"
	}
	return "post"
}

// Endpoint maps a post type to its REST collection.
func Endpoint(postType string) string {
	if postType == "page" {
		return "pages"
	}
	return "posts"
}

// Write is a request that changes the site: the caller must treat any answer
// other than a clear 4xx refusal as an unknown outcome.
func jsonHeaders() map[string]string { return map[string]string{"Content-Type": "application/json"} }

// CreatePost calls POST /posts or /pages.
func (c *Client) CreatePost(ctx context.Context, postType string, fields map[string]any) (Post, error) {
	body, err := json.Marshal(fields)
	if err != nil {
		return Post{}, errors.New("wordpress request could not be encoded")
	}
	payload, err := c.do(ctx, c.http, http.MethodPost, "/"+Endpoint(postType), url.Values{"_fields": {postFields}}, jsonHeaders(), bytes.NewReader(body))
	if err != nil {
		return Post{}, err
	}
	p, err := decodePost(payload)
	return forTarget(p, err, wantType(postType), 0)
}

// UpdatePost calls POST /posts/{id} or /pages/{id} (partial update).
func (c *Client) UpdatePost(ctx context.Context, postType string, id int, fields map[string]any) (Post, error) {
	body, err := json.Marshal(fields)
	if err != nil {
		return Post{}, errors.New("wordpress request could not be encoded")
	}
	payload, err := c.do(ctx, c.http, http.MethodPost, "/"+Endpoint(postType)+"/"+strconv.Itoa(id), url.Values{"_fields": {postFields}}, jsonHeaders(), bytes.NewReader(body))
	if err != nil {
		return Post{}, err
	}
	p, err := decodePost(payload)
	return forTarget(p, err, wantType(postType), id)
}

// GetPost calls GET /posts/{id} or /pages/{id}.
func (c *Client) GetPost(ctx context.Context, postType string, id int) (Post, error) {
	payload, err := c.do(ctx, c.http, http.MethodGet, "/"+Endpoint(postType)+"/"+strconv.Itoa(id), url.Values{"_fields": {postFields}}, nil, nil)
	if err != nil {
		return Post{}, err
	}
	p, err := decodePost(payload)
	return forTarget(p, err, wantType(postType), id)
}

// TrashPost calls DELETE /posts/{id} or /pages/{id} WITHOUT `force`: WordPress
// moves the item to the trash. This client has no way to send force=true.
func (c *Client) TrashPost(ctx context.Context, postType string, id int) (Post, error) {
	payload, err := c.do(ctx, c.http, http.MethodDelete, "/"+Endpoint(postType)+"/"+strconv.Itoa(id), url.Values{"_fields": {postFields}}, nil, nil)
	if err != nil {
		return Post{}, err
	}
	p, err := decodePost(payload)
	return forTarget(p, err, wantType(postType), id)
}

// UploadMedia calls POST /media with the raw file as body: Content-Type is the
// MIME type and Content-Disposition carries the file name.
func (c *Client) UploadMedia(ctx context.Context, filename, mimeType string, data []byte) (Post, error) {
	h := map[string]string{
		"Content-Type":        mimeType,
		"Content-Disposition": fmt.Sprintf(`attachment; filename="%s"`, filename),
	}
	payload, err := c.do(ctx, c.uploadHTTP, http.MethodPost, "/media", url.Values{"_fields": {"id,type,source_url,link,mime_type,alt_text,slug"}}, h, bytes.NewReader(data))
	if err != nil {
		return Post{}, err
	}
	return decodeMedia(payload)
}

// SetMediaAlt calls POST /media/{id} with alt_text.
func (c *Client) SetMediaAlt(ctx context.Context, id int, alt string) error {
	body, _ := json.Marshal(map[string]any{"alt_text": alt})
	payload, err := c.do(ctx, c.http, http.MethodPost, "/media/"+strconv.Itoa(id), url.Values{"_fields": {"id,alt_text"}}, jsonHeaders(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	var out struct {
		ID      int     `json:"id"`
		AltText *string `json:"alt_text"`
	}
	// The answer must be this media and carry the requested text. WordPress
	// sanitizes the text (trim, collapsed whitespace), so compare normalized forms.
	if err := json.Unmarshal(bytes.TrimSpace(payload), &out); err != nil || out.ID != id || out.AltText == nil ||
		strings.Join(strings.Fields(*out.AltText), " ") != strings.Join(strings.Fields(alt), " ") {
		return fmt.Errorf("%w: the answer does not confirm the requested alt_text", ErrBadBody)
	}
	return nil
}

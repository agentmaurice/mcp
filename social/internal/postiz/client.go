// Package postiz is a small HTTP client for the public REST API of Postiz
// (https://docs.postiz.com/public-api/introduction). It is written from the
// public documentation only and shares no code with Postiz.
package postiz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stable error values mapped to tool error codes by the server.
var (
	ErrUnauthorized = errors.New("postiz rejected the API key")
	ErrNotFound     = errors.New("postiz resource not found")
	ErrUpstream     = errors.New("postiz request failed")
	// ErrTransport: the request may have been sent but no answer was read.
	ErrTransport = errors.New("postiz transport failure")
	// ErrBadBody: a 2xx answer whose body is unreadable, truncated or malformed.
	ErrBadBody = errors.New("postiz answered 2xx with an unreadable body")
)

// StatusError is an HTTP status the client has no specific mapping for.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("postiz request failed: status %d", e.Code) }
func (e *StatusError) Unwrap() error { return ErrUpstream }

// RedirectError is a 3xx answer: redirects are never followed (every request
// must be counted by the limiter), and the destination is deliberately not kept.
type RedirectError struct{ Code int }

func (e *RedirectError) Error() string {
	return fmt.Sprintf("postiz answered with redirect %d", e.Code)
}

// RateLimitedError is returned by the local limiter or by an HTTP 429.
type RateLimitedError struct {
	RetryAfter time.Duration
	Local      bool
}

func (e *RateLimitedError) Error() string {
	if e.Local {
		return "client-side rate limit reached"
	}
	return "postiz rate limit reached"
}

// Limiter is a sliding window limiter: at most Max requests per Window.
type Limiter struct {
	mu     sync.Mutex
	Max    int
	Window time.Duration
	Now    func() time.Time
	events []time.Time
}

// NewLimiter returns a limiter of max requests per hour.
func NewLimiter(max int) *Limiter { return &Limiter{Max: max, Window: time.Hour, Now: time.Now} }

// Take reserves one request or returns the time to wait.
func (l *Limiter) Take() (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	kept := l.events[:0]
	for _, t := range l.events {
		if now.Sub(t) < l.Window {
			kept = append(kept, t)
		}
	}
	l.events = kept
	if l.Max > 0 && len(l.events) >= l.Max {
		return l.events[0].Add(l.Window).Sub(now), false
	}
	l.events = append(l.events, now)
	return 0, true
}

// Client calls the Postiz public API. The API key is only ever placed in the
// Authorization header: it is never part of an error, a log line or a result.
type Client struct {
	BaseURL string
	apiKey  string
	HTTP    *http.Client
	Limiter *Limiter
}

// Secret returns the API key so the server can mask it in any text.
func (c *Client) Secret() string { return c.apiKey }

// New builds a client; baseURL is the public API root (for example
// https://api.postiz.com/public/v1).
func New(baseURL, apiKey string, limiter *Limiter) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		Limiter: limiter,
	}
}

// Integration is one connected channel.
type Integration struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Picture    string `json:"picture"`
	Disabled   bool   `json:"disabled"`
	Profile    string `json:"profile"`
}

// Media is an uploaded file.
type Media struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// PostItem is one channel entry of a create request.
type PostItem struct {
	Integration struct {
		ID string `json:"id"`
	} `json:"integration"`
	Value    []PostValue    `json:"value"`
	Group    string         `json:"group,omitempty"`
	Settings map[string]any `json:"settings"`
}

// PostValue is the content of a post.
type PostValue struct {
	Content string  `json:"content"`
	Image   []Media `json:"image"`
}

// CreateRequest is the body of POST /posts.
type CreateRequest struct {
	Type      string     `json:"type"`
	Date      string     `json:"date"`
	ShortLink bool       `json:"shortLink"`
	Tags      []struct{} `json:"tags"`
	Posts     []PostItem `json:"posts"`
}

// Created is one element of the POST /posts response.
type Created struct {
	PostID      string `json:"postId"`
	Integration string `json:"integration"`
}

// Post is one element of the GET /posts response.
type Post struct {
	ID          string  `json:"id"`
	PublishDate string  `json:"publishDate"`
	ReleaseURL  string  `json:"releaseURL"`
	State       string  `json:"state"`
	Error       *string `json:"error"`
	Integration struct {
		ID string `json:"id"`
	} `json:"integration"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, contentType string, body io.Reader, out any) error {
	if wait, ok := c.Limiter.Take(); !ok {
		return &RateLimitedError{RetryAfter: wait, Local: true}
	}
	target := c.BaseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return fmt.Errorf("%w: invalid request", ErrUpstream)
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// url.Error may embed the URL, never the header; keep the message generic anyway.
		return fmt.Errorf("%w: network error", ErrTransport)
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RateLimitedError{RetryAfter: retryAfter(resp)}
	case resp.StatusCode/100 == 3:
		return &RedirectError{Code: resp.StatusCode}
	case resp.StatusCode/100 != 2:
		return &StatusError{Code: resp.StatusCode}
	}
	if readErr != nil {
		return ErrBadBody
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			return ErrBadBody
		}
	}
	return nil
}

// retryAfter reads Retry-After (seconds or HTTP date). The Postiz docs do not
// document the header (hypothesis); without it the window is one hour, the
// documented rate-limit period.
func retryAfter(resp *http.Response) time.Duration {
	return parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
}

const (
	defaultRetryAfter = time.Hour
	maxRetryAfter     = 24 * time.Hour
)

// parseRetryAfter reads seconds or an HTTP date, bounded to [1 s, 24 h]; anything
// negative, unreadable or beyond the bound gives one hour. No arithmetic can overflow.
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

// Integrations calls GET /integrations.
func (c *Client) Integrations(ctx context.Context) ([]Integration, error) {
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/integrations", nil, "", nil, &raw); err != nil {
		return nil, err
	}
	// The documented form is a JSON array; null, {} or anything else is invalid.
	var out []Integration
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' || json.Unmarshal(t, &out) != nil {
		return nil, ErrBadBody
	}
	return out, nil
}

// Upload calls POST /upload (multipart, field `file`).
func (c *Client) Upload(ctx context.Context, filename, mimeType string, data []byte) (Media, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	h.Set("Content-Type", mimeType)
	part, err := w.CreatePart(h)
	if err != nil {
		return Media{}, fmt.Errorf("%w: multipart", ErrUpstream)
	}
	_, _ = part.Write(data)
	if err := w.Close(); err != nil {
		return Media{}, fmt.Errorf("%w: multipart", ErrUpstream)
	}
	var out Media
	if err := c.do(ctx, http.MethodPost, "/upload", nil, w.FormDataContentType(), &buf, &out); err != nil {
		return Media{}, err
	}
	if out.ID == "" || out.Path == "" {
		return Media{}, fmt.Errorf("%w: unexpected upload response", ErrUpstream)
	}
	return out, nil
}

// CreatePosts calls POST /posts.
func (c *Client) CreatePosts(ctx context.Context, req CreateRequest) ([]Created, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: encode", ErrUpstream)
	}
	var out []Created
	if err := c.do(ctx, http.MethodPost, "/posts", nil, "application/json", bytes.NewReader(body), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListPosts calls GET /posts for a date range.
func (c *Client) ListPosts(ctx context.Context, start, end time.Time) ([]Post, error) {
	q := url.Values{}
	q.Set("startDate", start.UTC().Format("2006-01-02T15:04:05.000Z"))
	q.Set("endDate", end.UTC().Format("2006-01-02T15:04:05.000Z"))
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/posts", q, "", nil, &raw); err != nil {
		return nil, err
	}
	// The documented form is {"posts":[...]}; null, {}, a missing or null posts: invalid.
	var out struct {
		Posts *[]Post `json:"posts"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Posts == nil {
		return nil, ErrBadBody
	}
	return *out.Posts, nil
}

// DeletePost calls DELETE /posts/{id}; Postiz deletes every post of the same group.
func (c *Client) DeletePost(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/posts/"+url.PathEscape(id), nil, "", nil, nil)
}

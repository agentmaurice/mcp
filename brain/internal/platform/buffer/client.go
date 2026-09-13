package buffer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrNotFound       = errors.New("buffer content not found")
	ErrUnavailable    = errors.New("buffer service unavailable")
	ErrInvalidPayload = errors.New("invalid buffer payload")
	ErrTooLarge       = errors.New("buffer payload too large")
	ErrBinaryContent  = errors.New("binary buffer content is not supported")
)

// BufferedContent mirrors the stable wire contract exposed by the AgentMaurice server.
// It intentionally contains no storage or logging behavior.
type BufferedContent struct {
	Version    string `json:"version"`
	Namespace  string `json:"namespace"`
	MimeType   string `json:"mime_type,omitempty"`
	Structured any    `json:"structured,omitempty"`
	Text       string `json:"text,omitempty"`
	Binary     string `json:"binary,omitempty"`
}

type loadResponse struct {
	Content BufferedContent `json:"content"`
}

// Client is a read-only client for the authenticated shared buffer service.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(serviceURL, token string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(serviceURL), "/"),
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.baseURL != "" && c.token != ""
}

// LoadText resolves an opaque buffer key through the configured service URL.
// The key never controls the host and is never included in returned errors.
func (c *Client) LoadText(ctx context.Context, key string, maxBytes int64) ([]byte, string, error) {
	if !c.Configured() {
		return nil, "", ErrUnavailable
	}
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}

	requestURL := c.baseURL + "/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: create request", ErrUnavailable)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, "", ErrNotFound
	default:
		return nil, "", fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}

	maxResponseBytes := maxBytes*2 + 64*1024
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("%w: read response", ErrUnavailable)
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, "", ErrTooLarge
	}

	var loaded loadResponse
	if err := json.Unmarshal(body, &loaded); err != nil {
		return nil, "", ErrInvalidPayload
	}
	content := loaded.Content
	if content.Binary != "" {
		if _, err := base64.StdEncoding.DecodeString(content.Binary); err != nil {
			return nil, "", ErrInvalidPayload
		}
		return nil, content.MimeType, ErrBinaryContent
	}
	if content.Text != "" {
		if int64(len(content.Text)) > maxBytes {
			return nil, "", ErrTooLarge
		}
		return []byte(content.Text), content.MimeType, nil
	}
	if content.Structured != nil {
		structured, err := json.Marshal(content.Structured)
		if err != nil {
			return nil, "", ErrInvalidPayload
		}
		if int64(len(structured)) > maxBytes {
			return nil, "", ErrTooLarge
		}
		return structured, content.MimeType, nil
	}
	return nil, content.MimeType, ErrInvalidPayload
}

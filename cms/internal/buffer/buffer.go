// Package buffer reads files from the AgentMaurice Buffer service by reference.
// A `buffer://<key>` reference lets the host platform hand an image to the CMS
// MCP without the bytes crossing the model context (same mechanism as the social,
// document and browser MCPs).
package buffer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client reads from the Buffer service endpoint (for example `.../api/v1/buffer`).
type Client struct {
	URL       string
	Token     string
	Namespace string
	HTTP      *http.Client
}

// FromEnv reads CMS_BUFFER_SERVICE_URL, CMS_BUFFER_TOKEN and CMS_BUFFER_NAMESPACE
// (default cms); the generic BUFFER_SERVICE_URL, BUFFER_TOKEN and BUFFER_NAMESPACE
// are accepted. It returns nil when the Buffer is not configured.
func FromEnv() *Client {
	get := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(os.Getenv(n)); v != "" {
				return v
			}
		}
		return ""
	}
	c := &Client{
		URL:       get("CMS_BUFFER_SERVICE_URL", "BUFFER_SERVICE_URL"),
		Token:     get("CMS_BUFFER_TOKEN", "BUFFER_TOKEN"),
		Namespace: get("CMS_BUFFER_NAMESPACE", "BUFFER_NAMESPACE"),
		HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
	if c.URL == "" || c.Token == "" {
		return nil
	}
	if c.Namespace == "" {
		c.Namespace = "cms"
	}
	return c
}

// ErrNotFound is returned when a reference does not exist or has expired.
var ErrNotFound = fmt.Errorf("buffer reference not found")

// ErrTooLarge is returned when a referenced file exceeds the caller's limit.
var ErrTooLarge = fmt.Errorf("buffer content too large")

type loadResponse struct {
	Content struct {
		MimeType string         `json:"mime_type"`
		Text     string         `json:"text"`
		Binary   string         `json:"binary"`
		Metadata map[string]any `json:"metadata"`
	} `json:"content"`
}

// Load reads a `buffer://` reference and returns the raw bytes (decoding the
// base64 forms: `binary`, or `text` with metadata.encoding=base64). The key
// never selects the host: it is only a path segment under the configured URL.
func (c *Client) Load(ctx context.Context, ref string, maxBytes int) ([]byte, error) {
	key := strings.TrimPrefix(ref, "buffer://")
	if key == "" || strings.ContainsAny(key, "?#") {
		return nil, fmt.Errorf("invalid buffer reference")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/"+url.PathEscape(key), nil)
	if err != nil {
		return nil, fmt.Errorf("buffer load: invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("buffer load: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("buffer load: status %d", resp.StatusCode)
	}
	limit := int64(maxBytes)*2 + 1<<16
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("buffer load: read failed")
	}
	if int64(len(body)) > limit {
		return nil, ErrTooLarge
	}
	var out loadResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("buffer load: unexpected response")
	}
	var data []byte
	switch {
	case out.Content.Binary != "":
		data, err = base64.StdEncoding.DecodeString(out.Content.Binary)
	case out.Content.Metadata["encoding"] == "base64":
		data, err = base64.StdEncoding.DecodeString(out.Content.Text)
	default:
		data = []byte(out.Content.Text)
	}
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("buffer load: unexpected content")
	}
	if len(data) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

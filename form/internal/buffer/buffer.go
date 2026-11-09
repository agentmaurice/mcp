// Package buffer moves files between the Form MCP and the AgentMaurice Buffer
// service so that the host platform can materialize them in Storage. A tool
// result then carries a `buffer://<key>` reference instead of the bytes, and an
// input file can be given by reference (same mechanism as the artifact MCP).
package buffer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned when a reference does not exist or has expired.
var ErrNotFound = errors.New("buffer reference not found")

// ErrTooLarge is returned when a referenced file exceeds the caller's limit.
var ErrTooLarge = errors.New("buffer content too large")

// Client talks to the Buffer service endpoint (for example `.../api/v1/buffer`).
type Client struct {
	URL       string
	Token     string
	Namespace string
	TTL       int
	HTTP      *http.Client
}

// FromEnv reads FORM_BUFFER_SERVICE_URL, FORM_BUFFER_TOKEN,
// FORM_BUFFER_NAMESPACE (default form) and FORM_BUFFER_TTL_SECONDS
// (default 3600); the generic BUFFER_SERVICE_URL, BUFFER_TOKEN and
// BUFFER_NAMESPACE are accepted. It returns nil when the Buffer is not configured.
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
		URL:       get("FORM_BUFFER_SERVICE_URL", "BUFFER_SERVICE_URL"),
		Token:     get("FORM_BUFFER_TOKEN", "BUFFER_TOKEN"),
		Namespace: get("FORM_BUFFER_NAMESPACE", "BUFFER_NAMESPACE"),
		TTL:       3600,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
	if c.URL == "" || c.Token == "" {
		return nil
	}
	if c.Namespace == "" {
		c.Namespace = "form"
	}
	if v, err := strconv.Atoi(get("FORM_BUFFER_TTL_SECONDS")); err == nil && v > 0 {
		c.TTL = v
	}
	return c
}

type storeRequest struct {
	Namespace  string         `json:"namespace"`
	MimeType   string         `json:"mime_type"`
	TTLSeconds int            `json:"ttl_seconds"`
	Summary    string         `json:"summary"`
	ToolName   string         `json:"tool_name"`
	Text       string         `json:"text"`
	Metadata   map[string]any `json:"metadata"`
}

type storeResponse struct {
	Reference struct {
		Key string `json:"key"`
	} `json:"reference"`
}

// Upload stores a binary file (base64 in `text`, with metadata.encoding=base64)
// and returns its `buffer://` reference.
func (c *Client) Upload(ctx context.Context, toolName, filename, mimeType string, data []byte, sha256hex string) (string, error) {
	body, err := json.Marshal(storeRequest{
		Namespace: c.Namespace, MimeType: mimeType, TTLSeconds: c.TTL,
		Summary:  fmt.Sprintf("%s (%d bytes)", filename, len(data)),
		ToolName: toolName, Text: base64.StdEncoding.EncodeToString(data),
		Metadata: map[string]any{"encoding": "base64", "filename": filename, "sha256": sha256hex, "bytes": len(data)},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("buffer upload: request failed")
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("buffer upload: status %d", resp.StatusCode)
	}
	var out storeResponse
	if err := json.Unmarshal(payload, &out); err != nil || out.Reference.Key == "" {
		return "", fmt.Errorf("buffer upload: unexpected response")
	}
	return "buffer://" + strings.TrimPrefix(out.Reference.Key, "buffer://"), nil
}

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

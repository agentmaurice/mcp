// Package buffer uploads rendered files to the AgentMaurice Buffer service so
// that the host platform can materialize them in Storage. The tool result then carries
// a `buffer://<key>` reference instead of the bytes (same mechanism as the
// document and browser MCPs).
package buffer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client posts to the Buffer service endpoint (for example `.../api/v1/buffer`).
type Client struct {
	URL       string
	Token     string
	Namespace string
	TTL       int
	HTTP      *http.Client
}

// FromEnv reads ARTIFACT_BUFFER_SERVICE_URL, ARTIFACT_BUFFER_TOKEN,
// ARTIFACT_BUFFER_NAMESPACE (default artifact) and ARTIFACT_BUFFER_TTL_SECONDS
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
		URL:       get("ARTIFACT_BUFFER_SERVICE_URL", "BUFFER_SERVICE_URL"),
		Token:     get("ARTIFACT_BUFFER_TOKEN", "BUFFER_TOKEN"),
		Namespace: get("ARTIFACT_BUFFER_NAMESPACE", "BUFFER_NAMESPACE"),
		TTL:       3600,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
	if c.URL == "" || c.Token == "" {
		return nil
	}
	if c.Namespace == "" {
		c.Namespace = "artifact"
	}
	if v, err := strconv.Atoi(get("ARTIFACT_BUFFER_TTL_SECONDS")); err == nil && v > 0 {
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
func (c *Client) Upload(ctx context.Context, filename, mimeType string, data []byte, sha256hex string) (string, error) {
	body, err := json.Marshal(storeRequest{
		Namespace: c.Namespace, MimeType: mimeType, TTLSeconds: c.TTL,
		Summary:  fmt.Sprintf("%s (%d bytes)", filename, len(data)),
		ToolName: "artifact_render_v1", Text: base64.StdEncoding.EncodeToString(data),
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
		return "", fmt.Errorf("buffer upload: %w", err)
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

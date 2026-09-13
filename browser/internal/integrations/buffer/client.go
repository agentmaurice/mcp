package buffer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// FailureReason indicates why a buffer upload failed.
type FailureReason string

const (
	FailureUnavailable FailureReason = "unavailable"
	FailureTooLarge    FailureReason = "too_large"
	FailureAuth        FailureReason = "auth"
	FailureNetwork     FailureReason = "network"
	FailureServer      FailureReason = "server"
	FailureOther       FailureReason = "other"
)

// UploadError describes a buffer upload failure.
type UploadError struct {
	Reason     FailureReason
	StatusCode int
	Message    string
}

func (e *UploadError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("buffer upload failed (%s, status=%d): %s", e.Reason, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("buffer upload failed (%s): %s", e.Reason, e.Message)
}

// Options configures the buffer HTTP client.
type Options struct {
	BaseURL        string
	AuthToken      string
	MaxUploadBytes int
	NetworkRetries int
	Timeout        time.Duration
}

// UploadRequest is the request payload sent to /buffer.
type UploadRequest struct {
	Namespace  string
	ToolName   string
	MimeType   string
	Text       string
	Content    any
	Summary    string
	TTLSeconds int
	Metadata   map[string]any
}

type storeRequestBody struct {
	Namespace  string         `json:"namespace"`
	Content    any            `json:"content,omitempty"`
	MimeType   string         `json:"mime_type,omitempty"`
	TTLSeconds int            `json:"ttl_seconds,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	Text       string         `json:"text,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type storeResponseBody struct {
	Reference BufferReference `json:"reference"`
	Message   string          `json:"message,omitempty"`
}

// BufferReference is the buffer pointer returned by the buffer service.
type BufferReference struct {
	Type      string `json:"type"`
	Key       string `json:"key"`
	Size      int64  `json:"size,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	Summary   string `json:"summary,omitempty"`
	ExpiresIn int64  `json:"expires_in,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Client uploads tool outputs to AgentMaurice buffer service.
type Client struct {
	baseURL        string
	authToken      string
	maxUploadBytes int
	networkRetries int
	httpClient     *http.Client
	logger         *zap.Logger
}

// NewClient creates a new buffer client from options.
func NewClient(opts Options, logger *zap.Logger) *Client {
	if logger == nil {
		logger = zap.NewNop()
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &Client{
		baseURL:        strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		authToken:      strings.TrimSpace(opts.AuthToken),
		maxUploadBytes: opts.MaxUploadBytes,
		networkRetries: max(0, opts.NetworkRetries),
		httpClient: &http.Client{
			Timeout: timeout,
		},
		logger: logger.Named("buffer-client"),
	}
}

// Upload stores content in the buffer and returns a reference key.
func (c *Client) Upload(ctx context.Context, req UploadRequest) (*BufferReference, error) {
	if c.baseURL == "" {
		return nil, &UploadError{Reason: FailureUnavailable, Message: "buffer base URL is not set"}
	}
	if c.authToken == "" {
		return nil, &UploadError{Reason: FailureAuth, Message: "buffer auth token is required"}
	}
	if strings.TrimSpace(req.Namespace) == "" {
		return nil, &UploadError{Reason: FailureOther, Message: "buffer namespace is required"}
	}
	if req.Content == nil && req.Text == "" {
		return nil, &UploadError{Reason: FailureOther, Message: "either content or text must be set"}
	}

	body := storeRequestBody{
		Namespace:  req.Namespace,
		Content:    req.Content,
		MimeType:   req.MimeType,
		TTLSeconds: req.TTLSeconds,
		Summary:    req.Summary,
		ToolName:   req.ToolName,
		Text:       req.Text,
		Metadata:   req.Metadata,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &UploadError{Reason: FailureOther, Message: fmt.Sprintf("marshal request: %v", err)}
	}

	if c.maxUploadBytes > 0 && len(payload) > c.maxUploadBytes {
		return nil, &UploadError{
			Reason:  FailureTooLarge,
			Message: fmt.Sprintf("request payload too large: %d bytes > %d", len(payload), c.maxUploadBytes),
		}
	}

	endpoint := c.baseURL + "/buffer"

	for attempt := 0; attempt <= c.networkRetries; attempt++ {
		ref, err := c.uploadOnce(ctx, endpoint, payload)
		if err == nil {
			return ref, nil
		}

		var typedErr *UploadError
		if errors.As(err, &typedErr) {
			if typedErr != nil && typedErr.Reason == FailureNetwork && attempt < c.networkRetries {
				c.logger.Warn("buffer upload network error, retrying",
					zap.Int("attempt", attempt+1),
					zap.Int("max_retries", c.networkRetries),
					zap.String("error", typedErr.Message),
				)
				continue
			}
		}

		return nil, err
	}

	return nil, &UploadError{Reason: FailureNetwork, Message: "network retries exhausted"}
}

func (c *Client) uploadOnce(ctx context.Context, endpoint string, payload []byte) (*BufferReference, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, &UploadError{Reason: FailureOther, Message: fmt.Sprintf("build request: %v", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.authToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if isNetworkFailure(err) {
			return nil, &UploadError{Reason: FailureNetwork, Message: err.Error()}
		}
		return nil, &UploadError{Reason: FailureOther, Message: err.Error()}
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	bodyText := strings.TrimSpace(string(bodyBytes))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var response storeResponseBody
		if err := json.Unmarshal(bodyBytes, &response); err != nil {
			return nil, &UploadError{Reason: FailureOther, StatusCode: resp.StatusCode, Message: "invalid JSON response"}
		}
		if response.Reference.Type != "buffer" || response.Reference.Key == "" {
			return nil, &UploadError{Reason: FailureOther, StatusCode: resp.StatusCode, Message: "missing buffer reference"}
		}
		return &response.Reference, nil
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &UploadError{Reason: FailureAuth, StatusCode: resp.StatusCode, Message: bodyText}
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return nil, &UploadError{Reason: FailureTooLarge, StatusCode: resp.StatusCode, Message: bodyText}
	case resp.StatusCode >= 500:
		return nil, &UploadError{Reason: FailureServer, StatusCode: resp.StatusCode, Message: bodyText}
	default:
		return nil, &UploadError{Reason: FailureOther, StatusCode: resp.StatusCode, Message: bodyText}
	}
}

func isNetworkFailure(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection refused") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "timeout") ||
		strings.Contains(message, "tls") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "connection reset")
}

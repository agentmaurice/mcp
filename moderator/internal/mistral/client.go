package mistral

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/config"
	"go.uber.org/zap"
)

const userAgent = "mcp-moderator/0.1"

// Client wraps the HTTP interactions with the Mistral moderation API.
type Client struct {
	baseURL      *url.URL
	apiKey       string
	defaultModel string
	httpClient   *http.Client
	logger       *zap.Logger
}

// NewClient builds a Client from configuration.
func NewClient(cfg config.MistralConfig, logger *zap.Logger) (*Client, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("mistral base URL cannot be empty")
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Mistral base URL %q: %w", cfg.BaseURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("Mistral URL must include scheme and host (got %q)", cfg.BaseURL)
	}

	client := &http.Client{
		Timeout: cfg.Timeout,
	}

	return &Client{
		baseURL:      parsed,
		apiKey:       cfg.APIKey,
		defaultModel: cfg.Model,
		httpClient:   client,
		logger:       logger,
	}, nil
}

// DefaultModel returns the model configured for the client.
func (c *Client) DefaultModel() string {
	return c.defaultModel
}

// ModerateChat sends a moderation request to Mistral's chat moderation endpoint.
func (c *Client) ModerateChat(ctx context.Context, request ChatModerationRequest) (*ChatModerationResponse, error) {
	if len(request.Input) == 0 {
		return nil, fmt.Errorf("moderation request requires at least one message")
	}
	if strings.TrimSpace(request.Model) == "" {
		request.Model = c.defaultModel
	}
	if request.Model == "" {
		return nil, fmt.Errorf("no moderation model specified")
	}

	endpoint := c.baseURL.ResolveReference(&url.URL{Path: chatModerationsPath})

	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to encode moderation request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create moderation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", userAgent)

	c.logger.Info("calling Mistral moderation API",
		zap.String("endpoint", endpoint.String()),
		zap.Int("message_count", len(request.Input)),
		zap.String("model", request.Model),
	)

	c.logger.Debug("moderation request details",
		zap.Int("payload_size_bytes", len(payload)),
		zap.String("payload_preview", truncateString(string(payload), 500)),
	)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Error("Mistral API call failed",
			zap.String("endpoint", endpoint.String()),
			zap.Error(err),
		)
		return nil, fmt.Errorf("moderation request failed: %w", err)
	}
	defer resp.Body.Close()

	c.logger.Info("Mistral API responded",
		zap.Int("status_code", resp.StatusCode),
		zap.String("status", resp.Status),
	)

	c.logger.Debug("response details",
		zap.String("endpoint", endpoint.String()),
		zap.Int64("content_length", resp.ContentLength),
	)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Debug("failed to read response body", zap.Error(err))
		return nil, fmt.Errorf("failed to read moderation response: %w", err)
	}

	c.logger.Debug("response body read",
		zap.Int("body_size_bytes", len(body)),
	)

	if resp.StatusCode >= 400 {
		var apiErr APIErrorResponse
		if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
			c.logger.Error("Mistral API returned error",
				zap.Int("status_code", resp.StatusCode),
				zap.String("error_message", apiErr.Error.Message),
				zap.String("error_type", apiErr.Error.Type),
			)
			return nil, fmt.Errorf("mistral API error (%d): %s", resp.StatusCode, apiErr.Error.Message)
		}
		c.logger.Error("Mistral API returned error (raw)",
			zap.Int("status_code", resp.StatusCode),
			zap.String("raw_body", truncateString(string(body), 500)),
		)
		return nil, fmt.Errorf("mistral API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result ChatModerationResponse
	if err := json.Unmarshal(body, &result); err != nil {
		c.logger.Debug("failed to decode response JSON",
			zap.Error(err),
			zap.String("raw_body_preview", truncateString(string(body), 500)),
		)
		return nil, fmt.Errorf("failed to decode moderation response: %w", err)
	}

	// Determine if content was flagged
	flagged := false
	for _, r := range result.Results {
		if r.Flagged {
			flagged = true
			break
		}
	}

	c.logger.Info("Mistral moderation completed",
		zap.String("response_id", result.ID),
		zap.String("model", result.Model),
		zap.Int("results_count", len(result.Results)),
		zap.Bool("flagged", flagged),
	)
	result.Raw = append([]byte(nil), body...)
	for i := range result.Results {
		if result.Results[i].Categories == nil {
			result.Results[i].Categories = make(map[string]bool)
		}
		if result.Results[i].CategoryScores == nil {
			result.Results[i].CategoryScores = make(map[string]float64)
		}
		if result.Results[i].SensitiveTopics == nil {
			result.Results[i].SensitiveTopics = make(map[string]bool)
		}
		if result.Results[i].Details == nil {
			result.Results[i].Details = make(map[string]any)
		}
		if rawItem, err := json.Marshal(result.Results[i]); err == nil {
			result.Results[i].Raw = rawItem
		}
	}

	return &result, nil
}

// truncateString truncates a string to maxLen characters for logging purposes.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	openai "github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
)

// Client implements LLM operations
type Client struct {
	provider                  string
	client                    *openai.Client
	model                     string
	embeddingModel            openai.EmbeddingModel
	apiKey                    string
	baseURL                   string
	httpClient                *http.Client
	logger                    *zap.Logger
	embeddingRetryMaxAttempts int
	embeddingRetryBaseDelay   time.Duration
	embeddingRetryMaxDelay    time.Duration
}

// NewClient creates a new LLM client
func NewClient(cfg config.LLMConfig, logger *zap.Logger) (*Client, error) {
	openaiConfig := openai.DefaultConfig(cfg.APIKey)
	if cfg.BaseURL != "" {
		openaiConfig.BaseURL = cfg.BaseURL
	}

	embeddingModel := openai.AdaEmbeddingV2
	if cfg.EmbeddingModel != "" {
		embeddingModel = openai.EmbeddingModel(cfg.EmbeddingModel)
	}

	httpClient := openaiConfig.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	retryMaxAttempts := cfg.EmbeddingRetryMaxAttempts
	if retryMaxAttempts < 1 {
		retryMaxAttempts = 1
	}
	retryBaseDelay := time.Duration(cfg.EmbeddingRetryBaseDelayMS) * time.Millisecond
	if retryBaseDelay <= 0 {
		retryBaseDelay = 400 * time.Millisecond
	}
	retryMaxDelay := time.Duration(cfg.EmbeddingRetryMaxDelayMS) * time.Millisecond
	if retryMaxDelay < retryBaseDelay {
		retryMaxDelay = 8 * time.Second
	}

	return &Client{
		provider:                  cfg.Provider,
		client:                    openai.NewClientWithConfig(openaiConfig),
		model:                     cfg.Model,
		embeddingModel:            embeddingModel,
		apiKey:                    cfg.APIKey,
		baseURL:                   openaiConfig.BaseURL,
		httpClient:                httpClient,
		logger:                    logger.Named("llm"),
		embeddingRetryMaxAttempts: retryMaxAttempts,
		embeddingRetryBaseDelay:   retryBaseDelay,
		embeddingRetryMaxDelay:    retryMaxDelay,
	}, nil
}

// GenerateEmbedding generates embedding for text
func (c *Client) GenerateEmbedding(ctx context.Context, text string) ([]float64, error) {
	var lastErr error
	useNoUserRequest := c.requiresNoUserEmbeddingRequest()
	for attempt := 1; attempt <= c.embeddingRetryMaxAttempts; attempt++ {
		var (
			embedding []float64
			err       error
		)
		if useNoUserRequest {
			embedding, err = c.generateEmbeddingNoUserOnce(ctx, text)
		} else {
			embedding, err = c.generateEmbeddingOpenAIOnce(ctx, text)
		}
		if err == nil {
			return embedding, nil
		}
		lastErr = err

		retryable, retryAfter := isRetryableEmbeddingError(err)
		if !retryable || attempt == c.embeddingRetryMaxAttempts {
			break
		}

		delay := c.retryDelay(attempt, retryAfter)
		c.logger.Warn("embedding request failed; retrying",
			zap.Int("attempt", attempt),
			zap.Int("max_attempts", c.embeddingRetryMaxAttempts),
			zap.Duration("retry_in", delay),
			zap.Error(err))

		if err := waitForRetry(ctx, delay); err != nil {
			return nil, shared.ErrInternal("failed to generate embedding", err)
		}
	}

	return nil, shared.ErrInternal("failed to generate embedding", lastErr)
}

func (c *Client) generateEmbeddingOpenAIOnce(ctx context.Context, text string) ([]float64, error) {
	if c.requiresNoUserEmbeddingRequest() {
		return c.generateEmbeddingNoUserOnce(ctx, text)
	}
	resp, err := c.client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
		Model: c.embeddingModel,
		Input: []string{text},
	})

	if err != nil {
		// Some OpenAI-compatible providers (OVH Kepler) reject the optional `user` field.
		// The go-openai client always sends it, so retry once with the raw no-user payload.
		if shouldFallbackToNoUserEmbedding(err) {
			fallbackEmbedding, fallbackErr := c.generateEmbeddingNoUserOnce(ctx, text)
			if fallbackErr == nil {
				c.logger.Debug("embedding request succeeded without user field fallback")
				return fallbackEmbedding, nil
			}
			c.logger.Debug("embedding no-user fallback failed",
				zap.Error(fallbackErr),
				zap.Error(err))
		}
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}

	// Convert []float32 to []float64
	embedding32 := resp.Data[0].Embedding
	embedding64 := make([]float64, len(embedding32))
	for i, v := range embedding32 {
		embedding64[i] = float64(v)
	}

	return embedding64, nil
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

func (c *Client) generateEmbeddingNoUserOnce(ctx context.Context, text string) ([]float64, error) {
	reqBody := embeddingRequest{
		Model: string(c.embeddingModel),
		Input: []string{text},
	}
	if reqBody.Model == "" {
		reqBody.Model = "mistral-embed"
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
	}

	endpoint := strings.TrimRight(c.baseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(resp.Body)
		return nil, &embeddingHTTPError{
			statusCode: resp.StatusCode,
			message:    strings.TrimSpace(string(body)),
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	var parsed embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("failed to decode embedding response: %w", err)
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}

	return parsed.Data[0].Embedding, nil
}

func (c *Client) requiresNoUserEmbeddingRequest() bool {
	provider := strings.ToLower(strings.TrimSpace(c.provider))
	if provider == "mistral" || provider == "ovh" || provider == "kepler" {
		return true
	}

	baseURL := strings.ToLower(strings.TrimSpace(c.baseURL))
	return strings.Contains(baseURL, "kepler.ai.cloud.ovh.net") || strings.Contains(baseURL, ".ovh.net")
}

func shouldFallbackToNoUserEmbedding(err error) bool {
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) && reqErr.HTTPStatusCode == http.StatusBadRequest {
		return true
	}

	var apiErr *openai.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode == http.StatusBadRequest {
		return true
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "feature 'user' is not currently supported") || strings.Contains(msg, "unsupported") && strings.Contains(msg, "user")
}

func (c *Client) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	delay := c.embeddingRetryBaseDelay * time.Duration(1<<(attempt-1))
	if delay > c.embeddingRetryMaxDelay {
		delay = c.embeddingRetryMaxDelay
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay <= 0 {
		delay = c.embeddingRetryBaseDelay
	}
	return delay
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type embeddingHTTPError struct {
	statusCode int
	message    string
	retryAfter time.Duration
}

func (e *embeddingHTTPError) Error() string {
	return fmt.Sprintf("status code: %d, message: %s", e.statusCode, e.message)
}

func isRetryableEmbeddingError(err error) (bool, time.Duration) {
	if err == nil {
		return false, 0
	}

	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		if isRetryableStatus(apiErr.HTTPStatusCode) || strings.EqualFold(apiErr.Type, "rate_limited") || containsRateLimitSignal(apiErr.Message) {
			return true, 0
		}
	}

	var requestErr *openai.RequestError
	if errors.As(err, &requestErr) {
		if isRetryableStatus(requestErr.HTTPStatusCode) {
			return true, 0
		}
	}

	var httpErr *embeddingHTTPError
	if errors.As(err, &httpErr) {
		if isRetryableStatus(httpErr.statusCode) || containsRateLimitSignal(httpErr.message) {
			return true, httpErr.retryAfter
		}
	}

	if containsRateLimitSignal(err.Error()) {
		return true, 0
	}

	return false, 0
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func containsRateLimitSignal(message string) bool {
	normalized := strings.ToLower(message)
	return strings.Contains(normalized, "rate limit") ||
		strings.Contains(normalized, "too many requests") ||
		strings.Contains(normalized, "status code: 429")
}

func parseRetryAfter(value string) time.Duration {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(trimmed); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	if at, err := http.ParseTime(trimmed); err == nil {
		delay := time.Until(at)
		if delay > 0 {
			return delay
		}
	}

	return 0
}

// GenerateAnswer generates an answer using the LLM
func (c *Client) GenerateAnswer(ctx context.Context, prompt string, maxTokens int) (string, error) {
	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: c.model,
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens: maxTokens,
	})

	if err != nil {
		return "", shared.ErrInternal("failed to generate answer", err)
	}

	return resp.Choices[0].Message.Content, nil
}

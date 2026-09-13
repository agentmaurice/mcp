package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIProvider implements embeddings through an OpenAI-compatible API.
type OpenAIProvider struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	batchSize  int
	client     *http.Client
}

// OpenAIConfig holds OpenAI-compatible provider configuration.
type OpenAIConfig struct {
	BaseURL    string
	APIKey     string
	Model      string
	Dimensions int
	BatchSize  int
}

// NewOpenAIProvider creates an OpenAI-compatible embedding provider.
func NewOpenAIProvider(cfg OpenAIConfig) *OpenAIProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.Model == "" {
		cfg.Model = "text-embedding-3-small"
	}
	if cfg.Dimensions == 0 {
		cfg.Dimensions = 1536
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 32
	}

	return &OpenAIProvider{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		batchSize:  cfg.BatchSize,
		client:     &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *OpenAIProvider) Dimensions() int { return p.dimensions }
func (p *OpenAIProvider) Model() string   { return p.model }

// Embed generates embeddings for texts in batches.
func (p *OpenAIProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	allEmbeddings := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += p.batchSize {
		end := i + p.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		embeddings, err := p.embedBatch(ctx, texts[i:end])
		if err != nil {
			return nil, fmt.Errorf("batch %d-%d failed: %w", i, end, err)
		}
		allEmbeddings = append(allEmbeddings, embeddings...)
	}
	return allEmbeddings, nil
}

type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (p *OpenAIProvider) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(openAIEmbedRequest{Model: p.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	endpoint := p.baseURL + "/embeddings"
	if strings.HasSuffix(p.baseURL, "/embeddings") {
		endpoint = p.baseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request embeddings: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return nil, fmt.Errorf("embedding endpoint returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	var result openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Data) != len(texts) {
		return nil, fmt.Errorf("expected %d embeddings, got %d", len(texts), len(result.Data))
	}

	embeddings := make([][]float32, len(result.Data))
	for i := range result.Data {
		if len(result.Data[i].Embedding) != p.dimensions {
			return nil, fmt.Errorf("embedding %d has %d dimensions, expected %d", i, len(result.Data[i].Embedding), p.dimensions)
		}
		embeddings[i] = result.Data[i].Embedding
	}
	return embeddings, nil
}

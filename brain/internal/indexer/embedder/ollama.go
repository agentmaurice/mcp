package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaProvider implements embedding via Ollama API.
type OllamaProvider struct {
	baseURL    string
	model      string
	dimensions int
	batchSize  int
	client     *http.Client
}

// OllamaConfig holds Ollama provider configuration.
type OllamaConfig struct {
	BaseURL    string
	Model      string
	Dimensions int
	BatchSize  int
}

// NewOllamaProvider creates a new Ollama embedding provider.
func NewOllamaProvider(cfg OllamaConfig) *OllamaProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434"
	}
	if cfg.Model == "" {
		cfg.Model = "nomic-embed-text"
	}
	if cfg.Dimensions == 0 {
		cfg.Dimensions = 768
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 32
	}

	return &OllamaProvider{
		baseURL:    cfg.BaseURL,
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		batchSize:  cfg.BatchSize,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (p *OllamaProvider) Dimensions() int { return p.dimensions }
func (p *OllamaProvider) Model() string   { return p.model }

// Embed generates embeddings for texts in batches.
func (p *OllamaProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	allEmbeddings := make([][]float32, 0, len(texts))

	for i := 0; i < len(texts); i += p.batchSize {
		end := i + p.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		embeddings, err := p.embedBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("batch %d-%d failed: %w", i, end, err)
		}
		allEmbeddings = append(allEmbeddings, embeddings...)
	}

	return allEmbeddings, nil
}

type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

type ollamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (p *OllamaProvider) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := ollamaEmbedRequest{
		Model: p.model,
		Input: texts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := p.baseURL + "/api/embed"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	var resp *http.Response
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err = p.client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		if attempt < maxRetries-1 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
			// Recreate request for retry
			req, _ = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("request failed after %d retries: %w", maxRetries, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Embeddings) != len(texts) {
		return nil, fmt.Errorf("expected %d embeddings, got %d", len(texts), len(result.Embeddings))
	}

	return result.Embeddings, nil
}

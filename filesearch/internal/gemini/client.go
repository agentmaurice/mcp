package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/filesearch/internal/config"
	"go.uber.org/zap"
)

// Client is a client for the Gemini API.
type Client struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	logger     *zap.Logger
}

// NewClient creates a new Gemini API client.
func NewClient(cfg config.GeminiConfig, logger *zap.Logger) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("Gemini API key is required")
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	return &Client{
		httpClient: &http.Client{Timeout: cfg.Timeout},
		apiKey:     cfg.APIKey,
		baseURL:    strings.TrimSuffix(cfg.BaseURL, "/"),
		logger:     logger.Named("gemini"),
	}, nil
}

// CreateFileSearchStore creates a new FileSearch corpus/store.
func (c *Client) CreateFileSearchStore(ctx context.Context, displayName string) (*FileSearchStore, error) {
	url := fmt.Sprintf("%s/v1beta/corpora?key=%s", c.baseURL, c.apiKey)

	reqBody := CreateStoreRequest{
		DisplayName: displayName,
	}

	var response CreateStoreResponse
	if err := c.doRequest(ctx, "POST", url, reqBody, &response); err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	return &FileSearchStore{
		Name:        response.Name,
		DisplayName: response.DisplayName,
		CreateTime:  response.CreateTime,
		UpdateTime:  response.UpdateTime,
	}, nil
}

// GetFileSearchStore retrieves a FileSearch store by name.
func (c *Client) GetFileSearchStore(ctx context.Context, storeName string) (*FileSearchStore, error) {
	url := fmt.Sprintf("%s/v1beta/%s?key=%s", c.baseURL, storeName, c.apiKey)

	var store FileSearchStore
	if err := c.doRequest(ctx, "GET", url, nil, &store); err != nil {
		return nil, fmt.Errorf("failed to get store: %w", err)
	}

	return &store, nil
}

// ImportFileViaURL imports a file from a public URL.
func (c *Client) ImportFileViaURL(ctx context.Context, publicURL, mimeType, displayName string) (*File, error) {
	url := fmt.Sprintf("%s/upload/v1beta/files?key=%s", c.baseURL, c.apiKey)

	reqBody := map[string]interface{}{
		"file": map[string]interface{}{
			"displayName": displayName,
		},
	}

	// This is a simplified implementation - actual Gemini API may require different approach
	// For now, we'll use the media upload endpoint
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Goog-Upload-Protocol", "resumable")
	req.Header.Set("X-Goog-Upload-Command", "start")
	req.Header.Set("X-Goog-Upload-Header-Content-Type", mimeType)
	req.Header.Set("Content-Type", "application/json")

	bodyBytes, _ := json.Marshal(reqBody)
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	c.logger.Debug("importing file via URL",
		zap.String("url", publicURL),
		zap.String("mimeType", mimeType))

	// Note: This is a placeholder - actual implementation would need to handle
	// the resumable upload protocol properly
	var file File
	if err := c.doRequestWithHTTPClient(req, &file); err != nil {
		return nil, fmt.Errorf("failed to import file: %w", err)
	}

	return &file, nil
}

// UploadLocalFile uploads a file from the local filesystem to Gemini.
func (c *Client) UploadLocalFile(ctx context.Context, filePath, displayName string) (*File, error) {
	// Read the file
	fileData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Detect MIME type
	mimeType := http.DetectContentType(fileData)
	if mimeType == "application/octet-stream" {
		// Try to guess from extension
		ext := filepath.Ext(filePath)
		switch strings.ToLower(ext) {
		case ".pdf":
			mimeType = "application/pdf"
		case ".txt":
			mimeType = "text/plain"
		case ".md":
			mimeType = "text/markdown"
		case ".json":
			mimeType = "application/json"
		case ".csv":
			mimeType = "text/csv"
		case ".doc":
			mimeType = "application/msword"
		case ".docx":
			mimeType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		}
	}

	// Upload file to Gemini
	url := fmt.Sprintf("%s/upload/v1beta/files?key=%s", c.baseURL, c.apiKey)

	// Create multipart form data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file part
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return nil, fmt.Errorf("failed to write file data: %w", err)
	}

	// Add metadata
	metadata := map[string]interface{}{
		"file": map[string]interface{}{
			"displayName": displayName,
		},
	}
	metadataJSON, _ := json.Marshal(metadata)
	if err := writer.WriteField("metadata", string(metadataJSON)); err != nil {
		return nil, fmt.Errorf("failed to write metadata: %w", err)
	}

	writer.Close()

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	c.logger.Debug("uploading local file",
		zap.String("path", filePath),
		zap.String("mimeType", mimeType),
		zap.String("displayName", displayName))

	var file File
	if err := c.doRequestWithHTTPClient(req, &file); err != nil {
		return nil, fmt.Errorf("failed to upload file: %w", err)
	}

	c.logger.Info("file uploaded successfully",
		zap.String("file_name", file.Name),
		zap.String("state", file.State))

	return &file, nil
}

// GetFileStatus retrieves the status of a file.
func (c *Client) GetFileStatus(ctx context.Context, fileName string) (*File, error) {
	url := fmt.Sprintf("%s/v1beta/%s?key=%s", c.baseURL, fileName, c.apiKey)

	var file File
	if err := c.doRequest(ctx, "GET", url, nil, &file); err != nil {
		return nil, fmt.Errorf("failed to get file status: %w", err)
	}

	return &file, nil
}

// DeleteFile deletes a file.
func (c *Client) DeleteFile(ctx context.Context, fileName string) error {
	url := fmt.Sprintf("%s/v1beta/%s?key=%s", c.baseURL, fileName, c.apiKey)

	if err := c.doRequest(ctx, "DELETE", url, nil, nil); err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	return nil
}

// GenerateContentWithFileSearch generates content using file search.
func (c *Client) GenerateContentWithFileSearch(ctx context.Context, storeName, query string) (*GenerateContentResponse, error) {
	// Extract corpus name for the tool configuration
	url := fmt.Sprintf("%s/v1beta/models/gemini-2.0-flash:generateContent?key=%s", c.baseURL, c.apiKey)

	reqBody := GenerateContentRequest{
		Contents: []Content{
			{
				Parts: []Part{{Text: query}},
				Role:  "user",
			},
		},
		Tools: []Tool{
			{
				FileSearch: &FileSearchConfig{
					DynamicRetrievalConfig: &DynamicRetrievalConfig{
						Mode:             "MODE_DYNAMIC",
						DynamicThreshold: 0.3,
					},
				},
			},
		},
	}

	var response GenerateContentResponse
	if err := c.doRequest(ctx, "POST", url, reqBody, &response); err != nil {
		return nil, fmt.Errorf("failed to generate content: %w", err)
	}

	return &response, nil
}

// doRequest performs an HTTP request with retry logic.
func (c *Client) doRequest(ctx context.Context, method, url string, body, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.doRequestWithHTTPClient(req, result)
}

// doRequestWithHTTPClient executes the HTTP request with retry on 5xx errors.
func (c *Client) doRequestWithHTTPClient(req *http.Request, result interface{}) error {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			c.logger.Debug("retrying request",
				zap.Int("attempt", attempt+1),
				zap.String("method", req.Method),
				zap.String("url", req.URL.String()))
			time.Sleep(time.Duration(attempt) * time.Second)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		// Check for server errors (5xx) and retry
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server error: %d", resp.StatusCode)
			c.logger.Warn("server error, will retry",
				zap.Int("status_code", resp.StatusCode),
				zap.Int("attempt", attempt+1))
			continue
		}

		// Check for client errors (4xx)
		if resp.StatusCode >= 400 {
			var errResp ErrorResponse
			if err := json.Unmarshal(bodyBytes, &errResp); err == nil && errResp.Error.Message != "" {
				return fmt.Errorf("API error: %s (code: %d)", errResp.Error.Message, errResp.Error.Code)
			}
			return fmt.Errorf("HTTP error: %d - %s", resp.StatusCode, string(bodyBytes))
		}

		// Success - parse response if result is provided
		if result != nil && resp.StatusCode != http.StatusNoContent {
			if err := json.Unmarshal(bodyBytes, result); err != nil {
				return fmt.Errorf("failed to unmarshal response: %w (body: %s)", err, string(bodyBytes))
			}
		}

		c.logger.Debug("request successful",
			zap.String("method", req.Method),
			zap.String("url", req.URL.String()),
			zap.Int("status_code", resp.StatusCode))

		return nil
	}

	if lastErr != nil {
		return fmt.Errorf("request failed after %d attempts: %w", maxRetries, lastErr)
	}

	return fmt.Errorf("request failed after %d attempts", maxRetries)
}

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Credentials struct {
	MCPID  string `json:"mcp_id"`
	APIKey string `json:"api_key"`
}

type ExtractRequest struct {
	SourceRef     string `json:"source_ref,omitempty"`
	SourceURL     string `json:"source_url,omitempty"`
	Pages         []int  `json:"pages,omitempty"`
	IncludeImages bool   `json:"include_images"`
	RequestID     string `json:"request_id,omitempty"`
}

type Client struct {
	baseURL         string
	credentialsPath string
	httpClient      *http.Client
}

func NewFromEnv() *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MCP_SIDECAR_MAURICE_URL")), "/")
	credentialsPath := strings.TrimSpace(os.Getenv("MCP_CREDENTIALS_PATH"))
	if credentialsPath == "" {
		dir := strings.TrimSpace(os.Getenv("MCP_SIDECAR_CREDENTIALS_PATH"))
		if dir == "" {
			dir = "/var/mcp/credentials"
		}
		credentialsPath = filepath.Join(dir, "credentials.json")
	}
	return &Client{baseURL: baseURL, credentialsPath: credentialsPath, httpClient: &http.Client{Timeout: 15 * time.Minute}}
}

func (c *Client) Health(ctx context.Context) error {
	if c.baseURL == "" {
		return fmt.Errorf("MCP_SIDECAR_MAURICE_URL is not configured")
	}
	credentials, err := c.loadCredentials()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/mcp/ocr/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("bridge health request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bridge health returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) Extract(ctx context.Context, input ExtractRequest) (map[string]interface{}, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("MCP_SIDECAR_MAURICE_URL is not configured")
	}
	credentials, err := c.loadCredentials()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode OCR request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/mcp/ocr", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create OCR request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OCR bridge request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if err != nil {
		return nil, fmt.Errorf("read OCR bridge response: %w", err)
	}
	if len(responseBody) > 4*1024*1024 {
		return nil, fmt.Errorf("OCR bridge returned an oversized inline response")
	}
	var result map[string]interface{}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("decode OCR bridge response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := result["error"].(string)
		if message == "" {
			message = fmt.Sprintf("OCR bridge returned status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", message)
	}
	return result, nil
}

func (c *Client) loadCredentials() (*Credentials, error) {
	data, err := os.ReadFile(c.credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("read sidecar credentials: %w", err)
	}
	var credentials Credentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return nil, fmt.Errorf("parse sidecar credentials: %w", err)
	}
	if strings.TrimSpace(credentials.APIKey) == "" {
		return nil, fmt.Errorf("sidecar credentials contain no api_key")
	}
	return &credentials, nil
}

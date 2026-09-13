package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// APIClient handles communication with AgentMaurice API.
type APIClient struct {
	baseURL    string
	httpClient *http.Client
}

// SelfRegisterRequest represents the request for self-registration.
type SelfRegisterRequest struct {
	// Deprecated: ignored and not sent to server anymore.
	PublicURL string            `json:"-"`
	PublicKey string            `json:"public_key,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// SelfRegisterResponse represents the response from self-registration.
type SelfRegisterResponse struct {
	MCPID        string `json:"mcp_id"`
	APIKey       string `json:"api_key"`
	TenantID     string `json:"tenant_id"`
	DeploymentID string `json:"deployment_id,omitempty"`
	MQTTBroker   string `json:"mqtt_broker,omitempty"`
}

// ValidateResponse represents the response from credential validation.
type ValidateResponse struct {
	Valid         bool `json:"valid"`
	RenewRequired bool `json:"renew_required"`
}

// RenewResponse represents the response from credential renewal.
type RenewResponse struct {
	APIKey string `json:"api_key"`
}

// ErrorResponse represents an error response from the API.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// NewAPIClient creates a new API client.
func NewAPIClient(baseURL string) *APIClient {
	return &APIClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SelfRegister performs initial registration with a bootstrap token.
func (c *APIClient) SelfRegister(ctx context.Context, bootstrapToken string, req *SelfRegisterRequest) (*SelfRegisterResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/mcp/self-register", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+bootstrapToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("registration failed (%d): %s", resp.StatusCode, errResp.Message)
		}
		return nil, fmt.Errorf("registration failed: %s", resp.Status)
	}

	var result SelfRegisterResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// ValidateCredentials validates the API key with the server.
func (c *APIClient) ValidateCredentials(ctx context.Context, apiKey string) (*ValidateResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/mcp/auth/validate", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// For validation, non-200 means invalid credentials
		return &ValidateResponse{Valid: false}, nil
	}

	var result ValidateResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// RenewCredentials renews the API key.
func (c *APIClient) RenewCredentials(ctx context.Context, oldAPIKey string) (*RenewResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/mcp/auth/renew", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+oldAPIKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("renewal failed (%d): %s", resp.StatusCode, errResp.Message)
		}
		return nil, fmt.Errorf("renewal failed: %s", resp.Status)
	}

	var result RenewResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

package business

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

type poolManagerClient struct {
	baseURL    string
	authToken  string
	clientID   string
	poolID     string
	httpClient *http.Client
	logger     *zap.Logger
}

type poolManagerAcquireRequest struct {
	SessionKey string `json:"session_key"`
	ClientID   string `json:"client_id,omitempty"`
}

type poolManagerAcquireResponse struct {
	PoolID          string `json:"pool_id,omitempty"`
	SessionKey      string `json:"session_key"`
	InstanceID      string `json:"instance_id"`
	Endpoint        string `json:"endpoint"`
	LeaseTTLSeconds int64  `json:"lease_ttl_seconds,omitempty"`
}

type poolManagerReleaseRequest struct {
	SessionKey string `json:"session_key"`
	ClientID   string `json:"client_id,omitempty"`
}

type poolManagerStatusResponse struct {
	PoolID          string               `json:"pool_id,omitempty"`
	Mode            string               `json:"mode"`
	SelectionPolicy string               `json:"selection_policy"`
	TotalInstances  int                  `json:"total_instances"`
	ActiveSessions  int                  `json:"active_sessions"`
	Instances       []poolInstanceStatus `json:"instances"`
}

func newPoolManagerClient(baseURL, authToken, clientID, poolID string, timeout time.Duration, logger *zap.Logger) *poolManagerClient {
	if logger == nil {
		logger = zap.NewNop()
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	trimmedPoolID := strings.TrimSpace(poolID)
	if trimmedPoolID == "" {
		trimmedPoolID = "default"
	}
	return &poolManagerClient{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		authToken:  strings.TrimSpace(authToken),
		clientID:   strings.TrimSpace(clientID),
		poolID:     trimmedPoolID,
		httpClient: &http.Client{Timeout: timeout},
		logger:     logger.Named("pool-manager-client"),
	}
}

func (c *poolManagerClient) Acquire(ctx context.Context, sessionKey string) (*poolManagerAcquireResponse, error) {
	payload := poolManagerAcquireRequest{
		SessionKey: sessionKey,
		ClientID:   c.clientID,
	}
	var response poolManagerAcquireResponse
	newPath := fmt.Sprintf("/v1/pools/%s/allocate-session", url.PathEscape(c.poolID))
	if err := c.doJSON(ctx, http.MethodPost, newPath, payload, &response); err != nil {
		if c.isNotFoundError(err) {
			legacyPath := "/v1/sessions/acquire"
			if legacyErr := c.doJSON(ctx, http.MethodPost, legacyPath, payload, &response); legacyErr != nil {
				return nil, legacyErr
			}
		} else {
			return nil, err
		}
	}
	if strings.TrimSpace(response.Endpoint) == "" {
		return nil, fmt.Errorf("pool-manager returned empty endpoint")
	}
	if strings.TrimSpace(response.InstanceID) == "" {
		response.InstanceID = response.Endpoint
	}
	if strings.TrimSpace(response.SessionKey) == "" {
		response.SessionKey = sessionKey
	}
	if strings.TrimSpace(response.PoolID) == "" {
		response.PoolID = c.poolID
	}
	return &response, nil
}

func (c *poolManagerClient) Release(ctx context.Context, sessionKey string) error {
	payload := poolManagerReleaseRequest{
		SessionKey: sessionKey,
		ClientID:   c.clientID,
	}
	newPath := fmt.Sprintf("/v1/pools/%s/release-session", url.PathEscape(c.poolID))
	err := c.doJSON(ctx, http.MethodPost, newPath, payload, nil)
	if err != nil && c.isNotFoundError(err) {
		return c.doJSON(ctx, http.MethodPost, "/v1/sessions/release", payload, nil)
	}
	return err
}

func (c *poolManagerClient) Status(ctx context.Context) (*poolManagerStatusResponse, error) {
	var response poolManagerStatusResponse
	newPath := fmt.Sprintf("/v1/pools/%s/status", url.PathEscape(c.poolID))
	err := c.doJSON(ctx, http.MethodGet, newPath, nil, &response)
	if err != nil {
		if c.isNotFoundError(err) {
			if legacyErr := c.doJSON(ctx, http.MethodGet, "/v1/pool/status", nil, &response); legacyErr != nil {
				return nil, legacyErr
			}
		} else {
			return nil, err
		}
	}
	if strings.TrimSpace(response.PoolID) == "" {
		response.PoolID = c.poolID
	}
	return &response, nil
}

func (c *poolManagerClient) doJSON(ctx context.Context, method, path string, payload any, out any) error {
	if c.baseURL == "" {
		return fmt.Errorf("pool-manager base URL is empty")
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal pool-manager payload: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("build pool-manager request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		request.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("pool-manager request failed: %w", err)
	}
	defer response.Body.Close()

	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		bodyText := strings.TrimSpace(string(responseBody))
		if bodyText == "" {
			bodyText = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("pool-manager returned %d: %s", response.StatusCode, bodyText)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("decode pool-manager response: %w", err)
	}
	return nil
}

func (c *poolManagerClient) isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(v, "returned 404")
}

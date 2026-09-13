// Package sidecar provides lightweight self-registration for MCP binaries
// running under the binary deployment provider. It uses stdlib only.
//
// When MCP binaries are spawned by the binary provider, they receive env vars:
//   - MCP_SIDECAR_BOOTSTRAP_TOKEN: token for registration
//   - MCP_SIDECAR_MAURICE_URL: AgentMaurice instance URL
//   - MCP_SIDECAR_METADATA: optional comma-separated key=value pairs
//
// If these env vars are not set, RegisterIfConfigured returns nil (standalone mode).
package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// selfRegisterRequest is the JSON body sent to Maurice /api/v1/mcp/self-register.
type selfRegisterRequest struct {
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SelfRegisterResponse is the JSON body returned by Maurice.
type SelfRegisterResponse struct {
	MCPID        string `json:"mcp_id"`
	APIKey       string `json:"api_key"`
	TenantID     string `json:"tenant_id"`
	DeploymentID string `json:"deployment_id,omitempty"`
	MQTTBroker   string `json:"mqtt_broker,omitempty"`
}

// RegisterIfConfigured checks for sidecar env vars and registers with Maurice.
// Returns nil if env vars are not set (standalone mode — no registration needed).
// Returns error if registration was attempted but failed.
func RegisterIfConfigured(ctx context.Context) (*SelfRegisterResponse, error) {
	bootstrapToken := strings.TrimSpace(os.Getenv("MCP_SIDECAR_BOOTSTRAP_TOKEN"))
	mauriceURL := strings.TrimSpace(os.Getenv("MCP_SIDECAR_MAURICE_URL"))

	if bootstrapToken == "" || mauriceURL == "" {
		// Standalone mode — no registration needed.
		return nil, nil
	}

	metadata := parseMetadata(os.Getenv("MCP_SIDECAR_METADATA"))

	return Register(ctx, mauriceURL, bootstrapToken, metadata)
}

// Register performs sidecar self-registration with the Maurice server.
func Register(ctx context.Context, mauriceURL, bootstrapToken string, metadata map[string]string) (*SelfRegisterResponse, error) {
	registerURL := strings.TrimSuffix(mauriceURL, "/") + "/api/v1/mcp/self-register"

	body, err := json.Marshal(selfRegisterRequest{Metadata: metadata})
	if err != nil {
		return nil, fmt.Errorf("marshal register request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registerURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build register request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bootstrapToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("register request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("register failed with status %d", resp.StatusCode)
	}

	var result SelfRegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}
	return &result, nil
}

// parseMetadata parses comma-separated key=value pairs.
func parseMetadata(raw string) map[string]string {
	m := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return m
}

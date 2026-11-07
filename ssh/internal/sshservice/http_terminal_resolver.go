package sshservice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// HTTPTerminalResolver asks the authenticated host channel to resolve a
// server-minted grant. It does not send a secret and it does not accept one
// from the model.
type HTTPTerminalResolver struct {
	BaseURL    string
	Token      string
	Grant      string
	HTTPClient *http.Client
}

func (r *HTTPTerminalResolver) ResolveForTerminal(ctx context.Context, canonicalRef string, _ Purpose) (string, error) {
	if r == nil || strings.TrimSpace(r.BaseURL) == "" || strings.TrimSpace(r.Token) == "" || strings.TrimSpace(r.Grant) == "" {
		return "", NewError("credentials_provider_unavailable", "no credential provider is configured")
	}
	if !deploymentSecretPattern.MatchString(canonicalRef) {
		return "", NewError("credentials_provider_unavailable", "SSH target binding is not canonical")
	}
	payload, err := json.Marshal(map[string]string{"grant": r.Grant, "reference": canonicalRef})
	if err != nil {
		return "", NewError("credentials_provider_unavailable", "deployment binding was refused")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.BaseURL, "/")+"/ssh-resolve", bytes.NewReader(payload))
	if err != nil {
		return "", NewError("credentials_provider_unavailable", "deployment binding was refused")
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", NewError("credentials_provider_unavailable", "deployment binding was refused")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", NewError("credentials_provider_unavailable", "deployment binding was refused")
	}
	var body struct {
		PrivateKey string `json:"private_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || strings.TrimSpace(body.PrivateKey) == "" {
		return "", NewError("credentials_provider_unavailable", "deployment binding was refused")
	}
	return body.PrivateKey, nil
}

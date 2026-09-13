package mcp

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/config"
)

func TestResolveBufferOptionsRequiresAuthTokenWhenEnabled(t *testing.T) {
	tool := NewScreenshotTool(nil, &config.BufferConfig{}, zap.NewNop())

	_, err := tool.resolveBufferOptions(map[string]interface{}{
		"buffer_options": map[string]interface{}{
			"enabled": true,
			"url":     "http://localhost:3001",
		},
	})
	if err == nil {
		t.Fatal("expected error for missing auth_token")
	}
}

func TestResolveBufferOptionsParsesValues(t *testing.T) {
	tool := NewScreenshotTool(nil, &config.BufferConfig{}, zap.NewNop())

	opts, err := tool.resolveBufferOptions(map[string]interface{}{
		"buffer_options": map[string]interface{}{
			"enabled":              true,
			"url":                  "http://localhost:3001",
			"auth_token":           "token-1",
			"namespace":            "browser",
			"soft_threshold_bytes": float64(1000),
			"hard_threshold_bytes": float64(2000),
			"max_upload_bytes":     float64(5000),
			"ttl_seconds":          float64(60),
			"network_retries":      float64(2),
			"timeout_ms":           float64(1500),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !opts.Enabled {
		t.Fatal("expected buffer enabled")
	}
	if opts.URL != "http://localhost:3001" {
		t.Fatalf("unexpected url: %s", opts.URL)
	}
	if opts.AuthToken != "token-1" {
		t.Fatalf("unexpected auth token: %s", opts.AuthToken)
	}
	if opts.SoftThresholdBytes != 1000 || opts.HardThresholdBytes != 2000 {
		t.Fatalf("unexpected thresholds: soft=%d hard=%d", opts.SoftThresholdBytes, opts.HardThresholdBytes)
	}
	if opts.Timeout != 1500*time.Millisecond {
		t.Fatalf("unexpected timeout: %v", opts.Timeout)
	}
}

package business

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/config"
)

func TestResolvePoolEndpointsInternal(t *testing.T) {
	cfg := &config.BrowserConfig{
		CDPEndpoint: "ws://127.0.0.1:9222",
		PoolMode:    "internal",
		PoolSize:    3,
	}

	endpoints, mode, err := resolvePoolEndpoints(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if mode != "internal" {
		t.Fatalf("expected internal mode, got %s", mode)
	}
	if len(endpoints) != 3 {
		t.Fatalf("expected 3 endpoints, got %d", len(endpoints))
	}
	if endpoints[0] != "ws://127.0.0.1:9222" || endpoints[1] != "ws://127.0.0.1:9223" || endpoints[2] != "ws://127.0.0.1:9224" {
		t.Fatalf("unexpected endpoints: %v", endpoints)
	}
}

func TestResolvePoolEndpointsExternalWithManager(t *testing.T) {
	cfg := &config.BrowserConfig{
		PoolMode:          "external",
		PoolManagerURL:    "http://pool-manager:8090",
		AcquireTimeout:    2 * time.Second,
		DefaultTimeout:    30 * time.Second,
		NavigationTimeout: 60 * time.Second,
	}

	endpoints, mode, err := resolvePoolEndpoints(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if mode != "external" {
		t.Fatalf("expected external mode, got %s", mode)
	}
	if len(endpoints) != 0 {
		t.Fatalf("expected no static endpoints when using pool manager, got %v", endpoints)
	}
}

func TestAcquireStickySession(t *testing.T) {
	cfg := &config.BrowserConfig{
		CDPEndpoint:        "ws://127.0.0.1:9222",
		PoolMode:           "internal",
		PoolSize:           2,
		SelectionPolicy:    "least_loaded",
		AcquireTimeout:     100 * time.Millisecond,
		MaxQueueDepth:      2,
		MaxSessionsPerInst: 2,
		SessionIdleTTL:     0,
		DefaultTimeout:     30 * time.Second,
		NavigationTimeout:  60 * time.Second,
		ScreenshotFormat:   "png",
		ScreenshotQuality:  80,
		AutoReconnect:      true,
		ReconnectRetries:   1,
	}

	pool, err := newBrowserPoolClient(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	t.Cleanup(func() { _ = pool.Disconnect() })

	_, firstInstance, err := pool.Acquire(context.Background(), "session-a")
	if err != nil {
		t.Fatalf("expected no error on first acquire, got %v", err)
	}

	_, secondInstance, err := pool.Acquire(context.Background(), "session-a")
	if err != nil {
		t.Fatalf("expected no error on sticky acquire, got %v", err)
	}

	if firstInstance != secondInstance {
		t.Fatalf("expected sticky session to reuse same instance, got %s and %s", firstInstance, secondInstance)
	}
}

func TestAcquireRespectsCapacity(t *testing.T) {
	cfg := &config.BrowserConfig{
		CDPEndpoint:        "ws://127.0.0.1:9222",
		PoolMode:           "single",
		PoolSize:           1,
		SelectionPolicy:    "least_loaded",
		AcquireTimeout:     20 * time.Millisecond,
		MaxQueueDepth:      1,
		MaxSessionsPerInst: 1,
		SessionIdleTTL:     0,
		DefaultTimeout:     30 * time.Second,
		NavigationTimeout:  60 * time.Second,
		ScreenshotFormat:   "png",
		ScreenshotQuality:  80,
		AutoReconnect:      true,
		ReconnectRetries:   1,
	}

	pool, err := newBrowserPoolClient(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	t.Cleanup(func() { _ = pool.Disconnect() })

	if _, _, err := pool.Acquire(context.Background(), "session-a"); err != nil {
		t.Fatalf("expected first acquire to succeed, got %v", err)
	}

	if _, _, err := pool.Acquire(context.Background(), "session-b"); err == nil {
		t.Fatalf("expected acquire to fail when pool capacity is exhausted")
	}
}

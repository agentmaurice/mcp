package business

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

func TestExecuteWithRecoveryRetriesOnRecoverableError(t *testing.T) {
	manager := &BrowserManager{
		config: &config.BrowserConfig{
			AutoReconnect:    true,
			ReconnectRetries: 1,
		},
		logger:  zap.NewNop(),
		running: true,
	}

	var ensureCount, reconnectCount, opCount int
	manager.ensureConnectedFn = func(context.Context) error {
		ensureCount++
		return nil
	}
	manager.reconnectFn = func(context.Context) error {
		reconnectCount++
		return nil
	}

	err := manager.executeWithRecovery(context.Background(), "navigate", func() error {
		opCount++
		if opCount == 1 {
			return shared.ErrBrowserAction("navigate", context.Canceled)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}

	if opCount != 2 {
		t.Fatalf("expected 2 operation attempts, got %d", opCount)
	}
	if reconnectCount != 1 {
		t.Fatalf("expected 1 reconnect attempt, got %d", reconnectCount)
	}
	if ensureCount != 2 {
		t.Fatalf("expected 2 ensure-connected calls, got %d", ensureCount)
	}
}

func TestExecuteWithRecoveryNoRetryWhenDisabled(t *testing.T) {
	manager := &BrowserManager{
		config: &config.BrowserConfig{
			AutoReconnect:    false,
			ReconnectRetries: 3,
		},
		logger:  zap.NewNop(),
		running: true,
	}

	var reconnectCount, opCount int
	manager.ensureConnectedFn = func(context.Context) error { return nil }
	manager.reconnectFn = func(context.Context) error {
		reconnectCount++
		return nil
	}

	err := manager.executeWithRecovery(context.Background(), "navigate", func() error {
		opCount++
		return shared.ErrBrowserAction("navigate", context.Canceled)
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if opCount != 1 {
		t.Fatalf("expected a single operation attempt, got %d", opCount)
	}
	if reconnectCount != 0 {
		t.Fatalf("expected no reconnect attempt, got %d", reconnectCount)
	}
}

func TestExecuteWithRecoveryNoRetryOnNonRecoverableError(t *testing.T) {
	manager := &BrowserManager{
		config: &config.BrowserConfig{
			AutoReconnect:    true,
			ReconnectRetries: 2,
		},
		logger:  zap.NewNop(),
		running: true,
	}

	var reconnectCount, opCount int
	manager.ensureConnectedFn = func(context.Context) error { return nil }
	manager.reconnectFn = func(context.Context) error {
		reconnectCount++
		return nil
	}

	expectedErr := shared.ErrValidation("invalid selector")
	err := manager.executeWithRecovery(context.Background(), "click", func() error {
		opCount++
		return expectedErr
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected %v, got %v", expectedErr, err)
	}
	if opCount != 1 {
		t.Fatalf("expected a single operation attempt, got %d", opCount)
	}
	if reconnectCount != 0 {
		t.Fatalf("expected no reconnect attempt, got %d", reconnectCount)
	}
}

func TestExecuteWithRecoveryRetriesOnPageLoadShutdown(t *testing.T) {
	manager := &BrowserManager{
		config: &config.BrowserConfig{
			AutoReconnect:    true,
			ReconnectRetries: 1,
		},
		logger:  zap.NewNop(),
		running: true,
		baseCtx: context.Background(),
	}

	var reconnectCount, opCount int
	manager.ensureConnectedFn = func(context.Context) error { return nil }
	manager.reconnectFn = func(context.Context) error {
		reconnectCount++
		return nil
	}

	err := manager.executeWithRecovery(context.Background(), "navigate", func() error {
		opCount++
		if opCount == 1 {
			return shared.ErrBrowserAction("navigate", errors.New("page load error Shutdown"))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after reconnect on Shutdown, got %v", err)
	}
	if opCount != 2 {
		t.Fatalf("expected 2 operation attempts, got %d", opCount)
	}
	if reconnectCount != 1 {
		t.Fatalf("expected 1 reconnect attempt, got %d", reconnectCount)
	}
}

func TestExecuteWithRecoveryHonorsRetryCount(t *testing.T) {
	manager := &BrowserManager{
		config: &config.BrowserConfig{
			AutoReconnect:    true,
			ReconnectRetries: 2,
		},
		logger:  zap.NewNop(),
		running: true,
	}

	var reconnectCount, opCount int
	manager.ensureConnectedFn = func(context.Context) error { return nil }
	manager.reconnectFn = func(context.Context) error {
		reconnectCount++
		return nil
	}

	err := manager.executeWithRecovery(context.Background(), "navigate", func() error {
		opCount++
		return shared.ErrBrowserAction("navigate", context.Canceled)
	})
	if err == nil {
		t.Fatalf("expected error after retries are exhausted")
	}
	if opCount != 3 {
		t.Fatalf("expected 3 operation attempts, got %d", opCount)
	}
	if reconnectCount != 2 {
		t.Fatalf("expected 2 reconnect attempts, got %d", reconnectCount)
	}
}

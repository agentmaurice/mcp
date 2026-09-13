package browser

import (
	"context"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

func TestIsConnectedFalseWhenSessionContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &Client{
		connected: true,
		ctx:       ctx,
	}

	if client.IsConnected() {
		t.Fatalf("expected IsConnected to be false when session context is cancelled")
	}
}

func TestIsConnectedTrueWhenSessionContextAlive(t *testing.T) {
	client := &Client{
		connected: true,
		ctx:       context.Background(),
	}

	if !client.IsConnected() {
		t.Fatalf("expected IsConnected to be true when session context is alive")
	}
}

func TestRequireSessionLockedRejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &Client{
		connected: true,
		ctx:       ctx,
	}

	err := client.requireSessionLocked()
	if err == nil {
		t.Fatal("expected unavailable error for cancelled session")
	}
	if !shared.IsTransientBrowserError(err) {
		t.Fatalf("expected transient unavailable error, got %v", err)
	}
}


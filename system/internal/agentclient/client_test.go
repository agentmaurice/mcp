package agentclient

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHealthReportsLostSocket(t *testing.T) {
	client := New("/tmp/agentmaurice-system-socket-that-does-not-exist", 50*time.Millisecond, Identity{Actor: "test"})
	_, err := client.Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "host agent unavailable") {
		t.Fatalf("expected unavailable host agent, got %v", err)
	}
}

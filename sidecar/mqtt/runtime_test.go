package mqtt

import (
	"encoding/json"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"go.uber.org/zap"
)

func TestTopicsUseAMContract(t *testing.T) {
	cfg := config.NewDefaultConfig()
	r := NewRuntime(cfg, nil, zap.NewNop())
	r.serverID = "mcp-123"
	r.tenantID = "dep-456"

	if got, want := r.cmdTopic(), "am/dep-456/mcp-123/cmd"; got != want {
		t.Fatalf("cmd topic = %q, want %q", got, want)
	}
	if got, want := r.evtTopic(), "am/dep-456/mcp-123/evt"; got != want {
		t.Fatalf("evt topic = %q, want %q", got, want)
	}
}

func TestValidateCommandAllowlist(t *testing.T) {
	cfg := config.NewDefaultConfig()
	r := NewRuntime(cfg, nil, zap.NewNop())

	valid := &commandEnvelope{
		V:      protocolVersion,
		ID:     "cmd-1",
		Type:   "rpc",
		Method: "tools/list",
	}
	if err := r.validateCommand(valid); err != nil {
		t.Fatalf("expected valid command, got error: %v", err)
	}

	invalid := &commandEnvelope{
		V:      protocolVersion,
		ID:     "cmd-2",
		Type:   "rpc",
		Method: "tools/delete_everything",
	}
	if err := r.validateCommand(invalid); err == nil {
		t.Fatal("expected unsupported method validation error")
	}
}

func TestDecodeCommandRespectsDecompressedLimit(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.MQTTMaxDecompressedBytes = 32
	r := NewRuntime(cfg, nil, zap.NewNop())

	cmd := commandEnvelope{
		V:      protocolVersion,
		ID:     "cmd-1",
		Type:   "rpc",
		Method: "tools/list",
		Params: json.RawMessage(`{"a":"01234567890123456789012345678901234567890"}`),
	}
	body, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	compressed, err := compressPayload(body)
	if err != nil {
		t.Fatalf("compress command: %v", err)
	}

	if _, err := r.decodeCommand(compressed); err == nil {
		t.Fatal("expected error for decompressed payload limit")
	}
}

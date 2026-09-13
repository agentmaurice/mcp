package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/ssh/internal/sshservice"
	"github.com/mark3labs/mcp-go/mcp"
)

type fakeAPI struct {
	opened bool
}

func (f *fakeAPI) Health(context.Context) (sshservice.Health, error) {
	return sshservice.Health{Status: "ready"}, nil
}
func (f *fakeAPI) Capabilities(context.Context) (sshservice.Capabilities, error) {
	return sshservice.Capabilities{}, nil
}
func (f *fakeAPI) Targets(context.Context) (sshservice.Targets, error) {
	return sshservice.Targets{}, nil
}
func (f *fakeAPI) Check(context.Context, string, time.Duration) (sshservice.CheckResult, error) {
	return sshservice.CheckResult{Reachable: true}, nil
}
func (f *fakeAPI) Open(context.Context, string, time.Duration) (sshservice.SessionView, error) {
	f.opened = true
	return sshservice.SessionView{SessionID: "fixture"}, nil
}
func (f *fakeAPI) Exec(context.Context, string, string, string, time.Duration, int) (sshservice.ExecResult, error) {
	return sshservice.ExecResult{ExitCode: 0}, nil
}
func (f *fakeAPI) Status(context.Context, string) (sshservice.SessionView, error) {
	return sshservice.SessionView{Status: "open"}, nil
}
func (f *fakeAPI) CloseSession(context.Context, string) (sshservice.SessionView, error) {
	return sshservice.SessionView{Status: "closed"}, nil
}

func TestToolSurfaceIsExactAndAnnotated(t *testing.T) {
	server := New(&fakeAPI{})
	tools := server.Legacy().ListTools()
	expected := []string{
		"ssh_health_v1", "ssh_capabilities_v1", "ssh_targets_v1", "ssh_target_check_v1",
		"ssh_session_open_v1", "ssh_exec_v1", "ssh_session_status_v1", "ssh_session_close_v1",
	}
	if len(tools) != len(expected) {
		t.Fatalf("expected %d tools, got %d", len(expected), len(tools))
	}
	for _, name := range expected {
		tool := tools[name]
		if tool == nil {
			t.Fatalf("missing tool %s", name)
		}
		if tool.Tool.Annotations.ReadOnlyHint == nil || tool.Tool.Annotations.DestructiveHint == nil || tool.Tool.Annotations.IdempotentHint == nil {
			t.Fatalf("tool %s has incomplete annotations", name)
		}
	}
	if value := tools["ssh_exec_v1"].Tool.Annotations.DestructiveHint; value == nil || !*value {
		t.Fatal("ssh_exec_v1 must be destructive")
	}
}

func TestOpenRejectsTargetOverrideBeforeCallingService(t *testing.T) {
	api := &fakeAPI{}
	tool := New(api).Legacy().GetTool("ssh_session_open_v1")
	result, err := tool.Handler(context.Background(), request(map[string]any{
		"target_id": "prod",
		"host":      "attacker.invalid",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || api.opened {
		t.Fatalf("override was not rejected: %#v", result.StructuredContent)
	}
	content := result.StructuredContent.(map[string]any)
	if content["error"] != "ssh_target_override_forbidden" {
		t.Fatalf("unexpected error: %#v", content)
	}
}

func TestOpenRejectsUnknownArgumentsAndAcceptsTargetID(t *testing.T) {
	api := &fakeAPI{}
	tool := New(api).Legacy().GetTool("ssh_session_open_v1")
	result, err := tool.Handler(context.Background(), request(map[string]any{"target_id": "prod", "unknown": true}))
	if err != nil || !result.IsError || api.opened {
		t.Fatalf("unknown argument was not rejected: %#v %v", result, err)
	}
	result, err = tool.Handler(context.Background(), request(map[string]any{"target_id": "prod", "connect_timeout_seconds": 5}))
	if err != nil || result.IsError || !api.opened {
		t.Fatalf("valid open failed: %#v %v", result, err)
	}
}

func request(arguments map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: arguments}}
}

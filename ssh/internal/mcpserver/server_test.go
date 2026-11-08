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

func (f *fakeAPI) Upload(context.Context, string, string, []byte, bool, string) (sshservice.UploadResult, error) {
	return sshservice.UploadResult{Status: "ok"}, nil
}
func (f *fakeAPI) Remove(context.Context, string, string) (sshservice.RemoveResult, error) {
	return sshservice.RemoveResult{Status: "ok"}, nil
}
func (f *fakeAPI) List(context.Context, string, string) (sshservice.ListResult, error) {
	return sshservice.ListResult{}, nil
}

func TestToolSurfaceIsExactAndAnnotated(t *testing.T) {
	server := New(&fakeAPI{})
	tools := server.Legacy().ListTools()
	expected := []string{
		"ssh_health_v1", "ssh_capabilities_v1", "ssh_targets_v1", "ssh_target_check_v1",
		"ssh_session_open_v1", "ssh_exec_v1", "ssh_session_status_v1", "ssh_session_close_v1",
		"ssh_upload_v1", "ssh_remove_v1", "ssh_list_v1",
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

func TestUploadInputHandling(t *testing.T) {
	tool := New(&fakeAPI{}).Legacy().GetTool("ssh_upload_v1")
	code := func(args map[string]any) string {
		result, err := tool.Handler(context.Background(), request(args))
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			return ""
		}
		return result.StructuredContent.(map[string]any)["error"].(string)
	}
	if got := code(map[string]any{"target": "web", "path": "a.txt", "file": map[string]any{"content_base64": "aGk="}}); got != "" {
		t.Fatalf("valid upload failed: %s", got)
	}
	if got := code(map[string]any{"target": "web", "path": "a.txt", "file": map[string]any{"ref": "buffer://x"}}); got != "buffer_unavailable" {
		t.Fatalf("expected buffer_unavailable, got %s", got)
	}
	if got := code(map[string]any{"target": "web", "path": "a.txt", "file": map[string]any{"content_base64": "!!"}}); got != "invalid_arguments" {
		t.Fatalf("expected invalid_arguments, got %s", got)
	}
	if got := code(map[string]any{"target": "web", "path": "a.txt", "file": map[string]any{}}); got != "invalid_arguments" {
		t.Fatalf("expected invalid_arguments, got %s", got)
	}
	if got := code(map[string]any{"target": "web", "path": "a.txt", "host": "evil", "file": map[string]any{"content_base64": "aGk="}}); got != "ssh_target_override_forbidden" {
		t.Fatalf("expected override refusal, got %s", got)
	}
}

func TestInjectedContextIsIgnoredByEveryTool(t *testing.T) {
	server := New(&fakeAPI{})
	context1 := map[string]any{"deployment_id": "d", "organization_id": "o", "user_id": "u", "space_id": "s", "session_id": "ctx"}
	valid := map[string]map[string]any{
		"ssh_target_check_v1":   {"target_id": "prod"},
		"ssh_session_open_v1":   {"target_id": "prod"},
		"ssh_exec_v1":           {"session_id": "ssh_sess_x", "command": "true"},
		"ssh_session_status_v1": {"session_id": "ssh_sess_x"},
		"ssh_session_close_v1":  {"session_id": "ssh_sess_x"},
		"ssh_upload_v1":         {"target": "web", "path": "a.txt", "file": map[string]any{"content_base64": "aGk="}},
		"ssh_remove_v1":         {"target": "web", "path": "a.txt"},
		"ssh_list_v1":           {"target": "web"},
	}
	for name, args := range valid {
		tool := server.Legacy().GetTool(name)
		merged := map[string]any{}
		for k, v := range context1 {
			merged[k] = v
		}
		for k, v := range args {
			merged[k] = v
		}
		result, err := tool.Handler(context.Background(), request(merged))
		if err != nil || result.IsError {
			t.Fatalf("%s rejected injected context: %#v %v", name, result.StructuredContent, err)
		}
		args2 := map[string]any{"bogus_field": 1}
		for k, v := range args {
			args2[k] = v
		}
		result, err = tool.Handler(context.Background(), request(args2))
		if err != nil || !result.IsError || result.StructuredContent.(map[string]any)["error"] != "invalid_arguments" {
			t.Fatalf("%s accepted an unknown field: %#v", name, result.StructuredContent)
		}
	}
}

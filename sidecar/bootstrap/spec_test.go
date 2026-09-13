package bootstrap

import "testing"

func TestParseRuntimeSpecValid(t *testing.T) {
	raw := `{
		"runtime_kind":"python",
		"source":{"repo_url":"https://github.com/acme/example-mcp","ref":"v1.2.3","subdir":"server"},
		"start_command":"python -m server.main",
		"env":{"LOG_LEVEL":"info"}
	}`
	spec, err := ParseRuntimeSpec(raw)
	if err != nil {
		t.Fatalf("expected valid runtime spec, got error: %v", err)
	}
	if spec == nil {
		t.Fatal("expected non-nil runtime spec")
	}
	if spec.RuntimeKind != "python" {
		t.Fatalf("runtime_kind=%q", spec.RuntimeKind)
	}
	if spec.Source.Subdir != "server" {
		t.Fatalf("subdir=%q", spec.Source.Subdir)
	}
}

func TestParseRuntimeSpecRejectsBranchRef(t *testing.T) {
	raw := `{
		"runtime_kind":"node",
		"source":{"repo_url":"https://github.com/acme/example-mcp","ref":"refs/heads/main"},
		"start_command":"npm run start:mcp"
	}`
	_, err := ParseRuntimeSpec(raw)
	if err == nil {
		t.Fatal("expected branch ref to be rejected")
	}
}

func TestParseRuntimeSpecRejectsInvalidEnvKey(t *testing.T) {
	raw := `{
		"runtime_kind":"go",
		"source":{"repo_url":"https://github.com/acme/example-mcp","ref":"0123456789abcdef"},
		"start_command":"go run .",
		"env":{"bad-key":"x"}
	}`
	_, err := ParseRuntimeSpec(raw)
	if err == nil {
		t.Fatal("expected invalid env key to be rejected")
	}
}

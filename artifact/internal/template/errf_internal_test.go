package template

import "testing"

// The catalogue test (internal/mcpserver) checks the codes at every call of errf. This test checks the other half: errf
// emits exactly the code it is given, so a constructor that rewrites its code cannot pass unnoticed (AGENT-1789).
func TestErrfKeepsTheCodeItIsGiven(t *testing.T) {
	for _, code := range []string{"probe_code_a", CodeReservedNamespace, ""} {
		if got := errf(code, "m %d", 1); got.Code != code || got.Message != "m 1" {
			t.Fatalf("errf(%q) = {%q, %q}, want the same code and the formatted message", code, got.Code, got.Message)
		}
	}
}

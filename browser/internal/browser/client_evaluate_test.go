package browser

import "testing"

func TestBuildEvaluateScriptCandidates_ReturnScriptPrefersStatementWrapper(t *testing.T) {
	script := "if (true) { return 'ok'; }"

	candidates := buildEvaluateScriptCandidates(script)
	if len(candidates) < 3 {
		t.Fatalf("expected at least 3 candidates, got %d", len(candidates))
	}

	if candidates[0] != script {
		t.Fatalf("expected raw script as first candidate")
	}

	if candidates[1] != "(function(){\nif (true) { return 'ok'; }\n})()" {
		t.Fatalf("expected statement wrapper as second candidate, got %q", candidates[1])
	}
}

func TestBuildEvaluateScriptCandidates_ExpressionPrefersExpressionWrapper(t *testing.T) {
	script := "document.title"

	candidates := buildEvaluateScriptCandidates(script)
	if len(candidates) < 3 {
		t.Fatalf("expected at least 3 candidates, got %d", len(candidates))
	}

	if candidates[0] != script {
		t.Fatalf("expected raw script as first candidate")
	}

	if candidates[1] != "(function(){ return (document.title); })()" {
		t.Fatalf("expected expression wrapper as second candidate, got %q", candidates[1])
	}
}

func TestBuildEvaluateScriptCandidates_DedupesCandidates(t *testing.T) {
	candidates := buildEvaluateScriptCandidates("(function(){ return (document.title); })()")

	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			t.Fatalf("found duplicate candidate %q", candidate)
		}
		seen[candidate] = struct{}{}
	}
}


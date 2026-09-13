package browser

import "testing"

func TestParseContainsSelectorPatterns_Valid(t *testing.T) {
	patterns, hasContains, err := parseContainsSelectorPatterns(`a:contains('hello'), button.primary`)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !hasContains {
		t.Fatalf("expected hasContains=true")
	}
	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(patterns))
	}
	if patterns[0].Base != "a" || patterns[0].Text != "hello" {
		t.Fatalf("unexpected first pattern: %+v", patterns[0])
	}
	if patterns[1].Base != "button.primary" || patterns[1].Text != "" {
		t.Fatalf("unexpected second pattern: %+v", patterns[1])
	}
}

func TestParseContainsSelectorPatterns_ApostropheUnescaped(t *testing.T) {
	patterns, hasContains, err := parseContainsSelectorPatterns(`a:contains('s\'inscrire à la bêta')`)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !hasContains || len(patterns) != 1 {
		t.Fatalf("unexpected parse result: hasContains=%v patterns=%d", hasContains, len(patterns))
	}
	if patterns[0].Text != "s'inscrire à la bêta" {
		t.Fatalf("unexpected contains text: %q", patterns[0].Text)
	}
}

func TestParseContainsSelectorPatterns_NoContains(t *testing.T) {
	patterns, hasContains, err := parseContainsSelectorPatterns(`button.primary`)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if hasContains {
		t.Fatalf("expected hasContains=false")
	}
	if patterns != nil {
		t.Fatalf("expected nil patterns when no contains selector is provided")
	}
}

func TestParseContainsSelectorPatterns_InvalidContains(t *testing.T) {
	_, hasContains, err := parseContainsSelectorPatterns(`a:contains(test)`)
	if !hasContains {
		t.Fatalf("expected hasContains=true")
	}
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

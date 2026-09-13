package security

import (
	"encoding/json"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
)

func TestRedactResult_ColumnMatch(t *testing.T) {
	columns := []shared.Column{{Name: "name"}, {Name: "email"}, {Name: "phone"}}
	rows := [][]any{
		{"Alice", "alice@example.com", "+33612345678"},
		{"Bob", "bob@example.com", "+33698765432"},
	}
	RedactResult(columns, rows, []string{"email", "phone"}, "***")

	for i, row := range rows {
		if row[0] == "***" {
			t.Errorf("row %d: name should not be redacted", i)
		}
		if row[1] != "***" {
			t.Errorf("row %d: email should be redacted, got: %v", i, row[1])
		}
		if row[2] != "***" {
			t.Errorf("row %d: phone should be redacted, got: %v", i, row[2])
		}
	}
}

func TestRedactResult_CaseInsensitive(t *testing.T) {
	columns := []shared.Column{{Name: "Email"}, {Name: "PHONE"}}
	rows := [][]any{{"a@b.c", "123"}}
	RedactResult(columns, rows, []string{"email", "phone"}, "***")
	if rows[0][0] != "***" || rows[0][1] != "***" {
		t.Fatalf("case insensitive redaction failed: %v", rows[0])
	}
}

func TestRedactResult_JSONNestedKeys(t *testing.T) {
	attrs := map[string]any{"email": "secret@test.com", "city": "Paris"}
	jsonStr, _ := json.Marshal(attrs)

	columns := []shared.Column{{Name: "attributes"}}
	rows := [][]any{{string(jsonStr)}}
	RedactResult(columns, rows, []string{"email"}, "***")

	var result map[string]any
	if err := json.Unmarshal([]byte(rows[0][0].(string)), &result); err != nil {
		t.Fatalf("invalid JSON after redaction: %v", err)
	}
	if result["email"] != "***" {
		t.Fatalf("expected nested email redacted, got: %v", result["email"])
	}
	if result["city"] != "Paris" {
		t.Fatalf("expected city preserved, got: %v", result["city"])
	}
}

func TestRedactResult_EmptyInputs(t *testing.T) {
	// Should not panic on empty inputs
	RedactResult(nil, nil, []string{"email"}, "***")
	RedactResult([]shared.Column{}, [][]any{}, []string{"email"}, "***")
	RedactResult([]shared.Column{{Name: "a"}}, [][]any{{"x"}}, nil, "***")
	RedactResult([]shared.Column{{Name: "a"}}, [][]any{{"x"}}, []string{}, "***")
}

func TestRedactResult_DeeplyNested(t *testing.T) {
	attrs := map[string]any{
		"contact": map[string]any{
			"email": "deep@test.com",
			"address": map[string]any{
				"password": "secret123",
			},
		},
	}
	jsonStr, _ := json.Marshal(attrs)

	columns := []shared.Column{{Name: "data"}}
	rows := [][]any{{string(jsonStr)}}
	RedactResult(columns, rows, []string{"email", "password"}, "***")

	var result map[string]any
	json.Unmarshal([]byte(rows[0][0].(string)), &result)

	contact := result["contact"].(map[string]any)
	if contact["email"] != "***" {
		t.Fatalf("expected deep email redacted, got: %v", contact["email"])
	}
	address := contact["address"].(map[string]any)
	if address["password"] != "***" {
		t.Fatalf("expected deep password redacted, got: %v", address["password"])
	}
}

// --- Policy Rules ---

func TestApplyPolicyRules_Redact(t *testing.T) {
	columns := []shared.Column{{Name: "name"}, {Name: "email"}}
	rows := [][]any{{"Alice", "alice@test.com"}}
	rules := []PolicyRule{{Column: "email", Action: ActionRedact}}
	ApplyPolicyRules(columns, rows, rules, "***")

	if rows[0][1] != "***" {
		t.Fatalf("expected email redacted, got: %v", rows[0][1])
	}
	if rows[0][0] != "Alice" {
		t.Fatalf("expected name preserved, got: %v", rows[0][0])
	}
}

func TestApplyPolicyRules_Hash(t *testing.T) {
	columns := []shared.Column{{Name: "email"}}
	rows := [][]any{{"alice@test.com"}}
	rules := []PolicyRule{{Column: "email", Action: ActionHash}}
	ApplyPolicyRules(columns, rows, rules, "***")

	if rows[0][0] == "alice@test.com" || rows[0][0] == "***" {
		t.Fatalf("expected email hashed, got: %v", rows[0][0])
	}
	// Should be a hex string of length 64 (sha256)
	if len(rows[0][0].(string)) != 64 {
		t.Fatalf("expected 64-char hex hash, got: %v", rows[0][0])
	}
}

func TestApplyPolicyRules_Drop(t *testing.T) {
	columns := []shared.Column{{Name: "email"}}
	rows := [][]any{{"alice@test.com"}}
	rules := []PolicyRule{{Column: "email", Action: ActionDrop}}
	ApplyPolicyRules(columns, rows, rules, "***")

	if rows[0][0] != nil {
		t.Fatalf("expected email dropped (nil), got: %v", rows[0][0])
	}
}

func TestApplyPolicyRules_JSONKeyPath(t *testing.T) {
	attrs := map[string]any{"email": "test@test.com", "city": "Paris"}
	jsonStr, _ := json.Marshal(attrs)

	columns := []shared.Column{{Name: "attributes"}}
	rows := [][]any{{string(jsonStr)}}
	rules := []PolicyRule{{Column: "attributes", Key: "email", Action: ActionRedact}}
	ApplyPolicyRules(columns, rows, rules, "***")

	var result map[string]any
	json.Unmarshal([]byte(rows[0][0].(string)), &result)
	if result["email"] != "***" {
		t.Fatalf("expected email in JSON redacted, got: %v", result["email"])
	}
	if result["city"] != "Paris" {
		t.Fatalf("expected city preserved, got: %v", result["city"])
	}
}

func TestParsePolicyRule_Variants(t *testing.T) {
	tests := []struct {
		path   string
		action string
		col    string
		key    string
		valid  bool
	}{
		{"email", "redact", "email", "", true},
		{"entities.name", "drop", "name", "", true},
		{"entities.attributes.email", "hash", "attributes", "email", true},
		{"entities.attributes.contact.email", "redact", "attributes", "contact.email", true},
		{"", "redact", "", "", false},
		{"email", "invalid_action", "", "", false},
		{"email", "", "email", "", true}, // default to redact
	}
	for _, tc := range tests {
		rule, ok := ParsePolicyRule(tc.path, tc.action)
		if ok != tc.valid {
			t.Errorf("ParsePolicyRule(%q, %q): expected valid=%v, got %v", tc.path, tc.action, tc.valid, ok)
			continue
		}
		if !ok {
			continue
		}
		if rule.Column != tc.col {
			t.Errorf("ParsePolicyRule(%q, %q): expected column=%q, got %q", tc.path, tc.action, tc.col, rule.Column)
		}
		if rule.Key != tc.key {
			t.Errorf("ParsePolicyRule(%q, %q): expected key=%q, got %q", tc.path, tc.action, tc.key, rule.Key)
		}
	}
}

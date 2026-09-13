package security

import (
	"testing"
)

func TestRedactMapKeys_Basic(t *testing.T) {
	obj := map[string]any{
		"name":  "Alice",
		"email": "alice@example.com",
		"phone": "+33612345678",
		"city":  "Paris",
	}
	RedactMapKeys(obj, []string{"email", "phone"}, "***")

	if obj["name"] != "Alice" {
		t.Fatalf("name should not be redacted: %v", obj["name"])
	}
	if obj["email"] != "***" {
		t.Fatalf("email should be redacted: %v", obj["email"])
	}
	if obj["phone"] != "***" {
		t.Fatalf("phone should be redacted: %v", obj["phone"])
	}
	if obj["city"] != "Paris" {
		t.Fatalf("city should not be redacted: %v", obj["city"])
	}
}

func TestRedactMapKeys_CaseInsensitive(t *testing.T) {
	obj := map[string]any{
		"Email": "test@test.com",
		"PHONE": "123",
	}
	RedactMapKeys(obj, []string{"email", "phone"}, "***")

	if obj["Email"] != "***" {
		t.Fatalf("Email should be redacted: %v", obj["Email"])
	}
	if obj["PHONE"] != "***" {
		t.Fatalf("PHONE should be redacted: %v", obj["PHONE"])
	}
}

func TestRedactMapKeys_Nested(t *testing.T) {
	obj := map[string]any{
		"name": "Alice",
		"attributes": map[string]any{
			"email": "alice@example.com",
			"city":  "Paris",
			"contact": map[string]any{
				"phone": "+33612345678",
				"fax":   "123456",
			},
		},
	}
	RedactMapKeys(obj, []string{"email", "phone"}, "***")

	attrs := obj["attributes"].(map[string]any)
	if attrs["email"] != "***" {
		t.Fatalf("nested email should be redacted: %v", attrs["email"])
	}
	if attrs["city"] != "Paris" {
		t.Fatalf("city should not be redacted: %v", attrs["city"])
	}
	contact := attrs["contact"].(map[string]any)
	if contact["phone"] != "***" {
		t.Fatalf("deeply nested phone should be redacted: %v", contact["phone"])
	}
	if contact["fax"] != "123456" {
		t.Fatalf("fax should not be redacted: %v", contact["fax"])
	}
}

func TestRedactMapKeys_Array(t *testing.T) {
	obj := map[string]any{
		"entities": []any{
			map[string]any{"name": "Alice", "email": "alice@test.com"},
			map[string]any{"name": "Bob", "email": "bob@test.com"},
		},
	}
	RedactMapKeys(obj, []string{"email"}, "***")

	entities := obj["entities"].([]any)
	for i, e := range entities {
		m := e.(map[string]any)
		if m["email"] != "***" {
			t.Fatalf("entity %d email should be redacted: %v", i, m["email"])
		}
	}
}

func TestRedactMapKeys_EmptyInputs(t *testing.T) {
	// Should not panic
	RedactMapKeys(nil, []string{"email"}, "***")
	RedactMapKeys(map[string]any{}, []string{"email"}, "***")
	RedactMapKeys(map[string]any{"a": 1}, nil, "***")
	RedactMapKeys(map[string]any{"a": 1}, []string{}, "***")
}

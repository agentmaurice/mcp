package mcpserver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/mistral"
)

func TestParseConversation(t *testing.T) {
	raw := []any{
		map[string]any{
			"role": "system",
			"text": "Moderation rules.",
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "Hello",
				},
			},
		},
	}

	messages, err := parseConversation(raw)
	if err != nil {
		t.Fatalf("parseConversation returned unexpected error: %v", err)
	}

	expected := []mistral.ChatMessage{
		{
			Role: "system",
			Content: []mistral.ChatContentItem{
				{Type: "text", Text: "Moderation rules."},
			},
		},
		{
			Role: "user",
			Content: []mistral.ChatContentItem{
				{Type: "text", Text: "Hello"},
			},
		},
	}

	if !reflect.DeepEqual(expected, messages) {
		t.Fatalf("unexpected messages: %#v", messages)
	}
}

func TestParseConversationInvalid(t *testing.T) {
	_, err := parseConversation("not-an-array")
	if err == nil {
		t.Fatal("expected error when conversation is not an array")
	}
}

func TestBuildToolResponseAlwaysIncludesFlaggedCategories(t *testing.T) {
	// Test with empty results - flagged_categories should still be present
	response := &mistral.ChatModerationResponse{
		ID:    "test-id",
		Model: "mistral-moderation-latest",
		Results: []mistral.ChatModerationResult{
			{
				Flagged:        false,
				Blocked:        false,
				Categories:     map[string]bool{"violence_and_threats": false, "hate_and_discrimination": false},
				CategoryScores: map[string]float64{"violence_and_threats": 0.01, "hate_and_discrimination": 0.02},
			},
		},
	}

	result := buildToolResponse(response)

	// FlaggedCategories should be an empty slice, not nil
	if result.FlaggedCategories == nil {
		t.Fatal("FlaggedCategories should not be nil, it should be an empty slice")
	}
	if len(result.FlaggedCategories) != 0 {
		t.Fatalf("Expected empty FlaggedCategories, got: %v", result.FlaggedCategories)
	}

	// Verify JSON marshaling includes the field
	jsonData, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	// Ensure flagged_categories is present in JSON output
	if !strings.Contains(string(jsonData), `"flagged_categories"`) {
		t.Fatalf("JSON output missing flagged_categories field: %s", string(jsonData))
	}
}

func TestBuildToolResponseWithFlaggedContent(t *testing.T) {
	response := &mistral.ChatModerationResponse{
		ID:    "test-flagged",
		Model: "mistral-moderation-latest",
		Results: []mistral.ChatModerationResult{
			{
				Flagged:        true,
				Blocked:        false,
				Categories:     map[string]bool{"violence_and_threats": true, "hate_and_discrimination": false},
				CategoryScores: map[string]float64{"violence_and_threats": 0.95, "hate_and_discrimination": 0.02},
			},
		},
	}

	result := buildToolResponse(response)

	if !result.Flagged {
		t.Fatal("Expected Flagged to be true")
	}
	if len(result.FlaggedCategories) != 1 {
		t.Fatalf("Expected 1 flagged category, got: %d", len(result.FlaggedCategories))
	}
	if result.FlaggedCategories[0] != "violence_and_threats" {
		t.Fatalf("Expected 'violence_and_threats' category, got: %s", result.FlaggedCategories[0])
	}
}

package mistral

import "encoding/json"

const chatModerationsPath = "/v1/chat/moderations"

// ChatContentItem represents a piece of content included in a moderation request.
type ChatContentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ChatMessage models a single conversational turn for the moderation endpoint.
type ChatMessage struct {
	Role    string            `json:"role"`
	Content []ChatContentItem `json:"content,omitempty"`
	Text    string            `json:"text,omitempty"`
}

// ChatModerationRequest is the payload sent to Mistral's chat moderation API.
type ChatModerationRequest struct {
	Model string        `json:"model"`
	Input []ChatMessage `json:"input"`
}

// ChatModerationResponse mirrors the structure returned by the API.
type ChatModerationResponse struct {
	ID      string                 `json:"id"`
	Model   string                 `json:"model"`
	Results []ChatModerationResult `json:"results"`
	Meta    map[string]any         `json:"meta,omitempty"`
	Raw     json.RawMessage        `json:"-"`
}

// ChatModerationResult captures a single evaluation result from the API.
type ChatModerationResult struct {
	Flagged         bool               `json:"flagged"`
	Categories      map[string]bool    `json:"categories"`
	CategoryScores  map[string]float64 `json:"category_scores"`
	Blocked         bool               `json:"blocked"`
	SensitiveTopics map[string]bool    `json:"sensitive_topics,omitempty"`
	Details         map[string]any     `json:"details,omitempty"`
	Raw             json.RawMessage    `json:"-"`
}

// APIError represents an error payload coming from Mistral.
type APIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// APIErrorResponse wraps the standard error response envelope.
type APIErrorResponse struct {
	Error APIError `json:"error"`
}

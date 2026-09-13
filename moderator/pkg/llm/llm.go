package llm

import (
	"context"
	"strings"
)

// Message represents a conversational message exchanged with an LLM.
type Message interface {
	GetRole() string
	GetContent() string
	GetToolCalls() []ToolCall
	IsToolResponse() bool
	GetToolResponseID() string
	GetUsage() (input int, output int)
}

// ToolCall captures the invocation of a tool by an LLM.
type ToolCall interface {
	GetName() string
	GetArguments() map[string]interface{}
	GetID() string
}

// Tool matches the structure used across the AgentMaurice server to describe MCP tools.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema Schema `json:"input_schema"`
}

// SkillDescriptor provides prompt-time hints about available skills.
type SkillDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Schema expresses JSON schema fragments for tool inputs.
type Schema struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Required   []string               `json:"required"`
}

// Provider defines the interface required for interacting with an LLM backend.
type Provider interface {
	CreateMessage(ctx context.Context, prompt string, messages []Message, tools []Tool, skills []SkillDescriptor) (Message, string, error)
	CreateToolResponse(toolCallID string, content interface{}) (Message, error)
	SupportsTools() bool
	Name() string
}

// ComposeSkillPrompt mirrors the AgentMaurice server helper to append skills to prompts.
func ComposeSkillPrompt(prompt string, skills []SkillDescriptor) string {
	if len(skills) == 0 {
		return prompt
	}

	var builder strings.Builder
	if trimmed := strings.TrimSpace(prompt); trimmed != "" {
		builder.WriteString(trimmed)
		builder.WriteString("\n\n")
	}
	builder.WriteString("You have access to the following Skills:\n")
	for _, skill := range skills {
		if skill.Name == "" {
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(skill.Name)
		if skill.Description != "" {
			builder.WriteString(": ")
			builder.WriteString(skill.Description)
		}
		builder.WriteByte('\n')
	}
	builder.WriteString("\nYou may request to use a Skill when relevant by saying:\n")
	builder.WriteString("<use_skill name=\"Skill Name\" input=\"...\" />\n")

	return builder.String()
}

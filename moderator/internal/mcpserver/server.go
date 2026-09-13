package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/mistral"
	"github.com/agentmaurice/mcpchatui/mcp/moderator/pkg/llm"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

const (
	serviceName    = "mistral-moderator"
	serviceVersion = "0.1.0"
)

// Builder coordinates MCP server construction.
type Builder struct {
	cfg        config.Config
	client     *mistral.Client
	logger     *zap.Logger
	toolDef    llm.Tool
	modern     *officialmcp.Server
	streamable http.Handler
}

// NewBuilder creates a Builder with sensible defaults.
func NewBuilder(cfg config.Config, client *mistral.Client, logger *zap.Logger) *Builder {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Builder{
		cfg:    cfg,
		client: client,
		logger: logger,
		toolDef: llm.Tool{
			Name:        "mistral_moderate_text",
			Description: "Analyzes a message with the Mistral moderation API and returns any sensitive categories that were detected.",
			InputSchema: llm.Schema{
				Type: "object",
				Properties: map[string]any{
					"text": map[string]any{
						"type":        "string",
						"description": "Plain text content to analyse.",
					},
					"conversation": map[string]any{
						"type":        "array",
						"description": "Optional list of previous conversation items (oldest to newest).",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"role": map[string]any{
									"type":        "string",
									"description": "Speaker role (`user`, `assistant`, `system`, etc.).",
								},
								"text": map[string]any{
									"type":        "string",
									"description": "Textual content for this turn.",
								},
								"content": map[string]any{
									"type":        "array",
									"description": "Advanced content representation (array of objects with `type` and `text`).",
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"type": map[string]any{
												"type":        "string",
												"description": "Content type (for example: `text`).",
											},
											"text": map[string]any{
												"type":        "string",
												"description": "Text content for this item.",
											},
										},
										"required": []string{"type", "text"},
									},
								},
							},
							"required": []string{"role"},
						},
					},
					"model": map[string]any{
						"type":        "string",
						"description": "Optional model override (e.g. `moderation-latest`).",
					},
				},
				Required: []string{"text"},
			},
		},
	}
}

// Build assembles the MCP SSE server ready to be started.
func (b *Builder) Build() (*server.SSEServer, error) {
	if b.client == nil {
		return nil, fmt.Errorf("mistral client is required")
	}

	mcpSrv := server.NewMCPServer(
		serviceName,
		serviceVersion,
		server.WithToolCapabilities(true),
		server.WithLogging(),
		server.WithInstructions(buildInstructions()),
	)
	b.modern = newModernMCPServer(serviceName, serviceVersion, buildInstructions())

	definition := b.convertToolDefinition()
	handler := b.moderationHandler()
	mcpSrv.AddTool(definition, handler)
	b.modern.AddTool(toOfficialTool(definition), adaptToolHandler(handler))
	b.streamable = officialmcp.NewStreamableHTTPHandler(
		func(*http.Request) *officialmcp.Server { return b.modern },
		&officialmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)

	options := []server.SSEOption{
		server.WithStaticBasePath(b.cfg.Server.BasePath),
		server.WithKeepAlive(b.cfg.Server.KeepAlive),
		server.WithKeepAliveInterval(b.cfg.Server.KeepAliveInterval),
	}
	if trimmed := strings.TrimSpace(b.cfg.Server.PublicURL); trimmed != "" {
		options = append(options, server.WithBaseURL(trimmed))
	}

	return server.NewSSEServer(mcpSrv, options...), nil
}

// Streamable returns the stateless modern MCP HTTP handler.
func (b *Builder) Streamable() http.Handler {
	return b.streamable
}

func (b *Builder) convertToolDefinition() mcp.Tool {
	props := make(map[string]any, len(b.toolDef.InputSchema.Properties))
	for key, value := range b.toolDef.InputSchema.Properties {
		props[key] = value
	}

	return mcp.Tool{
		Name:        b.toolDef.Name,
		Description: b.toolDef.Description,
		InputSchema: mcp.ToolInputSchema{
			Type:       b.toolDef.InputSchema.Type,
			Properties: props,
			Required:   b.toolDef.InputSchema.Required,
		},
		Annotations: mcp.ToolAnnotation{
			Title:           b.toolDef.Name,
			ReadOnlyHint:    mcp.ToBoolPtr(true),
			DestructiveHint: mcp.ToBoolPtr(false),
			IdempotentHint:  mcp.ToBoolPtr(true),
			OpenWorldHint:   mcp.ToBoolPtr(true),
		},
	}
}

func (b *Builder) moderationHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		b.logger.Debug("received moderation request",
			zap.String("tool_name", request.Params.Name),
			zap.Any("raw_arguments", request.Params.Arguments),
		)

		text, err := request.RequireString("text")
		if err != nil {
			b.logger.Debug("missing required argument 'text'", zap.Error(err))
			return nil, fmt.Errorf("missing required argument 'text': %w", err)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			b.logger.Debug("argument 'text' is empty after trimming")
			return nil, fmt.Errorf("argument 'text' must not be empty")
		}

		b.logger.Debug("parsed text argument",
			zap.Int("text_length", len(text)),
			zap.String("text_preview", truncateForLog(text, 100)),
		)

		args := request.GetArguments()
		var conversation []mistral.ChatMessage
		if rawConversation, ok := args["conversation"]; ok {
			b.logger.Debug("parsing conversation argument", zap.Any("raw_conversation", rawConversation))
			conversation, err = parseConversation(rawConversation)
			if err != nil {
				b.logger.Debug("failed to parse conversation", zap.Error(err))
				return nil, err
			}
			b.logger.Debug("parsed conversation", zap.Int("message_count", len(conversation)))
		}

		var modelOverride string
		if rawModel, ok := args["model"]; ok {
			if modelStr, ok := rawModel.(string); ok {
				modelOverride = strings.TrimSpace(modelStr)
				b.logger.Debug("model override specified", zap.String("model", modelOverride))
			}
		}

		messages := append(conversation, mistral.ChatMessage{
			Role: "user",
			Content: []mistral.ChatContentItem{
				{Type: "text", Text: text},
			},
		})

		payload := mistral.ChatModerationRequest{
			Model: modelOverride,
			Input: messages,
		}

		b.logger.Debug("sending moderation request to Mistral",
			zap.Int("total_messages", len(messages)),
			zap.String("model_override", modelOverride),
		)

		result, err := b.client.ModerateChat(ctx, payload)
		if err != nil {
			b.logger.Error("mistral moderation failed", zap.Error(err))
			return nil, fmt.Errorf("mistral moderation failed: %w", err)
		}

		response := buildToolResponse(result)
		b.logger.Debug("moderation completed",
			zap.String("response_id", response.ID),
			zap.String("model_used", response.Model),
			zap.Bool("flagged", response.Flagged),
			zap.Bool("blocked", response.Blocked),
			zap.Strings("flagged_categories", response.FlaggedCategories),
			zap.Int("results_count", len(response.Results)),
		)

		return mcp.NewToolResultStructured(response, buildFallbackSummary(result)), nil
	}
}

// truncateForLog truncates a string to maxLen characters for logging purposes.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func parseConversation(raw any) ([]mistral.ChatMessage, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("conversation must be an array")
	}
	messages := make([]mistral.ChatMessage, 0, len(items))
	for idx, entry := range items {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("conversation[%d] must be an object", idx)
		}
		roleRaw, ok := entryMap["role"]
		if !ok {
			return nil, fmt.Errorf("conversation[%d] requires a 'role' field", idx)
		}
		role, ok := roleRaw.(string)
		if !ok || strings.TrimSpace(role) == "" {
			return nil, fmt.Errorf("conversation[%d].role must be a non-empty string", idx)
		}

		var contentItems []mistral.ChatContentItem
		if rawContent, ok := entryMap["content"]; ok {
			parsed, err := parseContentArray(rawContent)
			if err != nil {
				return nil, fmt.Errorf("conversation[%d].content is invalid: %w", idx, err)
			}
			contentItems = parsed
		}

		if len(contentItems) == 0 {
			if textRaw, ok := entryMap["text"]; ok {
				if text, ok := textRaw.(string); ok && strings.TrimSpace(text) != "" {
					contentItems = []mistral.ChatContentItem{{Type: "text", Text: strings.TrimSpace(text)}}
				}
			}
		}

		if len(contentItems) == 0 {
			return nil, fmt.Errorf("conversation[%d] must provide either 'text' or 'content'", idx)
		}

		messages = append(messages, mistral.ChatMessage{
			Role:    strings.TrimSpace(role),
			Content: contentItems,
		})
	}

	return messages, nil
}

func parseContentArray(raw any) ([]mistral.ChatContentItem, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("content must be an array")
	}
	result := make([]mistral.ChatContentItem, 0, len(items))
	for idx, entry := range items {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("content[%d] must be an object", idx)
		}
		typeVal, _ := entryMap["type"].(string)
		textVal, _ := entryMap["text"].(string)
		typeVal = strings.TrimSpace(typeVal)
		textVal = strings.TrimSpace(textVal)
		if typeVal == "" || textVal == "" {
			return nil, fmt.Errorf("content[%d] must include both 'type' and 'text'", idx)
		}
		result = append(result, mistral.ChatContentItem{
			Type: typeVal,
			Text: textVal,
		})
	}
	return result, nil
}

type toolResponse struct {
	ID                string                         `json:"id"`
	Model             string                         `json:"model"`
	Flagged           bool                           `json:"flagged"`
	Blocked           bool                           `json:"blocked"`
	FlaggedCategories []string                       `json:"flagged_categories"`
	Results           []mistral.ChatModerationResult `json:"results"`
	RawResponse       json.RawMessage                `json:"raw_response,omitempty"`
}

func buildToolResponse(response *mistral.ChatModerationResponse) toolResponse {
	flaggedCategories := map[string]bool{}
	flagged := false
	blocked := false
	for _, item := range response.Results {
		if item.Flagged {
			flagged = true
		}
		if item.Blocked {
			blocked = true
		}
		for cat, value := range item.Categories {
			if value {
				flaggedCategories[cat] = true
			}
		}
	}

	categoryList := make([]string, 0, len(flaggedCategories))
	for category := range flaggedCategories {
		categoryList = append(categoryList, category)
	}
	sort.Strings(categoryList)

	// Consider content flagged if Mistral's API flagged it OR if any category was detected
	// Mistral API may return categories=true without setting flagged=true (based on score thresholds)
	effectiveFlagged := flagged || len(categoryList) > 0

	return toolResponse{
		ID:                response.ID,
		Model:             response.Model,
		Flagged:           effectiveFlagged,
		Blocked:           blocked,
		FlaggedCategories: categoryList,
		Results:           response.Results,
		RawResponse:       response.Raw,
	}
}

func buildFallbackSummary(response *mistral.ChatModerationResponse) string {
	payload := buildToolResponse(response)
	if payload.Blocked {
		return "Content blocked by Mistral moderation."
	}
	// Check both Flagged flag and FlaggedCategories list
	// Mistral API may return categories without setting Flagged=true (based on score thresholds)
	if payload.Flagged || len(payload.FlaggedCategories) > 0 {
		if len(payload.FlaggedCategories) > 0 {
			return fmt.Sprintf("Flagged content: %s.", strings.Join(payload.FlaggedCategories, ", "))
		}
		return "Content flagged by Mistral moderation."
	}
	return "No sensitive categories detected."
}

func buildInstructions() string {
	return "This MCP server exposes a moderation tool backed by the Mistral API. " +
		"Call the `mistral_moderate_text` tool to analyse a message and surface potential policy violations."
}

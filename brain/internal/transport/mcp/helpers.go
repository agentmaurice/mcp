package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func errorResult(message string) *mcplib.CallToolResult {
	return &mcplib.CallToolResult{
		IsError: true,
		Content: []mcplib.Content{
			mcplib.TextContent{
				Type: "text",
				Text: message,
			},
		},
	}
}

func codedErrorResult(code, message string, retryable bool) *mcplib.CallToolResult {
	payload := map[string]any{
		"error": map[string]any{
			"code":      code,
			"message":   message,
			"retryable": retryable,
		},
	}
	data, _ := json.Marshal(payload)
	return &mcplib.CallToolResult{
		IsError: true,
		Content: []mcplib.Content{
			mcplib.TextContent{Type: "text", Text: string(data)},
		},
		StructuredContent: payload,
	}
}

func structuredResult(payload any) *mcplib.CallToolResult {
	data, _ := json.Marshal(payload)
	structured := payload
	if payload == nil {
		structured = json.RawMessage("null")
	}
	return &mcplib.CallToolResult{
		Content: []mcplib.Content{
			mcplib.TextContent{
				Type: "text",
				Text: string(data),
			},
		},
		StructuredContent: structured,
	}
}

func parseArgs(input any, target any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("failed to marshal arguments: %w", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("failed to unmarshal arguments: %w", err)
	}
	return nil
}

func resolveTenantID(ctx context.Context, cfg *config.Config, tenantArg string, rawArgs any) (string, error) {
	tenantID := strings.TrimSpace(tenantArg)
	if tenantID == "" {
		tenantID = strings.TrimSpace(extractTenantArg(rawArgs))
	}
	if tenantID == "" {
		identity := shared.IdentityFromContext(ctx)
		tenantID = strings.TrimSpace(identity.TenantID)
	}
	if tenantID == "" && cfg != nil {
		tenantID = strings.TrimSpace(cfg.Storage.DefaultTenantID)
	}
	if tenantID == "" {
		return "", fmt.Errorf("tenant_id is required")
	}
	if !tenantIDPattern.MatchString(tenantID) {
		return "", fmt.Errorf("invalid tenant_id: %s", tenantID)
	}
	return tenantID, nil
}

func extractTenantArg(input any) string {
	if input == nil {
		return ""
	}
	args, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	if val, ok := args["tenant_id"].(string); ok {
		return val
	}
	if val, ok := args["tenantId"].(string); ok {
		return val
	}
	return ""
}

func rebindQuery(manager storage.Manager, query string) string {
	if manager == nil {
		return query
	}
	if strings.ToLower(manager.Dialect()) != "postgres" {
		return query
	}
	return rebindDollar(query)
}

func rebindDollar(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			b.WriteString(fmt.Sprintf("$%d", arg))
			arg++
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

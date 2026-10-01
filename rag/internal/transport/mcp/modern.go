package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	legacymcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const jsonSchema202012 = "https://json-schema.org/draft/2020-12/schema"

func newModernMCPServer(name, version, instructions string) *modernmcp.Server {
	return modernmcp.New(name, version, instructions)
}

func toOfficialTool(definition legacymcp.Tool) legacymcp.Tool { return definition }
func adaptToolHandler(handler server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, request legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
		result, err := handler(ctx, request)
		if result == nil || err != nil || result.StructuredContent != nil || len(result.Content) != 1 {
			return result, err
		}
		content, ok := result.Content[0].(legacymcp.TextContent)
		if !ok || !json.Valid([]byte(content.Text)) {
			return result, err
		}
		result.RawStructuredContent = json.RawMessage(content.Text)
		var promoted any
		if json.Unmarshal([]byte(content.Text), &promoted) == nil && promoted != nil {
			result.StructuredContent = promoted
		}
		return result, err
	}
}
func toOfficialToolResult(result *legacymcp.CallToolResult) (*legacymcp.CallToolResult, error) {
	return result, nil
}

func toOfficialResourceTemplate(definition legacymcp.ResourceTemplate) legacymcp.ResourceTemplate {
	return definition
}

func adaptResourceHandler(handler server.ResourceHandlerFunc) server.ResourceHandlerFunc {
	return func(ctx context.Context, request legacymcp.ReadResourceRequest) ([]legacymcp.ResourceContents, error) {
		contents, err := handler(ctx, request)
		if err != nil {
			var appErr *shared.AppError
			if errors.As(err, &appErr) && appErr.Code == 404 {
				return nil, errors.Join(modernmcp.ErrResourceNotFound, fmt.Errorf("%w: %v", modernmcp.ErrResourceNotFound, err))
			}
		}
		return contents, err
	}
}

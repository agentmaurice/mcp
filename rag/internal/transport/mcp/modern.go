package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	legacymcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const jsonSchema202012 = "https://json-schema.org/draft/2020-12/schema"

func newModernMCPServer(name, version, instructions string) *officialmcp.Server {
	// The SDK serves these list endpoints. Advertise their static shape without
	// listChanged or logging support that the stateless server does not provide.
	return officialmcp.NewServer(
		&officialmcp.Implementation{Name: name, Version: version},
		&officialmcp.ServerOptions{
			Instructions: instructions,
			Capabilities: &officialmcp.ServerCapabilities{
				Tools:     &officialmcp.ToolCapabilities{},
				Prompts:   &officialmcp.PromptCapabilities{},
				Resources: &officialmcp.ResourceCapabilities{},
			},
		},
	)
}

func toOfficialTool(definition legacymcp.Tool) *officialmcp.Tool {
	data, err := json.Marshal(definition)
	if err != nil {
		panic(fmt.Errorf("marshal tool %q for modern MCP: %w", definition.Name, err))
	}
	var tool officialmcp.Tool
	if err := json.Unmarshal(data, &tool); err != nil {
		panic(fmt.Errorf("convert tool %q for modern MCP: %w", definition.Name, err))
	}
	inputSchema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		panic(fmt.Errorf("tool %q input schema is not a JSON object", definition.Name))
	}
	inputSchema["$schema"] = jsonSchema202012
	tool.InputSchema = inputSchema
	if outputSchema, ok := tool.OutputSchema.(map[string]any); ok {
		outputSchema["$schema"] = jsonSchema202012
		tool.OutputSchema = outputSchema
	}
	return &tool
}

func adaptToolHandler(handler server.ToolHandlerFunc) officialmcp.ToolHandler {
	return func(ctx context.Context, request *officialmcp.CallToolRequest) (*officialmcp.CallToolResult, error) {
		var arguments any
		if len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return nil, fmt.Errorf("decode tool arguments: %w", err)
			}
		}
		legacyResult, err := handler(ctx, legacymcp.CallToolRequest{
			Header: requestHeader(request.Extra),
			Params: legacymcp.CallToolParams{
				Name:      request.Params.Name,
				Arguments: arguments,
				Meta:      toLegacyMeta(request.Params.Meta),
			},
		})
		if err != nil || legacyResult == nil {
			return nil, err
		}
		return toOfficialToolResult(legacyResult)
	}
}

func toOfficialToolResult(result *legacymcp.CallToolResult) (*officialmcp.CallToolResult, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal tool result: %w", err)
	}
	var converted officialmcp.CallToolResult
	if err := json.Unmarshal(data, &converted); err != nil {
		return nil, fmt.Errorf("convert tool result: %w", err)
	}
	var raw struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("inspect tool result: %w", err)
	}
	if len(raw.StructuredContent) > 0 {
		if string(raw.StructuredContent) == "null" {
			converted.StructuredContent = json.RawMessage("null")
		}
		return &converted, nil
	}

	// Several historical RAG tools return JSON as text only. Preserve that wire
	// shape while also exposing the same value through structuredContent.
	if len(result.Content) == 1 {
		if content, ok := result.Content[0].(legacymcp.TextContent); ok && json.Valid([]byte(content.Text)) {
			converted.StructuredContent = json.RawMessage(content.Text)
		}
	}
	return &converted, nil
}

func toOfficialResourceTemplate(definition legacymcp.ResourceTemplate) *officialmcp.ResourceTemplate {
	data, err := json.Marshal(definition)
	if err != nil {
		panic(fmt.Errorf("marshal resource template %q for modern MCP: %w", definition.Name, err))
	}
	var resourceTemplate officialmcp.ResourceTemplate
	if err := json.Unmarshal(data, &resourceTemplate); err != nil {
		panic(fmt.Errorf("convert resource template %q for modern MCP: %w", definition.Name, err))
	}
	return &resourceTemplate
}

func adaptResourceHandler(handler server.ResourceHandlerFunc) officialmcp.ResourceHandler {
	return func(ctx context.Context, request *officialmcp.ReadResourceRequest) (*officialmcp.ReadResourceResult, error) {
		contents, err := handler(ctx, legacymcp.ReadResourceRequest{
			Header: requestHeader(request.Extra),
			Params: legacymcp.ReadResourceParams{URI: request.Params.URI},
		})
		if err != nil {
			var appErr *shared.AppError
			if errors.As(err, &appErr) && appErr.Code == 404 {
				return nil, officialmcp.ResourceNotFoundError(request.Params.URI)
			}
			return nil, err
		}
		data, err := json.Marshal(&legacymcp.ReadResourceResult{Contents: contents})
		if err != nil {
			return nil, fmt.Errorf("marshal resource result: %w", err)
		}
		var result officialmcp.ReadResourceResult
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("convert resource result: %w", err)
		}
		return &result, nil
	}
}

func toLegacyMeta(meta officialmcp.Meta) *legacymcp.Meta {
	if meta == nil {
		return nil
	}
	fields := make(map[string]any, len(meta))
	for key, value := range meta {
		fields[key] = value
	}
	return legacymcp.NewMetaFromMap(fields)
}

func requestHeader(extra *officialmcp.RequestExtra) map[string][]string {
	if extra == nil {
		return nil
	}
	return extra.Header
}

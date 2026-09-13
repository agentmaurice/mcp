package mcp

import (
	"context"
	"encoding/json"
	"fmt"

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
	if string(raw.StructuredContent) == "null" {
		converted.StructuredContent = json.RawMessage("null")
	}
	return &converted, nil
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

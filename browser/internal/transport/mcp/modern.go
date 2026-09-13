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

func newModernMCPServer(name, version string) *officialmcp.Server {
	// The official SDK always serves the list endpoints. Advertise those static
	// capabilities explicitly, without listChanged or logging support that this
	// stateless server does not implement.
	return officialmcp.NewServer(
		&officialmcp.Implementation{Name: name, Version: version},
		&officialmcp.ServerOptions{
			Capabilities: &officialmcp.ServerCapabilities{
				Tools:     &officialmcp.ToolCapabilities{},
				Prompts:   &officialmcp.PromptCapabilities{},
				Resources: &officialmcp.ResourceCapabilities{},
			},
		},
	)
}

func toOfficialTool(def legacymcp.Tool) *officialmcp.Tool {
	data, err := json.Marshal(def)
	if err != nil {
		panic(fmt.Errorf("marshal tool %q for modern MCP: %w", def.Name, err))
	}

	var tool officialmcp.Tool
	if err := json.Unmarshal(data, &tool); err != nil {
		panic(fmt.Errorf("convert tool %q for modern MCP: %w", def.Name, err))
	}

	inputSchema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		panic(fmt.Errorf("tool %q input schema is not a JSON object", def.Name))
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
		var meta *legacymcp.Meta
		if request.Params.Meta != nil {
			fields := make(map[string]any, len(request.Params.Meta))
			for key, value := range request.Params.Meta {
				fields[key] = value
			}
			meta = legacymcp.NewMetaFromMap(fields)
		}
		var header map[string][]string
		if request.Extra != nil {
			header = request.Extra.Header
		}

		legacyResult, err := handler(ctx, legacymcp.CallToolRequest{
			Header: header,
			Params: legacymcp.CallToolParams{
				Name:      request.Params.Name,
				Arguments: arguments,
				Meta:      meta,
			},
		})
		if err != nil || legacyResult == nil {
			return nil, err
		}

		data, err := json.Marshal(legacyResult)
		if err != nil {
			return nil, fmt.Errorf("marshal tool result: %w", err)
		}
		var modernResult officialmcp.CallToolResult
		if err := json.Unmarshal(data, &modernResult); err != nil {
			return nil, fmt.Errorf("convert tool result: %w", err)
		}
		var rawResult struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
		}
		if err := json.Unmarshal(data, &rawResult); err != nil {
			return nil, fmt.Errorf("inspect tool result: %w", err)
		}
		if string(rawResult.StructuredContent) == "null" {
			modernResult.StructuredContent = json.RawMessage("null")
		}
		return &modernResult, nil
	}
}

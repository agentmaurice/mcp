package mcp

import (
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	legacymcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const jsonSchema202012 = "https://json-schema.org/draft/2020-12/schema"

func newModernMCPServer(name, version, instructions string) *modernmcp.Server {
	return modernmcp.New(name, version, instructions)
}

func toOfficialTool(definition legacymcp.Tool) legacymcp.Tool                { return definition }
func adaptToolHandler(handler server.ToolHandlerFunc) server.ToolHandlerFunc { return handler }
func toOfficialToolResult(result *legacymcp.CallToolResult) (*legacymcp.CallToolResult, error) {
	return result, nil
}

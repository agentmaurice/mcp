package mcp

import (
	legacymcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
)

func newModernMCPServer(name, version string) *modernmcp.Server {
	return modernmcp.New(name, version, "")
}

// The shared facade owns the wire adapter. These helpers keep the browser
// adapter's registration boundary explicit while avoiding a second SDK.
func toOfficialTool(def legacymcp.Tool) legacymcp.Tool { return def }
func adaptToolHandler(handler server.ToolHandlerFunc) server.ToolHandlerFunc { return handler }

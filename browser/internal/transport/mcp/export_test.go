//go:build e2e

package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// CallToolForTest calls an MCP tool handler by name. Exposed for E2E tests only.
// The session_key is injected into the context from args["session_key"].
func (s *Server) CallToolForTest(ctx context.Context, name string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	tool := s.mcpServer.GetTool(name)
	if tool == nil {
		return nil, shared.ErrValidation("tool not found: " + name)
	}

	// Extract session key and inject into context (same as addTool wrapper)
	sessionKey := ""
	if sk, ok := args["session_key"].(string); ok {
		sessionKey = sk
	}
	if sessionKey == "" {
		sessionKey = shared.DefaultSessionKey
	}
	ctx = shared.WithSessionKey(ctx, sessionKey)

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}

	return tool.Handler(ctx, request)
}

// GetMCPServer returns the internal MCPServer for test inspection.
func (s *Server) GetMCPServer() interface{} {
	return s.mcpServer
}

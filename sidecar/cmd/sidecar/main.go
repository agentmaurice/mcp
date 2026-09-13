package main

import "github.com/agentmaurice/mcpchatui/mcp/sidecar/cli"

// MCP Sidecar - Autonomous registration and identity management for MCP servers.
//
// This CLI handles the complete lifecycle of MCP server identity:
// - Initial registration using bootstrap tokens
// - Credential persistence and management
// - Periodic validation and automatic renewal
// - Graceful shutdown and cleanup
func main() {
	cli.Execute()
}

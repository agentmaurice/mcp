package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/agentclient"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/protocol"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const serviceVersion = "0.1.0"

func New(client agentclient.API) *modernmcp.Server {
	s := modernmcp.New(
		"agentmaurice-system",
		serviceVersion,
		"Use System for bounded VM diagnostics and governed allowlisted actions. Never imply that arbitrary shell or filesystem access is available.",
	)
	s.AddTool(readTool("system_health_v1", "Check the local System host agent boundary.", schema(nil, nil)), withClient(client, health))
	s.AddTool(readTool("system_capabilities_v1", "Describe diagnostics, allowlisted actions and mutation policy.", schema(nil, nil)), withClient(client, capabilities))
	s.AddTool(readTool("system_inspect_v1", "Inspect bounded host identity, uptime, load, memory and root disk metrics.", schema(nil, nil)), withClient(client, inspect))
	s.AddTool(readTool("system_services_v1", "List the state of administrator-allowlisted services.", schema(nil, nil)), withClient(client, services))
	s.AddTool(readTool("system_logs_v1", "Read bounded, redacted logs for one allowlisted service.", schema(map[string]any{
		"service": map[string]any{"type": "string", "minLength": 1},
		"lines":   map[string]any{"type": "integer", "minimum": 1, "maximum": 2000, "default": 100},
	}, []string{"service"})), withClient(client, logs))
	s.AddTool(actionTool("system_action_plan_v1", "Create a short-lived hashed plan for one allowlisted system action.", false, schema(map[string]any{
		"action": map[string]any{"type": "string", "enum": []string{"service_restart", "runtime_restart"}},
		"target": map[string]any{"type": "string", "description": "Allowlisted service name; ignored for runtime_restart"},
	}, []string{"action"})), withClient(client, plan))
	s.AddTool(actionTool("system_action_apply_v1", "Apply exactly one unexpired system action plan when standing_grant is enabled.", true, schema(map[string]any{
		"plan_id":   map[string]any{"type": "string", "minLength": 1},
		"plan_hash": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
	}, []string{"plan_id", "plan_hash"})), withClient(client, apply))
	return s
}

type handler func(context.Context, agentclient.API, mcp.CallToolRequest) (*mcp.CallToolResult, error)

func withClient(client agentclient.API, next handler) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return next(ctx, client, request)
	}
}

func health(ctx context.Context, client agentclient.API, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	value, err := client.Health(ctx)
	return response(value, err)
}

func capabilities(ctx context.Context, client agentclient.API, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	value, err := client.Capabilities(ctx)
	return response(value, err)
}

func inspect(ctx context.Context, client agentclient.API, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	value, err := client.Inspect(ctx)
	return response(value, err)
}

func services(ctx context.Context, client agentclient.API, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	value, err := client.Services(ctx)
	return response(value, err)
}

func logs(ctx context.Context, client agentclient.API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input protocol.LogsRequest
	if err := decode(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := client.Logs(ctx, input)
	return response(value, err)
}

func plan(ctx context.Context, client agentclient.API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input protocol.PlanRequest
	if err := decode(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := client.Plan(ctx, input)
	return response(value, err)
}

func apply(ctx context.Context, client agentclient.API, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input protocol.ApplyRequest
	if err := decode(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	value, err := client.Apply(ctx, input.PlanID, input.PlanHash)
	return response(value, err)
}

func response(value any, err error) (*mcp.CallToolResult, error) {
	if err == nil {
		return &mcp.CallToolResult{StructuredContent: value}, nil
	}
	var apiError *agentclient.Error
	if errors.As(err, &apiError) {
		return failure(apiError.Code, apiError.Message), nil
	}
	return failure("host_agent_unavailable", err.Error()), nil
}

func decode(request mcp.CallToolRequest, target any) error {
	payload, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}
	return nil
}

func readTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := true, false, true
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}

func actionTool(name, description string, destructive bool, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, idempotent := false, false
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}

func schema(properties map[string]any, required []string) mcp.ToolInputSchema {
	if properties == nil {
		properties = map[string]any{}
	}
	return mcp.ToolInputSchema{Type: "object", Properties: properties, Required: required}
}

func failure(code, message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"error": code, "message": message}}
}

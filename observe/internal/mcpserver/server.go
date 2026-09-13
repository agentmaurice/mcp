package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/observe/internal/observe"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	serviceVersion = "0.1.0"
	maxTraceBytes  = 2 * 1024 * 1024
)

func New() *modernmcp.Server {
	s := modernmcp.New("agentmaurice-observe", serviceVersion, "Use Observe to inspect bounded, redacted run summaries or OTLP JSON. Do not pass raw prompts, credentials or business payloads.")
	s.AddTool(tool("observe_health_v1", "Check the Observe MCP.", schema(nil, nil)), health)
	s.AddTool(tool("observe_capabilities_v1", "Describe supported trace inputs and limits.", schema(nil, nil)), capabilities)
	one := schema(map[string]interface{}{"content": contentProperty("Redacted OTLP JSON or run summary")}, []string{"content"})
	s.AddTool(tool("observe_run_v1", "Normalize a trace and return its bounded timeline and summary.", one), run)
	s.AddTool(tool("observe_explain_v1", "Explain errors, retries and the critical path deterministically.", one), explain)
	two := schema(map[string]interface{}{"baseline": contentProperty("Baseline trace JSON"), "candidate": contentProperty("Candidate trace JSON")}, []string{"baseline", "candidate"})
	s.AddTool(tool("observe_compare_v1", "Compare two run traces.", two), compare)
	return s
}

func health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"status": "ok", "version": serviceVersion}), nil
}

func capabilities(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"version": "v1", "formats": []string{"otlp-json", "run-summary-json"}, "max_trace_bytes": maxTraceBytes, "direct_backends": false, "redacted_input_required": true}), nil
}

func run(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	spans, err := parseArgument(request, "content")
	if err != nil {
		return failure("invalid_trace", err.Error()), nil
	}
	return result(map[string]interface{}{"status": "ok", "summary": observe.Summarize(spans), "timeline": spans}), nil
}

func explain(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	spans, err := parseArgument(request, "content")
	if err != nil {
		return failure("invalid_trace", err.Error()), nil
	}
	payload := observe.Explain(spans)
	payload["status"] = "ok"
	return result(payload), nil
}

func compare(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	baseline, err := parseArgument(request, "baseline")
	if err != nil {
		return failure("invalid_baseline", err.Error()), nil
	}
	candidate, err := parseArgument(request, "candidate")
	if err != nil {
		return failure("invalid_candidate", err.Error()), nil
	}
	payload := observe.Compare(baseline, candidate)
	payload["status"] = "ok"
	return result(payload), nil
}

func parseArgument(request mcp.CallToolRequest, name string) ([]observe.Span, error) {
	content, ok := request.GetArguments()[name].(string)
	if !ok || strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	if len(content) > maxTraceBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, maxTraceBytes)
	}
	return observe.Parse(content)
}

func contentProperty(description string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "maxLength": maxTraceBytes, "description": description}
}

func tool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := true, false, true
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}

func schema(properties map[string]interface{}, required []string) mcp.ToolInputSchema {
	if properties == nil {
		properties = map[string]interface{}{}
	}
	return mcp.ToolInputSchema{Type: "object", Properties: properties, Required: required}
}

func result(content interface{}) *mcp.CallToolResult {
	return &mcp.CallToolResult{StructuredContent: content}
}

func failure(code, message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, StructuredContent: map[string]interface{}{"error": code, "message": message}}
}

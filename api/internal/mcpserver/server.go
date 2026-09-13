package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apiengine "github.com/agentmaurice/mcpchatui/mcp/api/internal/api"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

const serviceVersion = "0.1.0"
const maxSpecBytes = 2 * 1024 * 1024

func New() *modernmcp.Server {
	s := modernmcp.New("agentmaurice-api", serviceVersion, "Use API only with an approved OpenAPI document and operationId. Free-form URLs, literal IPs and redirects are forbidden.")
	s.AddTool(readTool("api_health_v1", "Check the API MCP.", schema(nil, nil)), health)
	s.AddTool(readTool("api_capabilities_v1", "Describe OpenAPI support and egress safety controls.", schema(nil, nil)), capabilities)
	describe := schema(map[string]interface{}{"spec": map[string]interface{}{"type": "string", "maxLength": maxSpecBytes}, "operation_id": map[string]interface{}{"type": "string"}}, []string{"spec"})
	s.AddTool(readTool("api_describe_v1", "List approved operations or describe one operationId.", describe), describeAPI)
	call := schema(map[string]interface{}{"spec": map[string]interface{}{"type": "string", "maxLength": maxSpecBytes}, "base_url": map[string]interface{}{"type": "string"}, "operation_id": map[string]interface{}{"type": "string"}, "path_params": map[string]interface{}{"type": "object"}, "query": map[string]interface{}{"type": "object"}, "headers": map[string]interface{}{"type": "object"}, "body": map[string]interface{}{}, "credential_ref": map[string]interface{}{"type": "string", "pattern": "^secret://"}, "allow_mutation": map[string]interface{}{"type": "boolean", "default": false}}, []string{"spec", "base_url", "operation_id"})
	s.AddTool(callTool("api_call_v1", "Call exactly one OpenAPI operationId through the egress guard.", call), callAPI)
	return s
}
func health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"status": "ok", "version": serviceVersion}), nil
}
func capabilities(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"version": "v1", "openapi": []string{"3.0", "3.1"}, "operation_id_required": true, "read_only_default": true, "https_required": true, "literal_ips": false, "redirects": false, "max_spec_bytes": maxSpecBytes, "max_response_bytes": apiengine.MaxResponseBytes}), nil
}
func describeAPI(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	spec, _ := request.GetArguments()["spec"].(string)
	if len(spec) == 0 || len(spec) > maxSpecBytes {
		return failure("invalid_spec", fmt.Sprintf("spec is required and limited to %d bytes", maxSpecBytes)), nil
	}
	document, err := apiengine.Parse(spec)
	if err != nil {
		return failure("invalid_spec", err.Error()), nil
	}
	operationID, _ := request.GetArguments()["operation_id"].(string)
	if strings.TrimSpace(operationID) == "" {
		return result(map[string]interface{}{"status": "ok", "operations": apiengine.Operations(document)}), nil
	}
	operation, err := apiengine.Find(document, operationID)
	if err != nil {
		return failure("unknown_operation", err.Error()), nil
	}
	return result(map[string]interface{}{"status": "ok", "operation": operation}), nil
}
func callAPI(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input apiengine.CallInput
	payload, err := json.Marshal(request.Params.Arguments)
	if err != nil || json.Unmarshal(payload, &input) != nil {
		return failure("invalid_arguments", "arguments do not match the API schema"), nil
	}
	if len(input.Spec) > maxSpecBytes {
		return failure("invalid_spec", "spec exceeds the size limit"), nil
	}
	response, err := apiengine.Call(ctx, input)
	if err != nil {
		return failure("api_call_failed", err.Error()), nil
	}
	return result(map[string]interface{}{"status": "ok", "response": response}), nil
}
func readTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := true, false, true
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}
func callTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := false, true, false
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

package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/guard/internal/guard"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	serviceVersion = "0.1.0"
	maxTextBytes   = 1024 * 1024
)

func New() *modernmcp.Server {
	s := modernmcp.New("agentmaurice-guard", serviceVersion, "Use Guard to detect, redact and classify sensitive text deterministically. Findings never copy detected secret values.")
	s.AddTool(tool("guard_health_v1", "Check the Guard MCP.", object(nil, nil)), health)
	s.AddTool(tool("guard_capabilities_v1", "Describe Guard categories, redaction modes and limits.", object(nil, nil)), capabilities)
	input := object(map[string]interface{}{"text": map[string]interface{}{"type": "string", "maxLength": maxTextBytes}}, []string{"text"})
	s.AddTool(tool("guard_scan_v1", "Locate PII and secrets without returning their values.", input), scan)
	redactInput := object(map[string]interface{}{"text": map[string]interface{}{"type": "string", "maxLength": maxTextBytes}, "mode": map[string]interface{}{"type": "string", "enum": []string{"mask", "hash", "remove"}, "default": "mask"}}, []string{"text"})
	s.AddTool(tool("guard_redact_v1", "Redact PII and secrets using mask, hash or removal.", redactInput), redact)
	s.AddTool(tool("guard_classify_v1", "Classify text as public, internal, confidential or restricted.", input), classify)
	return s
}

func health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"status": "ok", "version": serviceVersion}), nil
}

func capabilities(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"version": "v1", "deterministic": true, "max_text_bytes": maxTextBytes, "categories": []string{"email", "phone", "iban", "payment_card", "ipv4", "token", "private_key"}, "redaction_modes": []string{"mask", "hash", "remove"}, "classifications": []string{"public", "internal", "confidential", "restricted"}}), nil
}

func scan(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text, err := textArgument(request)
	if err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	findings := guard.Scan(text)
	return result(map[string]interface{}{"status": "ok", "findings": findings, "count": len(findings)}), nil
}

func redact(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text, err := textArgument(request)
	if err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	mode, _ := request.GetArguments()["mode"].(string)
	redacted, findings, err := guard.Redact(text, strings.TrimSpace(mode))
	if err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	return result(map[string]interface{}{"status": "ok", "text": redacted, "findings": findings, "count": len(findings), "mode": defaultString(mode, "mask")}), nil
}

func classify(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text, err := textArgument(request)
	if err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	findings := guard.Scan(text)
	return result(map[string]interface{}{"status": "ok", "classification": guard.Classify(findings), "finding_types": uniqueTypes(findings), "rules_version": "default-v1"}), nil
}

func textArgument(request mcp.CallToolRequest) (string, error) {
	raw, ok := request.GetArguments()["text"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("text is required")
	}
	if len(raw) > maxTextBytes {
		return "", fmt.Errorf("text exceeds %d bytes", maxTextBytes)
	}
	return raw, nil
}

func uniqueTypes(findings []guard.Finding) []string {
	seen := map[string]bool{}
	types := make([]string, 0)
	for _, finding := range findings {
		if !seen[finding.Type] {
			seen[finding.Type] = true
			types = append(types, finding.Type)
		}
	}
	return types
}

func tool(name, description string, schema mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := true, false, true
	return mcp.Tool{Name: name, Description: description, InputSchema: schema, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}

func object(properties map[string]interface{}, required []string) mcp.ToolInputSchema {
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

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

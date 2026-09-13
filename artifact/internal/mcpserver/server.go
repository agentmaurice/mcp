package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	artifactengine "github.com/agentmaurice/mcpchatui/mcp/artifact/internal/artifact"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

const serviceVersion = "0.1.0"
const maxArtifactBytes = 2 * 1024 * 1024

func New() *modernmcp.Server {
	s := modernmcp.New("agentmaurice-artifact", serviceVersion, "Use Artifact to create, patch, render and inspect bounded deliverables with a SHA-256 provenance manifest.")
	s.AddTool(readTool("artifact_health_v1", "Check the Artifact MCP.", schema(nil, nil)), health)
	s.AddTool(readTool("artifact_capabilities_v1", "Describe formats, renderer and limits.", schema(nil, nil)), capabilities)
	s.AddTool(writeTool("artifact_create_v1", "Create a deterministic bounded artifact.", schema(map[string]interface{}{"format": map[string]interface{}{"type": "string", "enum": []string{"markdown", "html", "csv", "json", "pdf"}}, "content": map[string]interface{}{}}, []string{"format", "content"})), create)
	s.AddTool(writeTool("artifact_patch_v1", "Apply ordered replace, append or prepend operations.", schema(map[string]interface{}{"format": map[string]interface{}{"type": "string", "enum": []string{"markdown", "html", "json"}}, "content": map[string]interface{}{"type": "string", "maxLength": maxArtifactBytes}, "patches": map[string]interface{}{"type": "array", "maxItems": 100, "items": map[string]interface{}{"type": "object"}}}, []string{"format", "content", "patches"})), patchArtifact)
	s.AddTool(writeTool("artifact_render_v1", "Render Markdown or HTML to a deterministic PDF preview.", schema(map[string]interface{}{"format": map[string]interface{}{"type": "string", "enum": []string{"markdown", "html"}}, "content": map[string]interface{}{"type": "string", "maxLength": maxArtifactBytes}, "output_format": map[string]interface{}{"type": "string", "enum": []string{"pdf"}}}, []string{"format", "content", "output_format"})), render)
	s.AddTool(readTool("artifact_inspect_v1", "Validate an artifact and return its provenance manifest.", schema(map[string]interface{}{"format": map[string]interface{}{"type": "string"}, "content_base64": map[string]interface{}{"type": "string"}}, []string{"format", "content_base64"})), inspect)
	return s
}
func health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"status": "ok", "version": serviceVersion}), nil
}
func capabilities(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"version": "v1", "create_formats": []string{"markdown", "html", "csv", "json", "pdf"}, "patch_formats": []string{"markdown", "html", "json"}, "preview_formats": []string{"pdf"}, "max_artifact_bytes": maxArtifactBytes, "storage_integration": false}), nil
}
func create(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	format, _ := arguments["format"].(string)
	created, err := artifactengine.Create(format, arguments["content"])
	if err != nil {
		return failure("artifact_create_failed", err.Error()), nil
	}
	if created.Manifest.Bytes > maxArtifactBytes {
		return failure("storage_required", "artifact exceeds the inline limit"), nil
	}
	return result(map[string]interface{}{"status": "ok", "artifact": created}), nil
}
func patchArtifact(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input struct {
		Format  string                 `json:"format"`
		Content string                 `json:"content"`
		Patches []artifactengine.Patch `json:"patches"`
	}
	if err := decode(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	if len(input.Content) > maxArtifactBytes {
		return failure("invalid_arguments", "content exceeds the size limit"), nil
	}
	patched, err := artifactengine.ApplyPatch(input.Format, input.Content, input.Patches)
	if err != nil {
		return failure("artifact_patch_failed", err.Error()), nil
	}
	return result(map[string]interface{}{"status": "ok", "artifact": patched}), nil
}
func render(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	format, _ := arguments["format"].(string)
	content, _ := arguments["content"].(string)
	output, _ := arguments["output_format"].(string)
	if len(content) > maxArtifactBytes {
		return failure("invalid_arguments", "content exceeds the size limit"), nil
	}
	rendered, err := artifactengine.Render(format, content, output)
	if err != nil {
		return failure("artifact_render_failed", err.Error()), nil
	}
	return result(map[string]interface{}{"status": "ok", "artifact": rendered}), nil
}
func inspect(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	format, _ := arguments["format"].(string)
	encoded, _ := arguments["content_base64"].(string)
	if len(encoded) > maxArtifactBytes*2 {
		return failure("invalid_arguments", "encoded artifact exceeds the size limit"), nil
	}
	payload, err := artifactengine.DecodeContent(encoded)
	if err != nil {
		return failure("invalid_artifact", err.Error()), nil
	}
	manifest, err := artifactengine.Inspect(strings.ToLower(format), payload)
	if err != nil {
		return failure("invalid_artifact", err.Error()), nil
	}
	return result(map[string]interface{}{"status": "ok", "manifest": manifest}), nil
}
func decode(request mcp.CallToolRequest, target interface{}) error {
	payload, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("arguments do not match schema: %w", err)
	}
	return nil
}
func readTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := true, false, true
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}
func writeTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := false, false, true
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

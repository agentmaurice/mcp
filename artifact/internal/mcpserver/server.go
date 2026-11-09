package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	artifactengine "github.com/agentmaurice/mcpchatui/mcp/artifact/internal/artifact"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/version"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

var serviceVersion = version.Version

const maxArtifactBytes = 2 * 1024 * 1024

func New() *modernmcp.Server { return NewWithDeps(DepsFromEnv()) }

// NewWithDeps builds the server with explicit dependencies (tests).
func NewWithDeps(deps Deps) *modernmcp.Server {
	h := &handlers{deps: deps}
	s := modernmcp.New("agentmaurice-artifact", serviceVersion, "Use Artifact to create, patch, render and inspect bounded deliverables with a SHA-256 provenance manifest.")
	s.AddTool(readTool("artifact_health_v1", "Check the Artifact MCP.", schema(nil, nil)), h.health)
	s.AddTool(readTool("artifact_capabilities_v1", "Describe formats, rendering engines, template contract and limits.", schema(nil, nil)), h.capabilities)
	s.AddTool(writeTool("artifact_create_v1", "Create a deterministic bounded artifact.", schema(map[string]interface{}{"format": map[string]interface{}{"type": "string", "enum": []string{"markdown", "html", "csv", "json", "pdf"}}, "content": map[string]interface{}{}}, []string{"format", "content"})), h.create)
	s.AddTool(writeTool("artifact_patch_v1", "Apply ordered replace, append or prepend operations.", schema(map[string]interface{}{"format": map[string]interface{}{"type": "string", "enum": []string{"markdown", "html", "json"}}, "content": map[string]interface{}{"type": "string", "maxLength": maxArtifactBytes}, "patches": map[string]interface{}{"type": "array", "maxItems": 100, "items": map[string]interface{}{"type": "object"}}}, []string{"format", "content", "patches"})), h.patchArtifact)
	s.AddTool(writeTool("artifact_render_v1", renderDescription, renderSchema()), h.render)
	s.AddTool(readTool("artifact_inspect_v1", "Validate an artifact and return its provenance manifest.", schema(map[string]interface{}{"format": map[string]interface{}{"type": "string"}, "content_base64": map[string]interface{}{"type": "string"}}, []string{"format", "content_base64"})), h.inspect)
	return s
}
func (h *handlers) health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"status": "ok", "version": serviceVersion}), nil
}
func (h *handlers) create(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
func (h *handlers) patchArtifact(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

// render dispatches: a call with a `template` is the v0.2 template render; a
// call without it keeps the v0.1 contract (format, content, output_format).
func (h *handlers) render(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if _, ok := request.GetArguments()["template"]; ok {
		return h.renderTemplate(ctx, request)
	}
	// Utility outputs (ics, vcf, csv): typed data, no template.
	if output, _ := request.GetArguments()["output"].(string); output != "" {
		if _, _, ok := utilityOutput(output); ok {
			return h.renderUtility(ctx, request, output)
		}
	}
	return renderLegacy(request)
}

func renderLegacy(request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
func (h *handlers) inspect(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

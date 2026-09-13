package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	dataengine "github.com/agentmaurice/mcpchatui/mcp/data/internal/data"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	serviceVersion = "0.1.0"
	maxInputBytes  = 4 * 1024 * 1024
	maxInlineBytes = 512 * 1024
)

type sourceInput struct {
	Format  string `json:"format"`
	Content string `json:"content"`
}
type queryInput struct {
	Format  string               `json:"format"`
	Content string               `json:"content"`
	Plan    dataengine.QueryPlan `json:"plan"`
}
type exportInput struct {
	Format       string               `json:"format"`
	Content      string               `json:"content"`
	Plan         dataengine.QueryPlan `json:"plan"`
	OutputFormat string               `json:"output_format"`
}

func New() *modernmcp.Server {
	s := modernmcp.New("agentmaurice-data", serviceVersion, "Use Data for bounded CSV and tabular JSON profiling and closed-plan queries. SQL text is never accepted.")
	s.AddTool(tool("data_health_v1", "Check the Data MCP.", schema(nil, nil)), health)
	s.AddTool(tool("data_capabilities_v1", "Describe supported formats, query operations and limits.", schema(nil, nil)), capabilities)
	base := map[string]interface{}{"format": map[string]interface{}{"type": "string", "enum": []string{"csv", "json"}}, "content": map[string]interface{}{"type": "string", "maxLength": maxInputBytes}}
	s.AddTool(tool("data_profile_v1", "Infer columns and calculate bounded profiles.", schema(base, []string{"format", "content"})), profile)
	plan := map[string]interface{}{"type": "object", "description": "Closed JSON plan: select, filters, sort, limit and aggregations"}
	queryProps := clone(base)
	queryProps["plan"] = plan
	s.AddTool(tool("data_query_v1", "Apply a closed JSON query plan; arbitrary SQL is not accepted.", schema(queryProps, []string{"format", "content", "plan"})), query)
	exportProps := clone(queryProps)
	exportProps["output_format"] = map[string]interface{}{"type": "string", "enum": []string{"csv", "json"}}
	s.AddTool(tool("data_export_v1", "Export a closed-plan result as bounded CSV or JSON.", schema(exportProps, []string{"format", "content", "plan", "output_format"})), exportData)
	return s
}

func health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"status": "ok", "version": serviceVersion}), nil
}
func capabilities(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"version": "v1", "formats": []string{"csv", "json"}, "max_input_bytes": maxInputBytes, "max_rows": dataengine.MaxRows, "max_columns": dataengine.MaxColumns, "filter_ops": []string{"eq", "ne", "gt", "gte", "lt", "lte", "contains"}, "aggregation_ops": []string{"count", "sum", "avg", "min", "max"}, "sql": false, "max_inline_bytes": maxInlineBytes}), nil
}
func profile(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input sourceInput
	if err := decode(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	dataset, err := parse(input.Format, input.Content)
	if err != nil {
		return failure("invalid_dataset", err.Error()), nil
	}
	return result(map[string]interface{}{"status": "ok", "rows": len(dataset.Rows), "columns": dataengine.Profile(dataset)}), nil
}
func query(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input queryInput
	if err := decode(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	dataset, err := parse(input.Format, input.Content)
	if err != nil {
		return failure("invalid_dataset", err.Error()), nil
	}
	queried, err := dataengine.Query(dataset, input.Plan)
	if err != nil {
		return failure("invalid_plan", err.Error()), nil
	}
	payload := map[string]interface{}{"status": "ok", "columns": queried.Columns, "rows": queried.Rows, "row_count": len(queried.Rows)}
	encoded, _ := json.Marshal(payload)
	if len(encoded) > maxInlineBytes {
		return failure("buffer_required", "query result exceeds the inline limit"), nil
	}
	return result(payload), nil
}
func exportData(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input exportInput
	if err := decode(request, &input); err != nil {
		return failure("invalid_arguments", err.Error()), nil
	}
	dataset, err := parse(input.Format, input.Content)
	if err != nil {
		return failure("invalid_dataset", err.Error()), nil
	}
	queried, err := dataengine.Query(dataset, input.Plan)
	if err != nil {
		return failure("invalid_plan", err.Error()), nil
	}
	payload, mime, err := dataengine.Export(queried, input.OutputFormat)
	if err != nil {
		return failure("invalid_export", err.Error()), nil
	}
	if len(payload) > maxInlineBytes {
		return failure("buffer_required", "export exceeds the inline limit"), nil
	}
	hash := sha256.Sum256(payload)
	return result(map[string]interface{}{"status": "ok", "mime_type": mime, "content": string(payload), "bytes": len(payload), "sha256": fmt.Sprintf("%x", hash[:])}), nil
}
func parse(format, content string) (dataengine.Dataset, error) {
	if strings.TrimSpace(content) == "" {
		return dataengine.Dataset{}, fmt.Errorf("content is required")
	}
	if len(content) > maxInputBytes {
		return dataengine.Dataset{}, fmt.Errorf("content exceeds %d bytes", maxInputBytes)
	}
	return dataengine.Parse(format, content)
}
func decode(request mcp.CallToolRequest, target interface{}) error {
	payload, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}
func clone(source map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{}
	for key, value := range source {
		result[key] = value
	}
	return result
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

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/observe/internal/observe"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	serviceVersion = "0.2.0"
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
	cohort := schema(map[string]interface{}{
		"reports":          map[string]interface{}{"type": "array", "maxItems": observe.CohortMaxReports, "description": "Inline redacted agentmaurice.execution_diagnostic/v1 reports with optional events"},
		"baseline_reports": map[string]interface{}{"type": "array", "maxItems": observe.CohortMaxReports, "description": "Optional second inline redacted cohort; candidate plus baseline are limited to 1000 reports combined"},
		"window":           map[string]interface{}{"type": "object", "description": "Optional RFC3339 start/end bounds"},
		"workflow_id":      map[string]interface{}{"type": "string"}, "workflow_version": map[string]interface{}{"type": "string"},
		"model": map[string]interface{}{"type": "string"}, "status": map[string]interface{}{"type": "string"},
		"group_by": map[string]interface{}{"type": "array", "maxItems": observe.CohortMaxGroupBy, "items": map[string]interface{}{"type": "string", "enum": []string{"workflow_id", "workflow_version", "model", "status"}}},
	}, []string{"reports"})
	s.AddTool(tool("observe_query_v1", "Compute bounded cohort metrics from inline redacted execution diagnostics; no backend access.", cohort), query)
	return s
}

func health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]interface{}{"status": "ok", "version": serviceVersion}), nil
}

func capabilities(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limits := observe.InputLimits()
	limits["max_reports"] = observe.CohortMaxReports
	limits["max_events_per_report"] = observe.CohortMaxEventsPerReport
	limits["max_groups"] = observe.CohortMaxGroups
	limits["max_group_by"] = observe.CohortMaxGroupBy
	return result(map[string]interface{}{"version": "v1", "formats": []string{"otlp-json", "run-summary-json", "execution-diagnostic-json"}, "diagnostic_schema": observe.DiagnosticSchemaVersion, "direct_backends": false, "redacted_input_required": true, "cohort_limits": limits, "max_trace_bytes": maxTraceBytes}), nil
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

func query(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, ok := request.GetArguments()["reports"]
	if !ok {
		return failure("invalid_query", "reports is required"), nil
	}
	baselineRaw := request.GetArguments()["baseline_reports"]
	if rawBaseline, present := request.GetArguments()["baseline_reports"]; present {
		if values, ok := rawBaseline.([]interface{}); ok && len(values) == 0 {
			return failure("invalid_query", "baseline_reports must not be empty"), nil
		}
	}
	if err := validateFilterArguments(queryCtx, request.GetArguments()); err != nil {
		if queryCtx.Err() != nil {
			return failure("query_timeout", "cohort query exceeded its 2 second budget"), nil
		}
		return failure("invalid_query", err.Error()), nil
	}
	if err := observe.ValidateCohortInput(queryCtx, raw, baselineRaw); err != nil {
		if queryCtx.Err() != nil {
			return failure("query_timeout", "cohort query exceeded its 2 second budget"), nil
		}
		return failure("invalid_query", err.Error()), nil
	}
	encoded, err := json.Marshal(map[string]interface{}{
		"reports": raw, "window": request.GetArguments()["window"], "workflow_id": request.GetArguments()["workflow_id"],
		"baseline_reports": baselineRaw,
		"workflow_version": request.GetArguments()["workflow_version"], "model": request.GetArguments()["model"],
		"status": request.GetArguments()["status"], "group_by": request.GetArguments()["group_by"],
	})
	if err != nil {
		return failure("invalid_query", err.Error()), nil
	}
	var input observe.CohortQuery
	if err := json.Unmarshal(encoded, &input); err != nil {
		return failure("invalid_query", err.Error()), nil
	}
	if baselineRaw, present := request.GetArguments()["baseline_reports"]; present && baselineRaw != nil {
		baselineJSON, marshalErr := json.Marshal(baselineRaw)
		if marshalErr != nil || json.Unmarshal(baselineJSON, &input.BaselineReports) != nil {
			return failure("invalid_query", "baseline_reports must be an array of diagnostic reports"), nil
		}
	}
	payload, err := observe.QueryCohortContext(queryCtx, input)
	if err != nil {
		if strings.Contains(err.Error(), "cohort query timeout") || queryCtx.Err() != nil {
			return failure("query_timeout", "cohort query exceeded its 2 second budget"), nil
		}
		return failure("invalid_query", err.Error()), nil
	}
	return result(payload), nil
}

func validateFilterArguments(ctx context.Context, args map[string]interface{}) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("cohort filter validation timeout")
	default:
	}
	for _, key := range []string{"workflow_id", "workflow_version", "model", "status"} {
		if value, present := args[key]; present && value != nil {
			text, ok := value.(string)
			if !ok || len(text) > 2048 {
				return fmt.Errorf("invalid cohort filter")
			}
		}
	}
	if raw, present := args["window"]; present && raw != nil {
		window, ok := raw.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid cohort window")
		}
		for key, value := range window {
			if key != "start" && key != "end" {
				return fmt.Errorf("invalid cohort window")
			}
			text, ok := value.(string)
			if !ok || len(text) > 64 {
				return fmt.Errorf("invalid cohort window")
			}
		}
	}
	if raw, present := args["group_by"]; present && raw != nil {
		groupBy, ok := raw.([]interface{})
		if !ok || len(groupBy) > observe.CohortMaxGroupBy {
			return fmt.Errorf("invalid cohort grouping")
		}
		allowed := map[string]bool{"workflow_id": true, "workflow_version": true, "model": true, "status": true}
		seen := map[string]bool{}
		for _, value := range groupBy {
			text, ok := value.(string)
			if !ok || !allowed[text] || seen[text] {
				return fmt.Errorf("invalid cohort grouping")
			}
			seen[text] = true
		}
	}
	return nil
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

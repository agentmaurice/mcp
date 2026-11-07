package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/observe/internal/observe"
	"github.com/mark3labs/mcp-go/mcp"
)

const diagnosticSummaryPayload = `{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"exec-1","workflow_id":"wf-1","state":"completed","usage":{"duration_ms":12},"coverage":{"partial":false,"values_redacted":true},"events":[{"action_id":"lifecycle-1","action_type":"lifecycle","status":"ok","duration_ms":1},{"action_id":"action-1","action_type":"tool_call","source":"runtime","status":"ok","start_ms":1,"end_ms":9,"duration_ms":8}]}`

func diagnosticRequest(name string, arguments map[string]interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: arguments}}
}

func TestDiagnosticSummaryIsAcceptedByRunExplainAndCompare(t *testing.T) {
	request := diagnosticRequest("observe_run_v1", map[string]interface{}{"content": diagnosticSummaryPayload})
	runResult, err := run(context.Background(), request)
	if err != nil || runResult.IsError {
		t.Fatalf("run rejected execution diagnostic summary: %#v %v", runResult, err)
	}
	if _, ok := runResult.StructuredContent.(map[string]interface{}); !ok {
		t.Fatalf("run result is not structured: %#v", runResult.StructuredContent)
	}

	explainResult, err := explain(context.Background(), request)
	if err != nil || explainResult.IsError {
		t.Fatalf("explain rejected execution diagnostic summary: %#v %v", explainResult, err)
	}
	compareRequest := diagnosticRequest("observe_compare_v1", map[string]interface{}{"baseline": diagnosticSummaryPayload, "candidate": diagnosticSummaryPayload})
	compareResult, err := compare(context.Background(), compareRequest)
	if err != nil || compareResult.IsError {
		t.Fatalf("compare rejected execution diagnostic summaries: %#v %v", compareResult, err)
	}
}

func TestQueryAcceptsOptionalBaselineAndReturnsComparison(t *testing.T) {
	candidate := map[string]interface{}{"schema_version": "agentmaurice.execution_diagnostic/v1", "execution_id": "candidate", "workflow_id": "wf", "state": "completed", "duration_ms": 30, "events": []interface{}{map[string]interface{}{"action_type": "tool_call", "cost_usd": 1.0}}}
	baseline := map[string]interface{}{"schema_version": "agentmaurice.execution_diagnostic/v1", "execution_id": "baseline", "workflow_id": "wf", "state": "completed", "duration_ms": 20, "events": []interface{}{map[string]interface{}{"action_type": "tool_call", "cost_usd": 1.0}}}
	result, err := query(context.Background(), diagnosticRequest("observe_query_v1", map[string]interface{}{"reports": []interface{}{candidate}, "baseline_reports": []interface{}{baseline}, "workflow_id": "wf", "group_by": []interface{}{"workflow_id"}}))
	if err != nil || result.IsError {
		t.Fatalf("baseline query failed: %#v %v", result, err)
	}
	payload, ok := result.StructuredContent.(observe.CohortResult)
	if !ok || payload.Comparison == nil || len(payload.Comparison.Deltas) != 1 {
		t.Fatalf("comparison missing from structured result: %#v", result.StructuredContent)
	}
}

func TestQueryRejectsExplicitlyEmptyBaseline(t *testing.T) {
	request := diagnosticRequest("observe_query_v1", map[string]interface{}{"reports": []interface{}{map[string]interface{}{"execution_id": "candidate"}}, "baseline_reports": []interface{}{}})
	result, err := query(context.Background(), request)
	if err != nil || !result.IsError {
		t.Fatalf("empty baseline was silently ignored: %#v %v", result, err)
	}
	payload := result.StructuredContent.(map[string]interface{})
	if payload["error"] != "invalid_query" {
		t.Fatalf("unexpected empty baseline error: %#v", payload)
	}
}

func TestQueryRejectsOversizedRawCorpusBeforeDecode(t *testing.T) {
	reports := make([]interface{}, observe.CohortMaxReports+1)
	request := diagnosticRequest("observe_query_v1", map[string]interface{}{"reports": reports})
	result, err := query(context.Background(), request)
	if err != nil || !result.IsError {
		t.Fatalf("oversized corpus was not rejected: %#v %v", result, err)
	}
	if payload, ok := result.StructuredContent.(map[string]interface{}); !ok || payload["error"] != "invalid_query" {
		t.Fatalf("unexpected oversized-corpus response: %#v", result.StructuredContent)
	}
}

func TestQueryRejectsOversizedEventArrayBeforeDecode(t *testing.T) {
	events := make([]interface{}, observe.CohortMaxEventsPerReport+1)
	request := diagnosticRequest("observe_query_v1", map[string]interface{}{"reports": []interface{}{map[string]interface{}{"events": events}}})
	result, err := query(context.Background(), request)
	if err != nil || !result.IsError {
		t.Fatalf("oversized event array was not rejected: %#v %v", result, err)
	}
}

func TestQueryRejectsUnboundedFilterBeforeMarshalWithoutEcho(t *testing.T) {
	request := diagnosticRequest("observe_query_v1", map[string]interface{}{"reports": []interface{}{map[string]interface{}{}}, "model": strings.Repeat("secret", 1000)})
	result, err := query(context.Background(), request)
	if err != nil || !result.IsError || strings.Contains(fmt.Sprint(result.StructuredContent), "secret") {
		t.Fatalf("oversized model filter was not safely rejected: %#v %v", result, err)
	}
	window := diagnosticRequest("observe_query_v1", map[string]interface{}{"reports": []interface{}{map[string]interface{}{}}, "window": map[string]interface{}{"start": strings.Repeat("x", 65)}})
	if result, err := query(context.Background(), window); err != nil || !result.IsError {
		t.Fatalf("malicious window filter was not rejected: %#v %v", result, err)
	}
}

func TestCapabilitiesAdvertiseDiagnosticSchemaAndCohortLimits(t *testing.T) {
	result, err := capabilities(context.Background(), diagnosticRequest("observe_capabilities_v1", nil))
	if err != nil || result.IsError {
		t.Fatalf("capabilities failed: %#v %v", result, err)
	}
	payload := result.StructuredContent.(map[string]interface{})
	formats := fmt.Sprint(payload["formats"])
	if !strings.Contains(formats, "execution-diagnostic-json") || payload["diagnostic_schema"] != observe.DiagnosticSchemaVersion || payload["direct_backends"] != false {
		t.Fatalf("capabilities omitted diagnostic contract: %#v", payload)
	}
	limits := payload["cohort_limits"].(map[string]int)
	if limits["max_input_nodes"] != 100000 || limits["max_string_bytes"] != 8*1024*1024 {
		t.Fatalf("capabilities omitted structural input limits: %#v", limits)
	}
}

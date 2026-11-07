package observe

import (
	"encoding/json"
	"testing"
)

// TestWorkflowDiagnosticsCohortAlgorithm exercises bounded Observe
// aggregations against the execution_diagnostic/v1 JSON shape. This is an
// algorithm/unit corpus only; runtime WE10 qualification is a separate
// host-platform procedure and is not claimed by this test.
func TestWorkflowDiagnosticsCohortAlgorithm(t *testing.T) {
	corpus := []string{
		`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"latency-base","workflow_id":"checkout","workflow_version":"1.0","model":"m1","state":"completed","duration_ms":10,"events":[{"action_type":"execution","source":"diagnostic_lifecycle","start_ms":100,"end_ms":101,"duration_ms":1},{"action_id":"run","action_type":"code_execution","source":"artifact","status":"ok","start_ms":100,"end_ms":110,"duration_ms":10,"cost_usd":0.10}]}`,
		`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"latency-regressed","workflow_id":"checkout","workflow_version":"2.0","model":"m1","state":"completed","duration_ms":30,"events":[{"action_type":"execution","source":"diagnostic_lifecycle","start_ms":100,"end_ms":101,"duration_ms":1,"cost_usd":9.0},{"action_id":"run","action_type":"code_execution","source":"artifact","status":"ok","start_ms":100,"end_ms":130,"duration_ms":30,"cost_usd":0.20}]}`,
		`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"cache-inefficient","workflow_id":"catalog","workflow_version":"1.0","model":"m1","state":"completed","duration_ms":20,"events":[{"action_type":"tool_call","source":"runtime","name":"cache_read","status":"ok","duration_ms":20}]}`,
		`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"retry-run","workflow_id":"payments","workflow_version":"1.0","model":"m1","state":"completed","duration_ms":18,"events":[{"action_id":"charge-attempt-1","action_type":"tool_call","source":"runtime","status":"error","duration_ms":8,"cost_usd":0.03},{"action_id":"charge-attempt-2","action_type":"tool_call","source":"runtime","status":"ok","duration_ms":10}]}`,
		`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"retry-run","workflow_id":"payments","workflow_version":"1.0","model":"m1","state":"completed","duration_ms":18,"events":[{"action_id":"charge-attempt-1","action_type":"tool_call","source":"runtime","status":"error","duration_ms":8,"cost_usd":0.03},{"action_id":"charge-attempt-2","action_type":"tool_call","source":"runtime","status":"ok","duration_ms":10}]}`,
	}
	reports := make([]DiagnosticReport, 0, len(corpus))
	for _, raw := range corpus {
		var report DiagnosticReport
		if err := json.Unmarshal([]byte(raw), &report); err != nil {
			t.Fatal(err)
		}
		reports = append(reports, report)
	}

	t.Run("latency regression run explain compare", func(t *testing.T) {
		baseline, err := Parse(corpus[0])
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := Parse(corpus[1])
		if err != nil {
			t.Fatal(err)
		}
		if got := Explain(candidate)["summary"].(Summary).DurationMS; got != 30 {
			t.Fatalf("candidate duration=%v, want 30", got)
		}
		comparison := Compare(baseline, candidate)
		if comparison["duration_delta_ms"] != float64(20) {
			t.Fatalf("duration comparison=%v, want 20", comparison)
		}
	})

	t.Run("cohort filters quantiles costs and coverage", func(t *testing.T) {
		result, err := QueryCohort(CohortQuery{Reports: reports, WorkflowID: "checkout", GroupBy: []string{"workflow_version"}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Coverage.ReportsMatched != 2 || result.Coverage.EventsSeen != 7 || result.Coverage.EventsMatched != 4 {
			t.Fatalf("unexpected checkout coverage: %#v", result.Coverage)
		}
		if len(result.Groups) != 2 || result.Groups[0].LatencyMS.P50 == 0 || result.Groups[1].LatencyMS.P95 == 0 {
			t.Fatalf("latency quantiles missing: %#v", result.Groups)
		}
		for _, group := range result.Groups {
			if group.Cost.KnownCount != 1 || group.Cost.UnknownCount != 0 {
				t.Fatalf("lifecycle cost was not excluded: %#v", group.Cost)
			}
		}
	})

	t.Run("cache and retries remain explicitly bounded", func(t *testing.T) {
		cacheResult, err := QueryCohort(CohortQuery{Reports: reports, WorkflowID: "catalog"})
		if err != nil {
			t.Fatal(err)
		}
		if cacheResult.Groups[0].EventCount != 1 || cacheResult.Groups[0].Cost.UnknownCount != 1 {
			t.Fatalf("cache cohort mismatch: %#v", cacheResult.Groups[0])
		}
		retryResult, err := QueryCohort(CohortQuery{Reports: reports, WorkflowID: "payments"})
		if err != nil {
			t.Fatal(err)
		}
		if retryResult.Groups[0].Cost.KnownCount != 1 || retryResult.Groups[0].Cost.UnknownCount != 1 {
			t.Fatalf("retry known/unknown cost mismatch: %#v", retryResult.Groups[0].Cost)
		}
		// execution_diagnostic/v1 has no cache-hit or retry-attempt field; the
		// action IDs are retained as metadata only and are not inferred here.
	})

	t.Run("replay is explicitly deduplicated", func(t *testing.T) {
		result, err := QueryCohort(CohortQuery{Reports: reports, WorkflowID: "payments"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Groups[0].ExecutionCount != 1 || result.Groups[0].Cost.KnownCount != 1 || result.Groups[0].Cost.UnknownCount != 1 {
			t.Fatalf("replay behavior changed unexpectedly: %#v", result.Groups[0])
		}
		if result.Coverage.ReportsDeduplicated != 1 {
			t.Fatalf("replay deduplication was not recorded: %#v", result.Coverage)
		}
	})
}

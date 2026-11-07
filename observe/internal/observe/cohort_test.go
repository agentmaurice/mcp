package observe

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestQueryCohortFiltersGroupsAndComputesNearestRankQuantiles(t *testing.T) {
	cost := 0.25
	result, err := QueryCohort(CohortQuery{
		Reports: []DiagnosticReport{
			{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "1", WorkflowID: "wf", WorkflowVersion: "1", Model: "m", Status: "completed", DurationMS: 10, Events: []DiagnosticEvent{{CostUSD: &cost}}},
			{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "2", WorkflowID: "wf", WorkflowVersion: "1", Model: "m", Status: "completed", DurationMS: 30, Events: []DiagnosticEvent{{Status: "error"}}},
			{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "3", WorkflowID: "other", Status: "failed", DurationMS: 99},
		},
		WorkflowID: "wf", GroupBy: []string{"workflow_id", "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 1 || result.Coverage.ReportsMatched != 2 || result.Coverage.CostUnknownCount != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	group := result.Groups[0]
	if group.LatencyMS.Count != 2 || group.LatencyMS.P50 != 10 || group.LatencyMS.P95 != 30 || group.ErrorCount != 0 {
		t.Fatalf("unexpected metrics: %#v", group)
	}
	if group.Cost.KnownUSD != .25 || !group.Cost.Incomplete || !result.Provenance.RedactedOnly || result.Provenance.BackendAccess {
		t.Fatalf("unexpected cost/provenance: %#v %#v", group.Cost, result.Provenance)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"execution_id"`) || strings.Contains(string(encoded), `"events"`) {
		t.Fatalf("result leaked raw report fields: %s", encoded)
	}
}

func TestQueryCohortRejectsLimitsAndUnsupportedDimensions(t *testing.T) {
	report := DiagnosticReport{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "1"}
	tooMany := make([]DiagnosticReport, CohortMaxReports+1)
	tooMany[0] = report
	if _, err := QueryCohort(CohortQuery{Reports: tooMany}); err == nil {
		t.Fatal("expected report limit")
	}
	if _, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{report}, GroupBy: []string{"workflow_id", "model", "status", "extra"}}); err == nil {
		t.Fatal("expected group_by validation")
	}
}

func TestQueryCohortRejectsMissingOrForeignSchema(t *testing.T) {
	if _, err := QueryCohort(CohortQuery{}); err == nil {
		t.Fatal("expected empty corpus error")
	}
	if _, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{SchemaVersion: "other/v1", ExecutionID: "1"}}}); err == nil {
		t.Fatal("expected schema error")
	} else if strings.Contains(err.Error(), "other/v1") {
		t.Fatalf("unsupported schema echoed untrusted value: %v", err)
	}
	var missing DiagnosticReport
	if err := json.Unmarshal([]byte(`{"execution_id":"missing-schema","state":"completed"}`), &missing); err != nil {
		t.Fatal(err)
	}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{missing}})
	if err != nil || !result.Coverage.Partial || !containsReason(result.Coverage.Reasons, "missing_schema_version") {
		t.Fatalf("missing schema was not explicit partial coverage: %#v %v", result, err)
	}
}

func TestQueryCohortCountsAnErroredExecutionOnce(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{
		SchemaVersion: DiagnosticSchemaVersion,
		ExecutionID:   "errored",
		Status:        "error",
		Events:        []DiagnosticEvent{{Status: "error"}, {Status: "error"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Groups[0].ErrorCount; got != 1 {
		t.Fatalf("error count = %d, want one errored execution", got)
	}
}

func TestQueryCohortExcludesLifecycleCostAndPropagatesPartialCoverage(t *testing.T) {
	cost := 0.5
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{
		SchemaVersion: DiagnosticSchemaVersion,
		ExecutionID:   "partial-cost",
		DurationMS:    20,
		Coverage:      DiagnosticCoverage{Partial: true, EventsTruncated: true},
		Events: []DiagnosticEvent{
			{ActionType: "lifecycle", CostUSD: &cost},
			{ActionType: "diagnostic_step", CostUSD: &cost},
			{ActionType: "tool_call", CostUSD: &cost},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.Cost.KnownCount != 1 || group.Cost.KnownUSD != cost || !group.Cost.Incomplete {
		t.Fatalf("lifecycle costs were counted or completeness was lost: %#v", group.Cost)
	}
	if result.Status != "partial" || result.Coverage.Complete || !result.Coverage.Partial || !result.Coverage.EventsTruncated {
		t.Fatalf("partial coverage was announced complete: %#v", result.Coverage)
	}
}

func TestQueryCohortExcludesDiagnosticSourcesAndPreservesSource(t *testing.T) {
	var report DiagnosticReport
	if err := json.Unmarshal([]byte(`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"source-1","duration_ms":12,"events":[{"action_type":"execution","source":"diagnostic_lifecycle","cost_usd":0.5,"duration_ms":1},{"action_type":"code_execution","source":"diagnostic_step","cost_usd":0.75,"duration_ms":2},{"action_type":"code_execution","source":"artifact","cost_usd":1.25,"duration_ms":9}]}`), &report); err != nil {
		t.Fatal(err)
	}
	if report.Events[0].Source != "diagnostic_lifecycle" || report.Events[2].Source != "artifact" {
		t.Fatalf("diagnostic source was not loaded: %#v", report.Events)
	}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{report}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.Cost.KnownCount != 1 || group.Cost.KnownUSD != 1.25 {
		t.Fatalf("diagnostic source costs were counted: %#v", group.Cost)
	}
	if group.EventCount != 3 || result.Coverage.EventsSeen != 3 {
		t.Fatalf("coverage must retain all input events while metrics exclude meta events: %#v %#v", group, result.Coverage)
	}
}

func TestQueryCohortRetainsToolNamedLifecycle(t *testing.T) {
	cost := 0.25
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "tool-lifecycle", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "tool_call", Name: "lifecycle", Source: "runtime", CostUSD: &cost}}}}})
	if err != nil || result.Groups[0].EventCount != 1 || result.Groups[0].Cost.KnownCount != 1 {
		t.Fatalf("tool action named lifecycle was incorrectly dropped: %#v %v", result, err)
	}
}

func TestQueryCohortDeduplicatesIdenticalTerminalSnapshots(t *testing.T) {
	cost := 0.25
	report := DiagnosticReport{SchemaVersion: DiagnosticSchemaVersion, AgentID: "agent-a", ExecutionID: "exec-1", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "tool_call", Source: "runtime", CostUSD: &cost}}}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{report, report}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.ExecutionCount != 1 || group.Cost.KnownCount != 1 || result.Coverage.ReportsDeduplicated != 1 || !result.Coverage.Complete {
		t.Fatalf("identical replay was not collapsed: %#v %#v", group, result.Coverage)
	}
}

func TestQueryCohortChoosesLatestSnapshotAndSeparatesAgents(t *testing.T) {
	older := DiagnosticReport{SchemaVersion: DiagnosticSchemaVersion, AgentID: "agent-a", ExecutionID: "exec-1", Status: "running", UpdatedAt: "2026-10-26T10:00:00Z", DurationMS: 2}
	newer := older
	newer.Status = "completed"
	newer.DurationMS = 20
	newer.UpdatedAt = "2026-10-26T10:01:00Z"
	otherAgent := older
	otherAgent.AgentID = "agent-b"
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{older, newer, otherAgent}, GroupBy: []string{"status"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.ReportsMatched != 2 || result.Coverage.ReportsDeduplicated != 1 || result.Coverage.DuplicateConflicts != 0 {
		t.Fatalf("snapshot/agent identity mismatch: %#v", result.Coverage)
	}
	if len(result.Groups) != 2 {
		t.Fatalf("latest snapshot was not selected: %#v", result.Groups)
	}
}

func TestQueryCohortMarksContradictoryUntimestampedSnapshotPartial(t *testing.T) {
	first := DiagnosticReport{SchemaVersion: DiagnosticSchemaVersion, AgentID: "agent-a", ExecutionID: "exec-1", Status: "running"}
	second := first
	second.Status = "failed"
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.DuplicateConflicts != 1 || result.Status != "partial" || result.Coverage.Complete || len(result.Coverage.Reasons) != 2 || result.Coverage.Reasons[0] != "duplicate_conflict" {
		t.Fatalf("untimestamped conflict was not surfaced: %#v", result.Coverage)
	}
}

func TestQueryCohortAggregatesKnownAndUnknownCacheMetadata(t *testing.T) {
	readBase, createBase := int64(80), int64(10)
	readCandidate, createCandidate := int64(20), int64(5)
	readOnly := int64(80)
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{
		{SchemaVersion: DiagnosticSchemaVersion, AgentID: "agent-a", ExecutionID: "cache-base", Events: []DiagnosticEvent{{ActionType: "llm_call", Source: "llm", AttemptID: "attempt-1", RecordedAtMS: 1793000000000, CacheReadTokens: &readBase, CacheCreationTokens: &createBase}}},
		{SchemaVersion: DiagnosticSchemaVersion, AgentID: "agent-a", ExecutionID: "cache-candidate", Events: []DiagnosticEvent{{ActionType: "llm_call", Source: "llm", CacheReadTokens: &readCandidate, CacheCreationTokens: &createCandidate}}},
		{SchemaVersion: DiagnosticSchemaVersion, AgentID: "agent-a", ExecutionID: "cache-unknown", Events: []DiagnosticEvent{{ActionType: "llm_call", Source: "llm"}}},
		{SchemaVersion: DiagnosticSchemaVersion, AgentID: "agent-a", ExecutionID: "cache-read-only", Events: []DiagnosticEvent{{ActionType: "llm_call", Source: "llm", CacheReadTokens: &readOnly}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.CacheReadTokensTotal != 180 || group.CacheCreationTokensTotal != 15 || group.CacheKnownEvents != 3 || group.CacheUnknownLLMEvents != 1 || group.CacheUnknownReadEvents != 1 || group.CacheUnknownCreationEvents != 2 {
		t.Fatalf("cache aggregation mismatch: %#v", group)
	}
	if len(result.Filters.Reports) != 0 {
		t.Fatal("cohort result must not echo raw reports")
	}
}

func TestQueryCohortComparesBoundedBaselineWithUnknownCost(t *testing.T) {
	baseCost := 1.0
	result, err := QueryCohort(CohortQuery{
		Reports: []DiagnosticReport{
			{SchemaVersion: DiagnosticSchemaVersion, AgentID: "a", ExecutionID: "candidate-1", WorkflowID: "wf", Status: "completed", DurationMS: 30, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: &baseCost}}},
			{SchemaVersion: DiagnosticSchemaVersion, AgentID: "a", ExecutionID: "candidate-2", WorkflowID: "wf", Status: "error", DurationMS: 50, Events: []DiagnosticEvent{{ActionType: "tool_call"}}},
		},
		BaselineReports: []DiagnosticReport{{
			SchemaVersion: DiagnosticSchemaVersion, AgentID: "a", ExecutionID: "baseline-1", WorkflowID: "wf", Status: "completed", DurationMS: 20,
			Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: &baseCost}},
		}},
		WorkflowID: "wf", GroupBy: []string{"workflow_id"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Comparison == nil || result.Comparison.Baseline == nil || result.Comparison.Candidate == nil || len(result.Comparison.Deltas) != 1 {
		t.Fatalf("missing comparison: %#v", result.Comparison)
	}
	delta := result.Comparison.Deltas[0]
	if delta.ExecutionCountDelta != 1 || delta.ErrorRateDelta == nil || *delta.ErrorRateDelta != .5 || delta.P50Delta == nil || *delta.P50Delta != 10 {
		t.Fatalf("unexpected count/rate/latency delta: %#v", delta)
	}
	if !delta.CostDeltaUnknown || delta.CostDeltaUSD != nil {
		t.Fatalf("unknown cost must remain null and explicit: %#v", delta)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "candidate-1") || strings.Contains(string(encoded), "baseline-1") || !strings.Contains(string(encoded), "identifiers_not_authorization") {
		t.Fatalf("raw report identity escaped or provenance missing: %s", encoded)
	}
	if !strings.Contains(string(encoded), "ref_") {
		t.Fatalf("pseudonymous report reference missing: %s", encoded)
	}
}

func TestQueryCohortComparisonUsesCombinedReportLimit(t *testing.T) {
	reports := make([]DiagnosticReport, CohortMaxReports/2+1)
	for i := range reports {
		reports[i] = DiagnosticReport{ExecutionID: fmt.Sprintf("%d", i)}
	}
	if _, err := QueryCohort(CohortQuery{Reports: reports, BaselineReports: reports}); err == nil {
		t.Fatal("expected combined report limit")
	}
}

func TestDiagnosticReportDurationUsesUsageAndPreservesUnknownZero(t *testing.T) {
	var report DiagnosticReport
	if err := json.Unmarshal([]byte(`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"usage-duration","state":"completed","usage":{"duration_ms":12}}`), &report); err != nil {
		t.Fatal(err)
	}
	if !report.DurationKnown || report.DurationMS != 12 {
		t.Fatalf("usage duration was not preserved: %#v", report)
	}
	var nullDuration DiagnosticReport
	if err := json.Unmarshal([]byte(`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"null-duration","state":"completed","duration_ms":null,"usage":{"duration_ms":7}}`), &nullDuration); err != nil || !nullDuration.DurationKnown || nullDuration.DurationMS != 7 {
		t.Fatalf("null duration did not fall back to usage: %#v %v", nullDuration, err)
	}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{report}})
	if err != nil || result.Groups[0].LatencyMS.Count != 1 || result.Groups[0].LatencyMS.P50 != 12 {
		t.Fatalf("usage duration was not aggregated: %#v %v", result, err)
	}
	var unknown DiagnosticReport
	if err := json.Unmarshal([]byte(`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"unknown-duration","state":"completed"}`), &unknown); err != nil {
		t.Fatal(err)
	}
	unknownResult, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{unknown}})
	if err != nil || unknownResult.Groups[0].LatencyMS.Count != 0 || unknownResult.Groups[0].LatencyUnknownCount != 1 || !unknownResult.Coverage.Partial || unknownResult.Coverage.Reasons[0] != "missing_duration" {
		t.Fatalf("absent duration became known zero: %#v %v", unknownResult, err)
	}
	encoded, err := json.Marshal(unknownResult.Groups[0].LatencyMS)
	if err != nil || !strings.Contains(string(encoded), `"p50":null`) || !strings.Contains(string(encoded), `"p95":null`) {
		t.Fatalf("unknown quantiles were not null: %s %v", encoded, err)
	}
}

func TestQueryCohortTerminalStatusesAndInvalidValues(t *testing.T) {
	negative := int64(-5)
	cost := -1.0
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{
		{ExecutionID: "failed", Status: "failed", DurationMS: 10},
		{ExecutionID: "cancelled", Status: "cancelled", DurationMS: 20},
		{ExecutionID: "running", Status: "running", DurationMS: 30},
		{ExecutionID: "retry-success", Status: "completed", DurationMS: 40, Events: []DiagnosticEvent{{Status: "error"}, {ActionType: "llm_call", Status: "ok", CacheReadTokens: &negative, CostUSD: &cost}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.ErrorCount != 1 || group.CancelledCount != 1 || group.NonTerminalCount != 1 || group.LatencyMS.Count != 2 || group.CancelledLatencyMS.Count != 1 {
		t.Fatalf("terminal classification mismatch: %#v", group)
	}
	if group.CacheKnownEvents != 0 || group.CacheUnknownLLMEvents != 1 || group.CacheUnknownReadEvents != 1 || group.Cost.KnownCount != 0 || group.Cost.UnknownCount != 2 {
		t.Fatalf("invalid values were treated as known: %#v", group)
	}
	comparison, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "pending", Status: "running"}}, BaselineReports: []DiagnosticReport{{ExecutionID: "cancelled", Status: "cancelled"}}})
	if err != nil || comparison.Comparison == nil || comparison.Comparison.Deltas[0].ErrorRateDelta != nil {
		t.Fatalf("error rate was not unknown with no eligible outcomes: %#v %v", comparison, err)
	}
}

func TestQueryCohortWindowReportsMissingCreatedAt(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Window: CohortWindow{Start: "2026-01-01T00:00:00Z"}, Reports: []DiagnosticReport{{ExecutionID: "missing-created", Status: "completed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Coverage.Partial || result.Coverage.Complete || len(result.Coverage.Reasons) != 1 || result.Coverage.Reasons[0] != "missing_created_at" {
		t.Fatalf("window exclusion was not explained: %#v", result.Coverage)
	}
}

func TestQueryCohortRefusedTimeoutUnknownAndNormalizedStatus(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Status: "error", GroupBy: []string{"status"}, Reports: []DiagnosticReport{
		{ExecutionID: "failed", Status: "failed", DurationMS: 1},
		{ExecutionID: "timeout", Status: "timeout", DurationMS: 2},
		{ExecutionID: "refused", Status: "policy_denied", DurationMS: 3},
		{ExecutionID: "unknown", Status: "future_state", DurationMS: 4},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 2 || result.Groups[0].ErrorCount+result.Groups[1].ErrorCount != 2 || result.Groups[0].RefusedCount != 0 || result.Groups[1].RefusedCount != 0 {
		t.Fatalf("status filter was not normalized: %#v", result)
	}
	result, err = QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "timeout", Status: "timeout"}, {ExecutionID: "refused", Status: "denied"}, {ExecutionID: "unknown", Status: "future_state"}}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.TimeoutCount != 1 || group.RefusedCount != 1 || group.UnknownStatusCount != 1 || !result.Coverage.Partial || !containsReason(result.Coverage.Reasons, "unknown_status") {
		t.Fatalf("terminal distinctions missing: %#v", result)
	}
}

func TestQueryCohortAcceptsAndGroupsHumanWaitingStatuses(t *testing.T) {
	statuses := []string{"waiting_for_form", "waiting_for_approval", "human_waiting"}
	for _, status := range statuses {
		result, err := QueryCohort(CohortQuery{Status: status, GroupBy: []string{"status"}, Reports: []DiagnosticReport{{ExecutionID: status, Status: status, DurationMS: 1}}})
		if err != nil || len(result.Groups) != 1 || result.Groups[0].Dimensions["status"] != "human_waiting" || result.Groups[0].HumanWaitingCount != 1 {
			t.Fatalf("status %q was not normalized consistently: %#v %v", status, result, err)
		}
	}
}

func TestQueryCohortCostProvenanceAndMixedNature(t *testing.T) {
	exact, estimate, unclassified := 1.25, 0.75, 0.5
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{
		ExecutionID: "cost-provenance", Status: "completed", DurationMS: 2,
		Events: []DiagnosticEvent{
			{ActionType: "tool_call", CostUSD: &exact, CostStatus: "reported_exact"},
			{ActionType: "llm_call", CostUSD: &estimate, CostStatus: "producer_estimate"},
			{ActionType: "tool_call", CostUSD: &unclassified},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	cost := result.Groups[0].Cost
	if cost.Unit != "USD" || cost.Source != "inline_reported_costs_not_billing_authority" || cost.Status != "mixed" || cost.ExactCount != 1 || cost.EstimatedCount != 1 || cost.UnclassifiedCount != 1 || cost.ExactUSD != exact || cost.EstimatedUSD != estimate || cost.UnclassifiedUSD != unclassified {
		t.Fatalf("cost nature/provenance was not explicit: %#v", cost)
	}
}

func TestQueryCohortUnknownCostZeroIsNotFree(t *testing.T) {
	zero := 0.0
	report := DiagnosticReport{ExecutionID: "byok", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "llm_call", CostUSD: &zero, CostStatus: "byok_unknown"}}}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{report}, BaselineReports: []DiagnosticReport{{ExecutionID: "byok-base", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "llm_call", CostUSD: &zero, CostStatus: "unknown"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	cost := result.Groups[0].Cost
	if cost.KnownCount != 0 || cost.UnknownCount != 1 || cost.Status != "unknown" || !cost.Incomplete || result.Comparison.Deltas[0].CostDeltaUSD != nil || !result.Comparison.Deltas[0].CostDeltaUnknown {
		t.Fatalf("unknown zero cost was treated as free: %#v %#v", cost, result.Comparison.Deltas)
	}
}

func TestQueryCohortPartialComparisonDoesNotEmitNumericCostOrErrorDelta(t *testing.T) {
	known := 1.0
	candidate := DiagnosticReport{ExecutionID: "candidate-running", WorkflowID: "wf", Status: "running", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: &known, CostStatus: "reported_exact"}}}
	baseline := DiagnosticReport{ExecutionID: "baseline-complete", WorkflowID: "wf", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: &known, CostStatus: "reported_exact"}}}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{candidate}, BaselineReports: []DiagnosticReport{baseline}})
	if err != nil {
		t.Fatal(err)
	}
	delta := result.Comparison.Deltas[0]
	if delta.CostDeltaUSD != nil || !delta.CostDeltaUnknown || delta.ErrorRateDelta != nil {
		t.Fatalf("partial cohort emitted authoritative deltas: %#v", delta)
	}
}

func TestQueryCohortMixedKnownAndUnknownCostIsNotExact(t *testing.T) {
	exact, unknown := 1.0, 0.0
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "mixed-cost", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{
		{ActionType: "tool_call", CostUSD: &exact, CostStatus: "reported_exact"},
		{ActionType: "llm_call", CostUSD: &unknown, CostStatus: "byok_unknown"},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	cost := result.Groups[0].Cost
	if cost.Status != "mixed" || cost.KnownCount != 1 || cost.UnknownCount != 1 || !cost.Incomplete {
		t.Fatalf("mixed known/unknown cost was reported exact: %#v", cost)
	}
}

func TestQueryCohortRejectsMissingExecutionID(t *testing.T) {
	if _, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{Status: "completed"}}}); err == nil {
		t.Fatal("expected missing execution_id rejection")
	}
}

func TestQueryCohortBoundsExecutionAndTraceReferences(t *testing.T) {
	reports := make([]DiagnosticReport, 25)
	for i := range reports {
		reports[i] = DiagnosticReport{ExecutionID: fmt.Sprintf("execution-%02d", i), TraceID: fmt.Sprintf("trace-%02d", i), Status: "completed", DurationMS: float64(i + 1)}
	}
	result, err := QueryCohort(CohortQuery{Reports: reports})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if len(group.ExecutionIDs) != 20 || len(group.TraceIDs) != 20 || result.Provenance.References != "identifiers_not_authorization" {
		t.Fatalf("references were not bounded/provenanced: %#v %#v", group, result.Provenance)
	}
}

func containsReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func TestQueryCohortBaselinePartialKeepsCandidateSnapshot(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "candidate", Status: "completed", DurationMS: 1}}, BaselineReports: []DiagnosticReport{{ExecutionID: "baseline", Status: "running"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "partial" || !containsReason(result.Coverage.Reasons, "baseline_partial") || result.Comparison == nil || result.Comparison.Candidate.Status != "ok" {
		t.Fatalf("baseline partial propagation lost candidate snapshot: %#v", result)
	}
}

func TestQueryCohortMarksCrossCohortDuplicate(t *testing.T) {
	report := DiagnosticReport{AgentID: "a", ExecutionID: "same", Status: "completed"}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{report}, BaselineReports: []DiagnosticReport{report}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.CrossCohortDuplicates != 1 || !containsReason(result.Coverage.Reasons, "cross_cohort_duplicate") || result.Coverage.Complete {
		t.Fatalf("cross-cohort duplicate was not explicit: %#v", result.Coverage)
	}
}

func TestQueryCohortNoMetricEventsMakesCostUnknownDelta(t *testing.T) {
	candidate := DiagnosticReport{ExecutionID: "candidate-no-cost", Status: "completed", DurationMS: 5}
	baseline := DiagnosticReport{ExecutionID: "baseline-no-cost", Status: "completed", DurationMS: 5}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{candidate}, BaselineReports: []DiagnosticReport{baseline}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if !group.Cost.Incomplete || group.Cost.KnownCount != 0 || result.Comparison.Deltas[0].CostDeltaUSD != nil || !result.Comparison.Deltas[0].CostDeltaUnknown {
		t.Fatalf("missing cost was treated as zero: %#v", result)
	}
}

func TestQueryCohortTimeoutRetainsLatencyAndError(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "timeout", Status: "timeout", DurationMS: 30000}}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.TimeoutCount != 1 || group.ErrorCount != 1 || group.LatencyMS.Count != 1 || group.LatencyMS.P50 != 30000 {
		t.Fatalf("timeout metrics lost: %#v", group)
	}
}

func TestQueryCohortCoverageSignalsAndFilteredUnknown(t *testing.T) {
	result, err := QueryCohort(CohortQuery{WorkflowID: "A", Reports: []DiagnosticReport{{ExecutionID: "a", WorkflowID: "A", Status: "completed", DurationMS: 5, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: ptrFloat(1)}}}, {ExecutionID: "b", WorkflowID: "B", Status: "future_state"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.Partial {
		t.Fatalf("out-of-filter unknown polluted coverage: %#v", result.Coverage)
	}
	windowed, err := QueryCohort(CohortQuery{Window: CohortWindow{Start: "2026-01-02T00:00:00Z"}, Reports: []DiagnosticReport{{ExecutionID: "outside-window", Status: "future_state", CreatedAt: "2026-01-01T00:00:00Z"}}})
	if err != nil || containsReason(windowed.Coverage.Reasons, "unknown_status") {
		t.Fatalf("out-of-window unknown polluted coverage: %#v %v", windowed.Coverage, err)
	}
	result, err = QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "purged", Status: "completed", Coverage: DiagnosticCoverage{PayloadState: "purged"}, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: ptrFloat(1)}}}}})
	if err != nil || !result.Coverage.Partial || result.Coverage.Complete {
		t.Fatalf("purged coverage was complete: %#v %v", result.Coverage, err)
	}
}

func TestQueryCohortRejectsAggregateOverflow(t *testing.T) {
	if _, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "cost-overflow", Status: "completed", Events: []DiagnosticEvent{{CostUSD: ptrFloat(1e308), ActionType: "tool_call"}, {CostUSD: ptrFloat(1e308), ActionType: "tool_call"}}}}}); err == nil {
		t.Fatal("expected cost overflow")
	}
	max := int64(^uint64(0) >> 1)
	if _, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "token-overflow", Status: "completed", Events: []DiagnosticEvent{{ActionType: "llm_call", CacheReadTokens: &max}, {ActionType: "llm_call", CacheReadTokens: &max}}}}}); err == nil {
		t.Fatal("expected token overflow")
	}
}

func ptrFloat(value float64) *float64 { return &value }

func TestReviewMetadataReferencesTrimAndUnknownDurations(t *testing.T) {
	for _, value := range []string{"secretsecretsecret12", "12345678901234567890"} {
		if got := sanitizeReference(value, "execution"); got == value {
			t.Fatalf("suspect reference exposed: %s", got)
		}
	}
	reports := []DiagnosticReport{{ExecutionID: "a", WorkflowID: "alice@example.com", Status: "refused"}, {ExecutionID: "b", WorkflowID: "alice@example.com ", Status: "cancelled"}}
	result, err := QueryCohort(CohortQuery{Reports: reports, GroupBy: []string{"workflow_id"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 1 || result.Groups[0].LatencyUnknownCount != 2 || !result.Coverage.Partial {
		t.Fatalf("missing duration/trim mismatch: %#v", result)
	}
}

func TestQueryCohortExcludesCopiedChildCostAndCacheButRetainsCoverage(t *testing.T) {
	cost := 0.25
	cache := int64(80)
	report := DiagnosticReport{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "parent", Status: "completed", DurationMS: 10, Events: []DiagnosticEvent{
		{Source: "diagnostic_child_copy", ActionType: "llm_call", CostUSD: &cost, CostStatus: "estimated", CacheReadTokens: &cache},
		{Source: "llm", ActionType: "llm_call", CostUSD: &cost, CostStatus: "estimated", CacheReadTokens: &cache},
	}}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{report}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.Cost.KnownUSD != 0.25 || group.Cost.KnownCount != 1 || group.CacheReadTokensTotal != 80 || group.CacheKnownEvents != 1 {
		t.Fatalf("copied child metrics counted: %#v", group)
	}
	if group.EventCount != 2 || result.Coverage.EventsSeen != 2 {
		t.Fatalf("copied observations lost from coverage: %#v", result.Coverage)
	}
}

func TestQueryCohortDoesNotMixLegacyAndWallLatencyScopes(t *testing.T) {
	var legacy, wall DiagnosticReport
	if err := json.Unmarshal([]byte(`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"legacy","workflow_id":"wf","state":"completed","duration_ms":17,"events":[{"source":"llm","action_type":"llm_call","cost_status":"estimated","cost_usd":0}]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"wall","workflow_id":"wf","state":"completed","duration_ms":78,"timing":{"wall_elapsed_ms":99193},"events":[{"source":"llm","action_type":"llm_call","cost_status":"estimated","cost_usd":0}]}`), &wall); err != nil {
		t.Fatal(err)
	}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{legacy, wall}, GroupBy: []string{"workflow_id"}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.LatencyScope != "mixed_incomparable" || group.LatencyMS.Count != 0 || group.LatencyUnknownCount != 2 || !result.Coverage.Partial {
		t.Fatalf("mixed duration scopes were combined: %#v coverage=%#v", group, result.Coverage)
	}
	if group.Cost.KnownCount != 2 || group.Cost.KnownUSD != 0 {
		t.Fatalf("cost metrics changed with latency normalization: %#v", group.Cost)
	}
}

func TestMixedLatencyScopesRetainStatusCounters(t *testing.T) {
	reports := []DiagnosticReport{
		{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "a", WorkflowID: "wf", Status: "cancelled", DurationMS: 17, DurationScope: "legacy_runtime_duration_ms"},
		{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "b", WorkflowID: "wf", Status: "refused", DurationMS: 99000, DurationScope: "wall_elapsed_ms"},
		{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "c", WorkflowID: "wf", Status: "completed", DurationMS: 99000, DurationScope: "wall_elapsed_ms"},
	}
	result, err := QueryCohort(CohortQuery{Reports: reports, GroupBy: []string{"workflow_id"}})
	if err != nil {
		t.Fatal(err)
	}
	group := result.Groups[0]
	if group.CancelledCount != 1 || group.RefusedCount != 1 || group.ExecutionCount != 3 || group.LatencyUnknownCount != 3 || group.LatencyMS.Count != 0 || group.CancelledLatencyMS.Count != 0 || group.RefusedLatencyMS.Count != 0 {
		t.Fatalf("status counters or mixed latencies changed: %#v", group)
	}
}

func TestUnknownDurationDoesNotEraseKnownWallLatency(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		reports := []DiagnosticReport{{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "known", WorkflowID: "wf", Status: "completed", DurationMS: 1200, DurationScope: "wall_elapsed_ms"}, {SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "missing", WorkflowID: "wf", Status: "running"}}
		if reverse {
			reports[0], reports[1] = reports[1], reports[0]
		}
		result, err := QueryCohort(CohortQuery{Reports: reports, GroupBy: []string{"workflow_id"}})
		if err != nil {
			t.Fatal(err)
		}
		g := result.Groups[0]
		if g.LatencyScope != "wall_elapsed_ms" || g.LatencyMS.Count != 1 || g.LatencyMS.P50 != 1200 || g.LatencyUnknownCount != 1 {
			t.Fatalf("missing duration erased valid latency: %#v", g)
		}
	}
}

func TestSeparateCohortsWithDifferentScopesHaveNoLatencyDelta(t *testing.T) {
	before := CohortGroup{Dimensions: map[string]string{"workflow_id": "wf"}, ExecutionCount: 1, LatencyScope: "legacy_runtime_duration_ms", LatencyMS: CohortQuantiles{Count: 1, P50: 17, P95: 17}}
	after := CohortGroup{Dimensions: before.Dimensions, ExecutionCount: 1, LatencyScope: "wall_elapsed_ms", LatencyMS: CohortQuantiles{Count: 1, P50: 99375, P95: 99375}}
	delta := compareGroups([]CohortGroup{before}, []CohortGroup{after}, false)[0]
	if delta.P50Delta != nil || delta.P95Delta != nil || !delta.LatencyDeltaUnknown {
		t.Fatalf("incompatible axes compared: %#v", delta)
	}
}

func TestPendingLegacyDurationDoesNotChangeCompletedWallScope(t *testing.T) {
	reports := []DiagnosticReport{{SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "done", WorkflowID: "wf", Status: "completed", DurationMS: 1200, DurationScope: "wall_elapsed_ms"}, {SchemaVersion: DiagnosticSchemaVersion, ExecutionID: "pending", WorkflowID: "wf", Status: "running", DurationMS: 17, DurationScope: "legacy_runtime_duration_ms"}}
	result, err := QueryCohort(CohortQuery{Reports: reports, GroupBy: []string{"workflow_id"}})
	if err != nil {
		t.Fatal(err)
	}
	g := result.Groups[0]
	if g.LatencyScope != "wall_elapsed_ms" || g.LatencyMS.Count != 1 || g.LatencyMS.P50 != 1200 || g.LatencyUnknownCount != 1 {
		t.Fatalf("pending report changed terminal latency scope: %#v", g)
	}
}

func TestDiagnosticReportJSONRoundTripPreservesDurationScope(t *testing.T) {
	for _, raw := range []string{`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"wall","workflow_id":"wf","state":"completed","duration_ms":17,"timing":{"wall_elapsed_ms":99375}}`, `{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"unknown","state":"completed","duration_ms":17,"timing":{"wall_elapsed_ms":null}}`} {
		var first, second DiagnosticReport
		if err := json.Unmarshal([]byte(raw), &first); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(encoded, &second); err != nil {
			t.Fatal(err)
		}
		if first.DurationKnown != second.DurationKnown || first.DurationScope != second.DurationScope || first.DurationMS != second.DurationMS {
			t.Fatalf("duration changed on round trip: %#v -> %#v", first, second)
		}
		if first.DurationKnown && first.DurationSource != second.DurationSource {
			t.Fatalf("known duration source changed: %#v -> %#v", first, second)
		}
	}
	var wall, legacy DiagnosticReport
	if err := json.Unmarshal([]byte(`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"wall","workflow_id":"wf","state":"completed","duration_ms":99375,"duration_scope":"wall_elapsed_ms"}`), &wall); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"legacy","workflow_id":"wf","state":"completed","duration_ms":17}`), &legacy); err != nil {
		t.Fatal(err)
	}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{wall, legacy}, GroupBy: []string{"workflow_id"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Groups[0].LatencyScope != "mixed_incomparable" || result.Groups[0].LatencyMS.Count != 0 {
		t.Fatalf("round-tripped scopes combined: %#v", result.Groups[0])
	}
}

func TestProgrammaticUnknownDurationSurvivesJSONRoundTrip(t *testing.T) {
	for _, input := range []DiagnosticReport{{}, {DurationMS: 17, DurationScope: "unknown"}, {DurationScope: "wall_elapsed_ms"}, {DurationScope: "legacy_runtime_duration_ms"}, {DurationScope: "wall_elapsed_ms", DurationKnown: true}, {DurationMS: 0, DurationKnown: true}, {DurationMS: 17}} {
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded DiagnosticReport
		if err = json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if durationKnown(input) != durationKnown(decoded) || reportDurationScope(input) != reportDurationScope(decoded) {
			t.Fatalf("programmatic duration changed: %#v -> %s -> %#v", input, encoded, decoded)
		}
		if !durationKnown(input) && !strings.Contains(string(encoded), `"duration_ms":null`) {
			t.Fatalf("unknown duration serialized as zero: %s", encoded)
		}
	}
}

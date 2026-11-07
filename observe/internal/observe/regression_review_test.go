package observe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestQueryCohortContextCanceledBeforeLargeDuplicateFingerprinting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	large := strings.Repeat("x", 4*1024*1024)
	reports := make([]DiagnosticReport, 999)
	for i := range reports {
		reports[i] = DiagnosticReport{ExecutionID: "xid12345678901234567890", Status: "completed", Events: []DiagnosticEvent{{Name: large}}}
	}
	if _, err := QueryCohortContext(ctx, CohortQuery{Reports: reports}); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("already-canceled context was not honored before fingerprinting: %v", err)
	}
}

func TestCohortJSONRedactsSecretFiltersAndGroupData(t *testing.T) {
	secret := "sk-live-" + "AbCdEf0123456789 secret-token-123"
	report := DiagnosticReport{ExecutionID: "xid12345678901234567890", WorkflowID: secret, Model: secret, Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "tool_call", Name: secret}}}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{report}, WorkflowID: secret, Model: secret, GroupBy: []string{"workflow_id", "model"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sk-live-"+"AbCdEf0123456789") || strings.Contains(string(encoded), "secret-token-123") {
		t.Fatalf("secret data escaped cohort JSON: %s", encoded)
	}
}

func TestParseAndCohortNormalizeHumanWaitingAndRefused(t *testing.T) {
	human, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"human_waiting","events":[{"action_type":"tool_call","status":"waiting_for_approval"}]}`)
	if err != nil || len(human) != 1 || human[0].Status != "human_waiting" || human[0].ExecutionState != "human_waiting" {
		t.Fatalf("human waiting normalization mismatch: %#v %v", human, err)
	}
	refused, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"refused","events":[{"action_type":"tool_call","status":"policy_denied"}]}`)
	if err != nil || len(refused) != 1 || refused[0].Status != "refused" || refused[0].ExecutionState != "refused" {
		t.Fatalf("refused normalization mismatch: %#v %v", refused, err)
	}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{
		{ExecutionID: "xid12345678901234567890", Status: "human_waiting"},
		{ExecutionID: "xid12345678901234567891", Status: "waiting_for_approval"},
		{ExecutionID: "xid12345678901234567892", Status: "policy_denied"},
	}})
	if err != nil || len(result.Groups) != 1 || result.Groups[0].HumanWaitingCount != 2 || result.Groups[0].RefusedCount != 1 {
		t.Fatalf("cohort status normalization mismatch: %#v %v", result, err)
	}
}

func TestSafeFilterValueRedactsPIIAndFreeformIdentifiers(t *testing.T) {
	if got := safeFilterValue("alice@example.com"); got == "alice@example.com" || !strings.HasPrefix(got, "ref_") {
		t.Fatalf("PII filter was not pseudonymized: %q", got)
	}
	if got := safeFilterValue("workflow-orders-v2"); got == "workflow-orders-v2" || !strings.HasPrefix(got, "ref_") {
		t.Fatalf("freeform identifier was not pseudonymized: %q", got)
	}
}

func TestValidExecutionReferencePreservedAfterCredentialDetection(t *testing.T) {
	value := "dbgb88a4p5cs4da55a70"
	if len(value) != 20 {
		t.Fatalf("test reference length changed: %d", len(value))
	}
	if got := sanitizeReference(value, "execution"); got != value {
		t.Fatalf("valid execution reference was treated as credential: %q", got)
	}
}

func TestCohortPseudonymizesDistinctSensitiveDimensions(t *testing.T) {
	secretA := "secret-token-A"
	secretB := "secret-token-B"
	result, err := QueryCohort(CohortQuery{GroupBy: []string{"model"}, Reports: []DiagnosticReport{
		{ExecutionID: "xid12345678901234567890", Model: secretA, Status: "completed", DurationMS: 1},
		{ExecutionID: "xid12345678901234567891", Model: secretB, Status: "completed", DurationMS: 2},
	}})
	if err != nil {
		t.Fatalf("distinct secret dimensions should use distinct HMACs: %v", err)
	}
	if len(result.Groups) != 2 {
		t.Fatalf("expected two independent groups, got %#v", result.Groups)
	}
	a, b := result.Groups[0].Dimensions["model"], result.Groups[1].Dimensions["model"]
	if a == b || !strings.HasPrefix(a, "ref_") || !strings.HasPrefix(b, "ref_") {
		t.Fatalf("pseudonyms collapsed or exposed: %q %q", a, b)
	}
}

func TestCohortStatusFilterAndGroupTreatMissingTerminalEventAsUnknown(t *testing.T) {
	report := DiagnosticReport{ExecutionID: "xid12345678901234567890", Status: "completed", Coverage: DiagnosticCoverage{Reasons: []string{"terminal_event_not_recorded"}}}
	completed, err := QueryCohort(CohortQuery{Status: "completed", Reports: []DiagnosticReport{report}})
	if err != nil || completed.Coverage.ReportsMatched != 0 {
		t.Fatalf("unconfirmed completed report matched completed: %#v %v", completed, err)
	}
	unknown, err := QueryCohort(CohortQuery{Status: "unknown", GroupBy: []string{"status"}, Reports: []DiagnosticReport{report}})
	if err != nil || len(unknown.Groups) != 1 || unknown.Groups[0].Dimensions["status"] != "unknown" || unknown.Groups[0].UnknownStatusCount != 1 {
		t.Fatalf("unconfirmed status was not unknown: %#v %v", unknown, err)
	}
}

func TestStatusFilterReportsExcludedUnknownAndNonterminalCoverage(t *testing.T) {
	result, err := QueryCohort(CohortQuery{
		Status: "error", Model: "target",
		Reports: []DiagnosticReport{
			{ExecutionID: "claimed-completed", Model: "target", Status: "completed", Coverage: DiagnosticCoverage{Reasons: []string{"terminal_event_not_recorded"}}},
			{ExecutionID: "running", Model: "target", Status: "running"},
			{ExecutionID: "other-model", Model: "other", Status: "running"},
		},
	})
	if err != nil || len(result.Groups) != 0 || !result.Coverage.Partial || !containsReason(result.Coverage.Reasons, "unknown_status") || !containsReason(result.Coverage.Reasons, "nonterminal_status") {
		t.Fatalf("excluded uncertain reports were hidden by status filter: %#v %v", result, err)
	}
	windowed, err := QueryCohort(CohortQuery{
		Status: "error", Model: "target", Window: CohortWindow{Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z"},
		Reports: []DiagnosticReport{{ExecutionID: "outside", Model: "target", Status: "running", CreatedAt: "2026-02-01T00:00:00Z"}},
	})
	if err != nil || windowed.Coverage.Partial {
		t.Fatalf("out-of-window uncertainty contaminated coverage: %#v %v", windowed, err)
	}
}

func TestCohortModelHMACKeepsVertexAndEmailModelsDistinct(t *testing.T) {
	result, err := QueryCohort(CohortQuery{GroupBy: []string{"model"}, Reports: []DiagnosticReport{
		{ExecutionID: "xid12345678901234567890", Model: "gemini@vertex-a", DurationMS: 1},
		{ExecutionID: "xid12345678901234567891", Model: "gemini@vertex-b", DurationMS: 1},
		{ExecutionID: "xid12345678901234567892", Model: "alice@example.com", DurationMS: 1},
	}})
	if err != nil || len(result.Groups) != 3 {
		t.Fatalf("sensitive model groups collapsed: %#v %v", result, err)
	}
}

func TestTechnicalModelAndVersionValuesRemainReadableWithoutWeakeningPII(t *testing.T) {
	model := "claude-opus-4-1-20250805"
	version := "1.0.20261027"
	result, err := QueryCohort(CohortQuery{
		Model: model, GroupBy: []string{"model", "workflow_version"},
		Reports: []DiagnosticReport{{ExecutionID: "xid12345678901234567890", Model: model, WorkflowVersion: version, Status: "completed", DurationMS: 1}},
	})
	if err != nil || len(result.Groups) != 1 || result.Groups[0].Dimensions["model"] == model || !strings.HasPrefix(result.Groups[0].Dimensions["model"], "ref_") || result.Groups[0].Dimensions["workflow_version"] != version {
		t.Fatalf("technical model/version was not preserved: %#v %v", result, err)
	}
	phoneModel := "0612345678"
	phone, err := QueryCohort(CohortQuery{GroupBy: []string{"model"}, Reports: []DiagnosticReport{{ExecutionID: "xid12345678901234567891", Model: phoneModel, Status: "completed", DurationMS: 1}}})
	if err != nil || len(phone.Groups) != 1 || phone.Groups[0].Dimensions["model"] == phoneModel || !strings.HasPrefix(phone.Groups[0].Dimensions["model"], "ref_") {
		t.Fatalf("phone-like model escaped pseudonymization: %#v %v", phone, err)
	}
}

func TestCohortRefusedAndCancelledLatenciesStayOutOfMainQuantiles(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{
		{ExecutionID: "xid12345678901234567890", Status: "refused", DurationMS: 10},
		{ExecutionID: "xid12345678901234567891", Status: "cancelled", DurationMS: 20},
		{ExecutionID: "xid12345678901234567892", Status: "completed", DurationMS: 30},
	}})
	if err != nil || len(result.Groups) != 1 || result.Groups[0].LatencyMS.Count != 1 || result.Groups[0].RefusedLatencyMS.Count != 1 || result.Groups[0].CancelledLatencyMS.Count != 1 {
		t.Fatalf("status latency buckets mixed: %#v %v", result, err)
	}
}

func TestCohortDerivesUniqueEventModelAndMarksMultipleModels(t *testing.T) {
	one, err := QueryCohort(CohortQuery{GroupBy: []string{"model"}, Reports: []DiagnosticReport{{ExecutionID: "xid12345678901234567890", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{Model: "provider-a"}}}}})
	if err != nil || len(one.Groups) != 1 || one.Groups[0].Dimensions["model"] == "provider-a" || !strings.HasPrefix(one.Groups[0].Dimensions["model"], "ref_") {
		t.Fatalf("unique event model was not derived: %#v %v", one, err)
	}
	many, err := QueryCohort(CohortQuery{GroupBy: []string{"model"}, Reports: []DiagnosticReport{{ExecutionID: "xid12345678901234567891", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{Model: "provider-a"}, {Model: "provider-b"}}}}})
	if err != nil || len(many.Groups) != 1 || many.Groups[0].Dimensions["model"] != "multiple_models" {
		t.Fatalf("multiple event models were not explicit: %#v %v", many, err)
	}
}

func TestAllMetadataRailsRedactCredentialFormatsAndKeepActualExecutionIDs(t *testing.T) {
	for _, value := range []string{"SK-live-AbCdEf0123456789", "xoxb-1234-abcd-efgh", "AKIAIOSFODNN7EXAMPLE", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefg", "alice@example.com"} {
		if got := safeFilterValue(value); got == value || !strings.HasPrefix(got, "ref_") {
			t.Fatalf("filter leaked credential format: %q", got)
		}
		if got := sanitizeSpanText(value, "name"); got == value || !strings.HasPrefix(got, "ref_") {
			t.Fatalf("span leaked credential format: %q", got)
		}
		if sanitizeReference(value, "execution") != "" {
			t.Fatalf("reference leaked credential format")
		}
	}
	if got := sanitizeReference("dbfkrma4p5ckvuf5btng", "execution"); got != "dbfkrma4p5ckvuf5btng" {
		t.Fatalf("actual product xid not navigable: %s", got)
	}
	if got := sanitizeSpanText("token_limit_exceeded", "error_code"); got != "token_limit_exceeded" {
		t.Fatalf("structured cause lost: %s", got)
	}
}

func TestSanitizedTokenActionIdentifiersRemainDistinct(t *testing.T) {
	a := sanitizeSpanText("count_tokens", "action_id")
	b := sanitizeSpanText("refresh_token", "action_id")
	if a == b || a == "[redacted]" || b == "[redacted]" {
		t.Fatal("different workflow actions collapsed")
	}
}

func TestCohortSignalsReferenceTruncation(t *testing.T) {
	reports := make([]DiagnosticReport, 26)
	for i := range reports[:25] {
		reports[i] = DiagnosticReport{ExecutionID: fmt.Sprintf("baseline-exec-%d", i), WorkflowID: "baseline", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: ptrFloat(1)}}}
	}
	reports[25] = DiagnosticReport{ExecutionID: "candidate-exec", WorkflowID: "candidate", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: ptrFloat(1)}}}
	result, err := QueryCohort(CohortQuery{Reports: reports, GroupBy: []string{"workflow_id"}})
	if err != nil || !result.ResultTruncated || !containsReason(result.TruncationReasons, "references_truncated") || !result.Coverage.Complete || result.Status != "ok" {
		t.Fatalf("reference truncation was not explicit: %#v %v", result, err)
	}
	for _, group := range result.Groups {
		if group.Dimensions["workflow_id"] == "baseline" && group.Cost.Incomplete {
			t.Fatalf("reference truncation incorrectly made baseline metrics incomplete: %#v", group)
		}
		if group.Dimensions["workflow_id"] == "candidate" && group.Cost.Incomplete {
			t.Fatalf("candidate metrics became incomplete: %#v", group)
		}
	}
}

func TestCohortDoesNotHideCompleteGroupCostBehindGlobalPartial(t *testing.T) {
	known := 0.25
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{
		{ExecutionID: "known", WorkflowID: "A", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: &known}}},
		{ExecutionID: "unknown", WorkflowID: "B", Status: "completed", DurationMS: 1, Coverage: DiagnosticCoverage{Partial: true}, Events: []DiagnosticEvent{{ActionType: "tool_call"}}},
	}, GroupBy: []string{"workflow_id"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range result.Groups {
		if group.Dimensions["workflow_id"] == "A" && (group.Cost.Incomplete || group.Cost.KnownUSD != known) {
			t.Fatalf("complete group cost was masked: %#v", group)
		}
	}
}

func TestCohortMissingAgentSnapshotIsDeduplicatedButConflicted(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{
		{AgentID: "agent-a", ExecutionID: "same", Status: "failed", DurationMS: 1},
		{ExecutionID: "same", Status: "failed", DurationMS: 1},
	}})
	if err != nil || result.Groups[0].ExecutionCount != 1 || result.Coverage.DuplicateConflicts == 0 || !result.Coverage.Partial {
		t.Fatalf("ambiguous missing agent was double-counted or hidden: %#v %v", result, err)
	}
}

func TestCohortUpdatedAndCreatedTimestampsAreNotComparable(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{
		{AgentID: "agent-a", ExecutionID: "same-time", Status: "completed", CreatedAt: "2026-01-01T09:00:00Z", UpdatedAt: "2026-01-01T09:30:00Z"},
		{AgentID: "agent-a", ExecutionID: "same-time", Status: "failed", CreatedAt: "2026-01-01T09:00:00Z"},
	}})
	if err != nil || result.Coverage.DuplicateConflicts == 0 || !result.Coverage.Partial {
		t.Fatalf("timestamp source conflict was not reported: %#v %v", result, err)
	}
}

func TestTracePseudonymizationUsesProcessSecret(t *testing.T) {
	value := "alice@example.com"
	got := pseudonymize(value)
	sum := sha256.Sum256([]byte(value))
	if got == "ref_"+hex.EncodeToString(sum[:8]) || strings.Contains(got, value) {
		t.Fatalf("trace pseudonym is a reversible raw digest: %q", got)
	}
}

func TestEmbeddedPhoneValuesNeverEscapeMetadataRails(t *testing.T) {
	value := "client-0612345678"
	if safeFilterValue(value) == value || sanitizeSpanText(value, "name") == value || sanitizeReference(value, "execution") == value {
		t.Fatalf("embedded phone value escaped a metadata rail")
	}
}

func TestRestrictedKeyPrefixesNeverEscapeMetadataRails(t *testing.T) {
	for _, value := range []string{"rk_live_" + "9f8e7d6c5b4a", "rk_test_" + "9f8e7d6c5b4a"} {
		if safeFilterValue(value) == value || sanitizeSpanText(value, "name") == value || sanitizeReference(value, "execution") == value {
			t.Fatalf("restricted key prefix escaped metadata rail: %q", value)
		}
	}
}

func TestPayloadCoverageMakesOnlyItsGroupCostIncomplete(t *testing.T) {
	cost := 0.5
	result, err := QueryCohort(CohortQuery{GroupBy: []string{"workflow_id"}, Reports: []DiagnosticReport{
		{ExecutionID: "purged", WorkflowID: "A", Status: "completed", DurationMS: 1, Coverage: DiagnosticCoverage{PayloadState: "purged"}, Events: []DiagnosticEvent{{CostUSD: &cost}}},
		{ExecutionID: "known", WorkflowID: "B", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{CostUSD: &cost}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range result.Groups {
		if group.Dimensions["workflow_id"] == "A" && !group.Cost.Incomplete {
			t.Fatal("purged group cost was reported complete")
		}
		if group.Dimensions["workflow_id"] == "B" && group.Cost.Incomplete {
			t.Fatal("unrelated group cost was masked")
		}
	}
}

func TestTerminalEventMissingCannotCountAsCompleted(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{{ExecutionID: "unconfirmed", Status: "completed", DurationMS: 1, Coverage: DiagnosticCoverage{Reasons: []string{"terminal_event_not_recorded"}}}}})
	if err != nil || result.Groups[0].ExecutionCount != 1 || result.Groups[0].UnknownStatusCount != 1 || result.Groups[0].ErrorCount != 0 {
		t.Fatalf("unconfirmed terminal was counted as success: %#v %v", result, err)
	}
}

func TestTraceToolsProjectMissingTerminalEventAsUnknown(t *testing.T) {
	payload := `{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","duration_ms":42,"coverage":{"reasons":["terminal_event_not_recorded"]},"events":[{"name":"llm_call","status":"ok","duration_ms":42}]}`
	spans, err := Parse(payload)
	if err != nil || len(spans) != 1 || spans[0].ExecutionState != "unknown" {
		t.Fatalf("parse did not preserve unconfirmed state: %#v %v", spans, err)
	}
	summary := Summarize(spans)
	if summary.ExecutionState != "unknown" || summary.DurationMS != 42 || summary.DurationComplete {
		t.Fatalf("summary attested an unconfirmed terminal: %#v", summary)
	}
	explained := Explain(spans)
	if explained["diagnosis"] == "execution completed" {
		t.Fatalf("explain invented completed outcome: %#v", explained)
	}
	comparison := Compare(spans, spans)
	if comparison["error_delta"] != 0 || comparison["duration_delta_ms"] != float64(0) {
		t.Fatalf("compare changed unconfirmed evidence: %#v", comparison)
	}
}

func TestTraceKnownRootDurationIsIncompleteForNonterminalState(t *testing.T) {
	spans, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"running","duration_ms":42,"events":[{"name":"llm_call","status":"ok","duration_ms":42}]}`)
	if err != nil {
		t.Fatal(err)
	}
	summary := Summarize(spans)
	if summary.ExecutionState != "running" || summary.DurationMS != 42 || summary.DurationComplete {
		t.Fatalf("nonterminal root duration was attested complete: %#v", summary)
	}
}

func TestCoverageReasonsAloneMakeOnlyTheirGroupCostIncomplete(t *testing.T) {
	reasons := []string{"payload_purged", "payload_expired", "persisted_logs_truncated", "archive_unavailable", "lifecycle_not_recorded", "no_recorded_events", "interrupted_segment_calls_unknown"}
	for _, reason := range reasons {
		known := 0.25
		result, err := QueryCohort(CohortQuery{GroupBy: []string{"workflow_id"}, Reports: []DiagnosticReport{
			{ExecutionID: "incomplete-" + reason, WorkflowID: "incomplete", Status: "completed", DurationMS: 1, Coverage: DiagnosticCoverage{Reasons: []string{reason}}, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: &known}}},
			{ExecutionID: "known-" + reason, WorkflowID: "known", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{ActionType: "tool_call", CostUSD: &known}}},
		}})
		if err != nil || result.Status != "partial" || !containsReason(result.Coverage.Reasons, reason) {
			t.Fatalf("coverage reason %s was not normalized: %#v %v", reason, result, err)
		}
		for _, group := range result.Groups {
			if group.Dimensions["workflow_id"] == "incomplete" && !group.Cost.Incomplete {
				t.Fatalf("group %s cost was reported complete: %#v", reason, group)
			}
			if group.Dimensions["workflow_id"] == "known" && group.Cost.Incomplete {
				t.Fatalf("unrelated known group was contaminated by %s: %#v", reason, group)
			}
		}
	}
}

func TestModelFilterMatchesAnEventInMultimodelReport(t *testing.T) {
	result, err := QueryCohort(CohortQuery{Model: "provider-b", Reports: []DiagnosticReport{{ExecutionID: "multi", Model: "provider-a", Status: "completed", DurationMS: 1, Events: []DiagnosticEvent{{Model: "provider-b"}, {Model: "provider-c"}}}}})
	if err != nil || result.Coverage.ReportsMatched != 1 {
		t.Fatalf("model filter missed event model: %#v %v", result, err)
	}
}

func TestCandidateSnapshotKeepsCrossCohortDuplicateMarking(t *testing.T) {
	report := DiagnosticReport{AgentID: "agent", ExecutionID: "same", Status: "completed", DurationMS: 1}
	result, err := QueryCohort(CohortQuery{Reports: []DiagnosticReport{report}, BaselineReports: []DiagnosticReport{report}})
	if err != nil || result.Comparison == nil || result.Status != "partial" || result.Comparison.Candidate.Status != "partial" || result.Comparison.Baseline.Status != "partial" || !result.Comparison.Candidate.Coverage.Partial || !result.Comparison.Baseline.Coverage.Partial || result.Comparison.Candidate.Coverage.CrossCohortDuplicates != 1 || result.Comparison.Baseline.Coverage.CrossCohortDuplicates != 1 {
		t.Fatalf("candidate snapshot lost duplicate marking: %#v %v", result, err)
	}
}

func TestCrossCohortDuplicateMatchesMissingAgentIdentity(t *testing.T) {
	result, err := QueryCohort(CohortQuery{
		Reports:         []DiagnosticReport{{AgentID: "agent", ExecutionID: "same", Status: "completed", DurationMS: 1}},
		BaselineReports: []DiagnosticReport{{ExecutionID: "same", Status: "completed", DurationMS: 1}},
	})
	if err != nil || result.Comparison == nil || result.Comparison.Candidate.Coverage.CrossCohortDuplicates != 1 || result.Comparison.Baseline.Coverage.CrossCohortDuplicates != 1 {
		t.Fatalf("missing-agent cross-cohort duplicate was not visible on both snapshots: %#v %v", result, err)
	}
}

func TestSensitiveCredentialPatternsStayHMACAcrossAllRails(t *testing.T) {
	for _, value := range []string{"github_pat_1234567890abcdef", "AIzaSyA1234567890abcdef", "ya29.a0ARrdaM1234567890"} {
		for _, field := range []string{"name", "action_id", "span_id"} {
			got := sanitizeSpanText(value, field)
			if got == value || !strings.HasPrefix(got, "ref_") {
				t.Fatalf("credential escaped %s rail: %q -> %q", field, value, got)
			}
		}
		if got := safeFilterValue(value); got == value || !strings.HasPrefix(got, "ref_") {
			t.Fatalf("credential escaped filter rail: %q -> %q", value, got)
		}
	}
	if got := sanitizeSpanText("token_limit_exceeded", "error_code"); got != "token_limit_exceeded" {
		t.Fatalf("structured error code was not preserved: %q", got)
	}
}

func TestArbitraryMetadataStaysHMACAcrossAllRails(t *testing.T) {
	for _, value := range []string{"06/12/34/56/78", "06_12_34_56_78", "FR76_3000_6000_0112_3456_7890_189", "claude-0612345678-x-20250101", "jean.dupont", "+33 6 12 34 56 78", "192.0.2.42", "FR7612345987650123456789014", "model-home-address", "name-home-address"} {
		for _, got := range []string{safeFilterValue(value), safeFilterValueForDimension(value, "workflow_id"), safeFilterValueForDimension(value, "model"), sanitizeSpanText(value, "name"), sanitizeSpanText(value, "action_id"), sanitizeSpanText(value, "error_code")} {
			if got == value || !strings.HasPrefix(got, "ref_") {
				t.Fatalf("arbitrary metadata escaped HMAC rail: %q -> %q", value, got)
			}
		}
	}
}

func TestCanonicalSpanReferenceRemainsNavigable(t *testing.T) {
	spanID := "4bf92f3577b34da1"
	if got := sanitizeSpanText(spanID, "span_id"); got != spanID {
		t.Fatalf("canonical span ID was not preserved: %q", got)
	}
}

func TestTechnicalVersionExceptionDoesNotAdmitPhoneShapedNumericComponents(t *testing.T) {
	for _, value := range []string{"1.2025550123", "01.202.555.123", "+33 6 12 34 56 78"} {
		if got := safeFilterValueForDimension(value, "workflow_version"); got == value {
			t.Fatalf("phone shaped version exposed: %q", got)
		}
	}
	for _, value := range []string{"1.0.20261027", "2026-10-27-1530", "1.2.3.4"} {
		if got := safeFilterValueForDimension(value, "workflow_version"); got != value {
			t.Fatalf("technical version hidden: %q", got)
		}
	}
}

func TestEmbeddedCredentialAndPhoneCannotBypassAnyMetadataRail(t *testing.T) {
	for _, sensitive := range []string{"0612345678", "sk_live_" + "AbCdEf0123456789", "ghp_abcdef123456", "glpat-abcdef123456", "rk_live_" + "AbCdEf0123456789", "xoxb-abcdef123456"} {
		for _, value := range []string{sensitive, "client_" + sensitive, "client" + sensitive, sensitive + "x"} {
			for _, got := range []string{safeFilterValue(value), safeFilterValueForDimension(value, "model"), safeFilterValueForDimension(value, "workflow_id"), sanitizeSpanText(value, "name"), sanitizeReference(value, "execution")} {
				if got == value {
					t.Fatalf("sensitive metadata escaped: %q", value)
				}
			}
		}
	}
}

func TestCanonicalRuntimeReferencesSurviveSubstringFalsePositives(t *testing.T) {
	execution := "d1pkabcdefghij0123k0"
	trace := "4bf92f3577b34da1234567890e0e4736"
	if sanitizeReference(execution, "execution") != execution || sanitizeReference(trace, "trace") != trace {
		t.Fatal("canonical runtime correlation lost")
	}
	for _, value := range []string{"task-router", "pkg_resolver"} {
		if got := safeFilterValue(value); got == value || !strings.HasPrefix(got, "ref_") {
			t.Fatalf("freeform identifier was not pseudonymized: %s", got)
		}
	}
	r, err := QueryCohort(CohortQuery{Model: "m1", Reports: []DiagnosticReport{{ExecutionID: execution, Status: "completed", DurationMS: 1, Model: "m1 "}}})
	if err != nil || r.Coverage.ReportsMatched != 1 {
		t.Fatalf("model trim mismatch: %#v %v", r, err)
	}
}

package observe

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSummarizeAndExplain(t *testing.T) {
	spans, err := Parse(`{"spans":[{"name":"run","span_id":"a","start_unix_nano":"1000000","end_unix_nano":"11000000","status":"ok"},{"name":"tool","span_id":"b","parent_span_id":"a","duration_ms":4,"status":"error","retry_count":1}]}`)
	if err != nil {
		t.Fatal(err)
	}
	summary := Summarize(spans)
	if summary.SpanCount != 2 || summary.ErrorCount != 1 || summary.RetryCount != 1 || summary.DurationMS != 10 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(summary.CriticalPath) != 2 || summary.CriticalPath[1] == "tool" || !strings.HasPrefix(summary.CriticalPath[1], "ref_") {
		t.Fatalf("unexpected critical path: %#v", summary.CriticalPath)
	}
}

func TestParseOTLPShape(t *testing.T) {
	content := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"http","spanId":"01","startTimeUnixNano":"1000000","endTimeUnixNano":"3000000","status":{"code":"STATUS_CODE_ERROR"}}]}]}]}`
	spans, err := Parse(content)
	if err != nil || len(spans) != 1 || spans[0].Status != "error" || spans[0].DurationMS != 2 {
		t.Fatalf("unexpected parse: %#v, %v", spans, err)
	}
}

func TestParseExecutionDiagnosticSummaryShape(t *testing.T) {
	content := `{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"exec-1","workflow_id":"wf-1","state":"failed","error_code":"tool_failed","coverage":{"partial":true,"values_redacted":true,"source":"local","trace_kind":"persisted_execution_journal","prompt":"must not escape","user_email":"must not escape","nested":{"prompt":"must not escape"},"reasons":["persisted_logs_truncated","prompt"]},"usage":{"duration_ms":12},"events":[{"action_id":"step-1","action_type":"diagnostic_step","status":"ok","duration_ms":1},{"action_id":"step-2","action_type":"tool_call","source":"runtime","status":"error","error_code":"tool_failed","start_ms":2,"end_ms":8,"duration_ms":6}]}`
	spans, err := Parse(content)
	if err != nil || len(spans) != 1 {
		t.Fatalf("diagnostic parse = %#v, %v", spans, err)
	}
	span := spans[0]
	if span.ActionID == "step-2" || !strings.HasPrefix(span.ActionID, "ref_") || span.Runtime != "runtime" || span.ErrorCode != "tool_failed" || span.DurationMS != 6 || span.Status != "error" || !span.Coverage["partial"].(bool) {
		t.Fatalf("unexpected diagnostic span: %#v", span)
	}
	if _, ok := span.Coverage["prompt"]; ok {
		t.Fatalf("arbitrary coverage field escaped: %#v", span.Coverage)
	}
	if _, ok := span.Coverage["user_email"]; ok {
		t.Fatalf("personal coverage field escaped: %#v", span.Coverage)
	}
	if got := span.Coverage["reasons"].([]string); len(got) != 1 || got[0] != "persisted_logs_truncated" {
		t.Fatalf("unexpected allowlisted coverage reasons: %#v", got)
	}
}

func TestParseDiagnosticKeepsInputOrderForEqualStartTimes(t *testing.T) {
	content := `{"schema_version":"agentmaurice.execution_diagnostic/v1","events":[{"action_id":"first","action_type":"tool_call","start_ms":3,"end_ms":4},{"action_id":"second","action_type":"tool_call","start_ms":3,"end_ms":5}]}`
	spans, err := Parse(content)
	if err != nil || len(spans) != 2 || spans[0].ActionID != pseudonymize("first") || spans[1].ActionID != pseudonymize("second") {
		t.Fatalf("equal-start input order was not stable: %#v, %v", spans, err)
	}
}

func TestParseExecutionDiagnosticExcludesLifecycleAndDiagnosticStepSources(t *testing.T) {
	content := `{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"exec-source","workflow_id":"wf-1","state":"completed","usage":{"duration_ms":12},"events":[{"action_type":"execution","source":"diagnostic_lifecycle","duration_ms":1},{"action_type":"code_execution","source":"diagnostic_step","status":"ok","duration_ms":2},{"action_type":"code_execution","source":"artifact","status":"ok","duration_ms":9},{"action_type":"tool_call","name":"lifecycle","source":"artifact","status":"ok","duration_ms":4},{"action_type":"execution","source":"diagnostic_lifecycle","status":"completed","duration_ms":0} ]}`
	spans, err := Parse(content)
	if err != nil || len(spans) != 2 {
		t.Fatalf("diagnostic source filtering = %#v, %v", spans, err)
	}
	if spans[0].Runtime != "artifact" || spans[0].DurationMS != 9 || spans[1].Name == "lifecycle" || !strings.HasPrefix(spans[1].Name, "ref_") {
		t.Fatalf("unexpected surviving runtime events: %#v", spans)
	}
}

func TestParseDiagnosticPreservesExplicitZeroAndLifecycleState(t *testing.T) {
	spans, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"failed","error_code":"cancelled","events":[{"action_type":"tool_call","duration_ms":0,"start_ms":2,"end_ms":8}]}`)
	if err != nil || len(spans) != 1 || spans[0].DurationMS != 0 {
		t.Fatalf("explicit zero was changed: %#v %v", spans, err)
	}
	spans, err = Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"failed","error_code":"guest_panic","events":[{"action_type":"lifecycle","status":"failed"}]}`)
	if err != nil || len(spans) != 1 || !spans[0].Synthetic || spans[0].ExecutionState != "failed" || spans[0].ErrorCode != "guest_panic" {
		t.Fatalf("lifecycle-only state was lost: %#v %v", spans, err)
	}
}

func TestDiagnosticRootStateSurvivesSuccessfulStep(t *testing.T) {
	for _, tc := range []struct {
		state      string
		wantErrors int
	}{{"running", 0}, {"failed", 1}, {"timeout", 1}} {
		spans, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"root-state","state":"` + tc.state + `","events":[{"action_type":"tool_call","status":"ok","duration_ms":5}]}`)
		if err != nil {
			t.Fatal(err)
		}
		summary := Summarize(spans)
		if summary.ExecutionState != tc.state || summary.ErrorCount != tc.wantErrors {
			t.Fatalf("root state lost for %s: %#v", tc.state, summary)
		}
		explained := Explain(spans)
		if tc.state == "failed" && len(explained["errors"].([]string)) == 0 {
			t.Fatalf("failed root had no explain error: %#v", explained)
		}
	}
}

func TestDiagnosticEventsNormalizeFailedAndTimeoutStatuses(t *testing.T) {
	spans, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","events":[{"action_type":"tool_call","status":"failed"},{"action_type":"tool_call","status":"timed_out"}]}`)
	if err != nil || len(spans) != 2 || spans[0].Status != "error" || spans[1].Status != "error" {
		t.Fatalf("diagnostic statuses were not normalized: %#v %v", spans, err)
	}
	if got := Summarize(spans).ErrorCount; got != 2 {
		t.Fatalf("normalized diagnostic errors=%d, want 2", got)
	}
}

func TestDiagnosticDurationAbsentNullAndZeroAreDistinct(t *testing.T) {
	inputs := []string{
		`{"schema_version":"agentmaurice.execution_diagnostic/v1","events":[{"action_type":"tool_call","status":"ok"}]}`,
		`{"schema_version":"agentmaurice.execution_diagnostic/v1","events":[{"action_type":"tool_call","status":"ok","duration_ms":null,"start_ms":2,"end_ms":8}]}`,
		`{"schema_version":"agentmaurice.execution_diagnostic/v1","events":[{"action_type":"tool_call","status":"ok","duration_ms":0,"start_ms":2,"end_ms":8}]}`,
	}
	wantKnown := []bool{false, false, true}
	wantDuration := []float64{0, 6, 0}
	for i, input := range inputs {
		spans, err := Parse(input)
		if err != nil || len(spans) != 1 || spans[0].DurationKnown != wantKnown[i] || spans[0].DurationMS != wantDuration[i] {
			t.Fatalf("duration case %d: %#v %v", i, spans, err)
		}
	}
}

func TestLegacyDurationIsKnownAndLifecycleUsageDurationIsCompared(t *testing.T) {
	legacy, err := Parse(`{"spans":[{"name":"legacy","span_id":"a","duration_ms":2,"status":"ok"}]}`)
	if err != nil || len(legacy) != 1 || !legacy[0].DurationKnown {
		t.Fatalf("legacy duration was not marked known: %#v %v", legacy, err)
	}
	left, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","usage":{"duration_ms":10},"events":[{"action_type":"lifecycle"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","usage":{"duration_ms":30},"events":[{"action_type":"lifecycle"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := Compare(left, right)["duration_delta_ms"]; got != float64(20) {
		t.Fatalf("usage duration delta=%v", got)
	}
}

func TestDiagnosticRootDurationWinsOverStepDurationsInCompare(t *testing.T) {
	left, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","usage":{"duration_ms":10},"events":[{"action_type":"tool_call","duration_ms":5}]}`)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","usage":{"duration_ms":500},"events":[{"action_type":"tool_call","duration_ms":5}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := Compare(left, right)["duration_delta_ms"]; got != float64(490) {
		t.Fatalf("root duration was not authoritative: %v", got)
	}
}

func TestDiagnosticDurationUsesKnownTimestampSpanWhenAnotherStepIsUntimed(t *testing.T) {
	spans, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","events":[{"action_type":"tool_call","start_ms":100,"end_ms":110},{"action_type":"tool_call","start_ms":120,"end_ms":150},{"action_type":"tool_call","duration_ms":1}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := Summarize(spans).DurationMS; got != 50 {
		t.Fatalf("known timestamp span was not used: %v", got)
	}
}

func TestDiagnosticParseRedactsAllStringFieldsAndMarksLocalSource(t *testing.T) {
	canary := "alice@example.com secret-token sk-live-abcdef"
	content := `{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"` + canary + `","error_code":"` + canary + `","coverage":{"partial":true},"events":[{"name":"` + canary + `","action_id":"` + canary + `","source":"` + canary + `","provider":"` + canary + `","error_code":"` + canary + `","trace_id":"` + canary + `","status":"ok","duration_ms":1}]}`
	spans, err := Parse(content)
	if err != nil || len(spans) != 1 {
		t.Fatalf("redaction parse failed: %#v %v", spans, err)
	}
	encoded, err := json.Marshal(spans[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), canary) || strings.Contains(string(encoded), "alice@example.com") || spans[0].Source != "local_execution_journal" {
		t.Fatalf("diagnostic raw fields escaped or source missing: %s %#v", encoded, spans[0])
	}
}

func TestLegacyParseRedactsSpanStrings(t *testing.T) {
	canary := "bob@example.com secret-token"
	spans, err := Parse(`{"spans":[{"name":"` + canary + `","span_id":"` + canary + `","parent_span_id":"` + canary + `","trace_id":"` + canary + `","status":"` + canary + `","duration_ms":"not-a-number"}]}`)
	if err != nil || len(spans) != 1 {
		t.Fatalf("legacy redaction parse failed: %#v %v", spans, err)
	}
	encoded, err := json.Marshal(spans[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), canary) || strings.Contains(string(encoded), "bob@example.com") || spans[0].DurationKnown {
		t.Fatalf("legacy raw field or invalid numeric escaped: %s %#v", encoded, spans[0])
	}
}

func TestParseMixedLegacyAndExecutionDiagnosticShapes(t *testing.T) {
	legacy, err := Parse(`{"spans":[{"name":"legacy","span_id":"a","duration_ms":2,"status":"ok"}]}`)
	if err != nil || len(legacy) != 1 {
		t.Fatalf("legacy parse = %#v, %v", legacy, err)
	}
	diagnostic, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","events":[{"action_id":"step-1","action_type":"tool_call","duration_ms":3}]}`)
	if err != nil || len(diagnostic) != 1 || diagnostic[0].ActionID == "step-1" || !strings.HasPrefix(diagnostic[0].ActionID, "ref_") {
		t.Fatalf("diagnostic parse = %#v, %v", diagnostic, err)
	}
}

func TestPseudonymousErrorRetainsStableSourceLocator(t *testing.T) {
	input := `{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"dbgb88a4p5cs4da55a70","events":[{"source":"diagnostic_lifecycle"},{"source":"artifact","action_id":"jean.dupont","name":"private step","status":"error","error_code":"wasm_execution_failed"}]}`
	spans, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	errors := Explain(spans)["error_spans"].([]Span)
	if len(errors) != 1 || errors[0].SourceIndex == nil || *errors[0].SourceIndex != 1 || errors[0].ExecutionID != "dbgb88a4p5cs4da55a70" || len(errors[0].InputSHA256) != 64 {
		t.Fatalf("error source not recoverable: %#v", errors)
	}
	if errors[0].Name == "private step" || errors[0].ActionID == "jean.dupont" {
		t.Fatal("source locator exposed free-form data")
	}
	again, _ := Parse(input)
	if errors[0].InputSHA256 != again[0].InputSHA256 || *errors[0].SourceIndex != *again[0].SourceIndex {
		t.Fatal("source locator is unstable")
	}
}

func TestParseDiagnosticExcludesCopiedChildSpans(t *testing.T) {
	content := `{"schema_version":"agentmaurice.execution_diagnostic/v1","execution_id":"parent","state":"completed","duration_ms":10,"events":[{"source":"diagnostic_child_copy","action_type":"code_execution","action_id":"child/copied","duration_ms":90000,"start_ms":1,"end_ms":90001},{"source":"code","action_type":"code_execution","action_id":"own","duration_ms":10,"start_ms":1,"end_ms":11}]}`
	spans, err := Parse(content)
	if err != nil || len(spans) != 1 || spans[0].ActionID != pseudonymize("own") || spans[0].DurationMS != 10 {
		t.Fatalf("copied child span counted: %#v %v", spans, err)
	}
}

func TestDiagnosticTimingWallElapsedOverridesLegacyRootDuration(t *testing.T) {
	content := `{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","duration_ms":17,"timing":{"wall_elapsed_ms":99375},"events":[{"action_type":"code_execution","duration_ms":17}]}`
	spans, err := Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	summary := Summarize(spans)
	if summary.DurationMS != 99375 || summary.DurationSource != "timing.wall_elapsed_ms" || summary.DurationScope != "wall_elapsed_ms" || !summary.DurationComplete {
		t.Fatalf("wall duration not authoritative: %#v", summary)
	}
	if spans[0].DurationSource != "recorded_event_metadata" || spans[0].DurationScope != "recorded_call_elapsed_not_compute" {
		t.Fatalf("span duration provenance missing: %#v", spans[0])
	}
}

func TestDiagnosticTimingWallMissingDoesNotFallbackToSegmentDuration(t *testing.T) {
	for _, timing := range []string{`{"wall_elapsed_ms":null}`, `{"wall_elapsed_ms":"invalid"}`} {
		content := `{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","duration_ms":17,"timing":` + timing + `,"events":[{"action_type":"code_execution","duration_ms":17}]}`
		spans, err := Parse(content)
		if err != nil {
			t.Fatal(err)
		}
		summary := Summarize(spans)
		if summary.DurationMS != 0 || summary.DurationSource != "unknown" || summary.DurationComplete {
			t.Fatalf("invalid wall duration fell back: %#v", summary)
		}
	}
}

func TestDiagnosticWithoutTimingKeepsLegacyDurationScope(t *testing.T) {
	spans, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","duration_ms":17,"events":[{"action_type":"code_execution","duration_ms":3}]}`)
	if err != nil {
		t.Fatal(err)
	}
	summary := Summarize(spans)
	if summary.DurationMS != 17 || summary.DurationSource != "legacy_runtime_duration_ms" || summary.DurationScope != "legacy_runtime_duration_ms" {
		t.Fatalf("legacy duration scope missing: %#v", summary)
	}
}

func TestRunComparisonDoesNotCompareWallWithLegacyRuntime(t *testing.T) {
	legacy, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","duration_ms":17,"events":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	wall, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","duration_ms":17,"timing":{"wall_elapsed_ms":99375},"events":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	result := Compare(legacy, wall)
	if result["duration_delta_ms"] != nil || result["duration_scope_compatible"] != false || result["duration_delta_complete"] != false {
		t.Fatalf("incompatible durations compared: %#v", result)
	}
}

func TestCompareKnownAxisWithUnknownValueHasNoDelta(t *testing.T) {
	unknown, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","duration_ms":null,"duration_scope":"wall_elapsed_ms","events":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	known, err := Parse(`{"schema_version":"agentmaurice.execution_diagnostic/v1","state":"completed","timing":{"wall_elapsed_ms":99375},"events":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2][]Span{{unknown, known}, {known, unknown}} {
		result := Compare(pair[0], pair[1])
		if result["duration_delta_ms"] != nil || result["duration_delta_complete"] != false {
			t.Fatalf("unknown value compared as zero: %#v", result)
		}
	}
}

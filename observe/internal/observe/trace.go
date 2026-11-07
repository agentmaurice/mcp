package observe

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Span struct {
	InputSHA256              string                 `json:"input_sha256,omitempty"`
	SourceIndex              *int                   `json:"source_index,omitempty"`
	ExecutionID              string                 `json:"execution_id,omitempty"`
	Name                     string                 `json:"name"`
	ActionID                 string                 `json:"action_id,omitempty"`
	Runtime                  string                 `json:"runtime,omitempty"`
	Source                   string                 `json:"source,omitempty"`
	ErrorCode                string                 `json:"error_code,omitempty"`
	Coverage                 map[string]interface{} `json:"coverage,omitempty"`
	SpanID                   string                 `json:"span_id,omitempty"`
	TraceID                  string                 `json:"trace_id,omitempty"`
	ParentSpanID             string                 `json:"parent_span_id,omitempty"`
	StartNS                  int64                  `json:"start_unix_nano,omitempty"`
	EndNS                    int64                  `json:"end_unix_nano,omitempty"`
	DurationMS               float64                `json:"duration_ms"`
	Status                   string                 `json:"status"`
	ExecutionState           string                 `json:"execution_state,omitempty"`
	Synthetic                bool                   `json:"synthetic,omitempty"`
	DurationKnown            bool                   `json:"duration_known"`
	DurationSource           string                 `json:"duration_source,omitempty"`
	DurationScope            string                 `json:"duration_scope,omitempty"`
	ExecutionDurationMS      float64                `json:"-"`
	ExecutionDurationKnown   bool                   `json:"-"`
	ExecutionDurationPresent bool                   `json:"-"`
	ExecutionDurationSource  string                 `json:"-"`
	ExecutionDurationScope   string                 `json:"-"`
	RetryCount               int                    `json:"retry_count,omitempty"`
}

type Summary struct {
	SpanCount        int      `json:"span_count"`
	ErrorCount       int      `json:"error_count"`
	RetryCount       int      `json:"retry_count"`
	DurationMS       float64  `json:"duration_ms"`
	DurationSource   string   `json:"duration_source,omitempty"`
	DurationScope    string   `json:"duration_scope,omitempty"`
	DurationComplete bool     `json:"duration_complete"`
	SlowestSpan      string   `json:"slowest_span,omitempty"`
	CriticalPath     []string `json:"critical_path"`
	CriticalPathMS   float64  `json:"critical_path_ms"`
	ExecutionState   string   `json:"execution_state,omitempty"`
}

func Parse(content string) ([]Span, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var root interface{}
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("invalid JSON trace: %w", err)
	}
	if object, ok := root.(map[string]interface{}); ok && object["schema_version"] == DiagnosticSchemaVersion {
		spans, err := parseDiagnosticEvents(object)
		attachInputReference(spans, content)
		return spans, err
	}
	spans := make([]Span, 0)
	walk(root, &spans)
	if len(spans) == 0 {
		return nil, fmt.Errorf("no spans found")
	}
	for i := range spans {
		index := i
		spans[i].SourceIndex = &index
	}
	attachInputReference(spans, content)
	sortSpans(spans)
	return spans, nil
}

func attachInputReference(spans []Span, content string) {
	digest := sha256.Sum256([]byte(content))
	for i := range spans {
		spans[i].InputSHA256 = hex.EncodeToString(digest[:])
	}
}

func parseDiagnosticEvents(root map[string]interface{}) ([]Span, error) {
	rawEvents, ok := root["events"].([]interface{})
	if !ok {
		rawEvents = []interface{}{}
	}
	rootCoverage, _ := root["coverage"].(map[string]interface{})
	rootErrorCode := stringValue(root, "error_code")
	rootDuration, rootDurationKnown, rootDurationSource, rootDurationScope, rootDurationPresent := diagnosticRootDuration(root)
	spans := make([]Span, 0, len(rawEvents))
	for index, raw := range rawEvents {
		event, ok := raw.(map[string]interface{})
		if !ok || diagnosticMetaEvent(event) {
			continue
		}
		actionID := firstString(event, "action_id", "event_id")
		if actionID == "" {
			actionID = fmt.Sprintf("diagnostic-%d", index)
		}
		name := firstString(event, "name", "action_type", "source")
		if name == "" {
			name = fmt.Sprintf("event_%d", index)
		}
		startMS, startKnown := nonNegativeNumber(event["start_ms"])
		endMS, endKnown := nonNegativeNumber(event["end_ms"])
		_, durationKeyPresent := event["duration_ms"]
		duration, durationPresent := nonNegativeNumber(event["duration_ms"])
		if !durationPresent && startKnown && endKnown && endMS >= startMS {
			duration = endMS - startMS
			if !durationKeyPresent {
				durationPresent = endKnown
			}
		}
		status := strings.ToLower(firstString(event, "status"))
		if status == "" {
			status = ifDiagnosticError(event)
		}
		if status == "" {
			status = "ok"
		}
		status = normalizeSpanStatus(status)
		coverage := cloneMetadata(rootCoverage)
		state := strings.ToLower(firstString(root, "state"))
		projectedState := normalizeExecutionState(state)
		if coverageReasonPresent(coverage, "terminal_event_not_recorded") {
			projectedState = "unknown"
		}
		sourceIndex := index
		span := Span{SourceIndex: &sourceIndex, ExecutionID: sanitizeReference(firstString(root, "execution_id"), "execution"),
			Name: sanitizeSpanText(name, "name"), ActionID: sanitizeSpanText(actionID, "action_id"), SpanID: sanitizeSpanText(actionID, "span_id"), TraceID: sanitizeTraceID(firstString(event, "trace_id", "traceId")), Source: "local_execution_journal", Runtime: sanitizeRuntime(firstString(event, "source", "provider")), ErrorCode: sanitizeSpanText(firstString(event, "error_code"), "error_code"),
			StartNS: int64(startMS * 1e6), EndNS: int64(endMS * 1e6), DurationMS: duration, Status: status, ExecutionState: projectedState, DurationKnown: durationPresent, Coverage: coverage,
		}
		span.DurationSource, span.DurationScope = "recorded_event_metadata", "recorded_call_elapsed_not_compute"
		span.ExecutionDurationMS, span.ExecutionDurationKnown, span.ExecutionDurationPresent = rootDuration, rootDurationKnown, rootDurationPresent
		span.ExecutionDurationSource, span.ExecutionDurationScope = rootDurationSource, rootDurationScope
		spans = append(spans, span)
	}
	if len(spans) == 0 {
		state := strings.ToLower(firstString(root, "state"))
		if state == "" {
			state = "unknown"
		}
		projectedState := normalizeExecutionState(state)
		coverage := cloneMetadata(rootCoverage)
		if coverageReasonPresent(coverage, "terminal_event_not_recorded") {
			projectedState = "unknown"
		}
		span := Span{Name: "execution", SpanID: "execution", Source: "local_execution_journal", Status: projectedState, ErrorCode: sanitizeSpanText(rootErrorCode, "error_code"), ExecutionState: projectedState, Synthetic: true, Coverage: coverage, DurationMS: rootDuration, DurationKnown: rootDurationKnown, DurationSource: rootDurationSource, DurationScope: rootDurationScope, ExecutionDurationMS: rootDuration, ExecutionDurationKnown: rootDurationKnown, ExecutionDurationPresent: rootDurationPresent, ExecutionDurationSource: rootDurationSource, ExecutionDurationScope: rootDurationScope}
		return []Span{span}, nil
	}
	sortSpans(spans)
	return spans, nil
}

func coverageReasonPresent(coverage map[string]interface{}, wanted string) bool {
	reasons, ok := coverage["reasons"].([]interface{})
	if !ok {
		if typed, typedOK := coverage["reasons"].([]string); typedOK {
			for _, reason := range typed {
				if reason == wanted {
					return true
				}
			}
		}
		return false
	}
	for _, reason := range reasons {
		if value, ok := reason.(string); ok && value == wanted {
			return true
		}
	}
	return false
}

func diagnosticMetaEvent(event map[string]interface{}) bool {
	actionType := strings.ToLower(strings.TrimSpace(firstString(event, "action_type")))
	source := strings.ToLower(strings.TrimSpace(firstString(event, "source")))
	name := strings.ToLower(strings.TrimSpace(firstString(event, "name")))
	if actionType == "diagnostic_step" || actionType == "lifecycle" || source == "diagnostic_step" || source == "diagnostic_lifecycle" || source == "diagnostic_child_copy" {
		return true
	}
	return actionType == "" && (name == "diagnostic_step" || name == "lifecycle")
}

func ifDiagnosticError(event map[string]interface{}) string {
	if firstString(event, "error", "error_code") != "" {
		return "error"
	}
	return ""
}

func cloneMetadata(value map[string]interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	result := make(map[string]interface{})
	for key, item := range value {
		switch key {
		case "data_available", "logs_available", "trace_available", "values_redacted", "values_included", "archive_hydrated", "archive_unavailable", "partial", "events_truncated":
			if typed, ok := item.(bool); ok {
				result[key] = typed
			}
		case "events_total", "events_included":
			switch typed := item.(type) {
			case int, int64, float64, json.Number:
				result[key] = typed
			}
		case "source":
			if coverageEnum("source", item) {
				result[key] = item
			}
		case "trace_kind", "sampling", "payload_state", "distributed_trace":
			if coverageEnum(key, item) {
				result[key] = item
			}
		case "reasons":
			if reasons := coverageReasons(item); len(reasons) > 0 {
				result[key] = reasons
			}
		}
	}
	return result
}

var coverageEnums = map[string]map[string]struct{}{
	"source":            {"local": {}, "archive": {}},
	"trace_kind":        {"persisted_execution_journal": {}},
	"sampling":          {"not_sampled_at_projection": {}},
	"payload_state":     {"hot": {}, "archived": {}, "purged": {}, "expired": {}},
	"distributed_trace": {"not_loaded_use_trace_id": {}, "available": {}},
}

var coverageReasonAllowlist = map[string]struct{}{
	"payload_purged": {}, "payload_expired": {}, "archive_unavailable": {},
	"persisted_logs_truncated": {}, "lifecycle_not_recorded": {},
	"terminal_event_not_recorded": {}, "no_recorded_events": {},
	"interrupted_segment_calls_unknown": {},
}

func coverageEnum(key string, value interface{}) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	_, ok = coverageEnums[key][text]
	return ok
}

func coverageReasons(value interface{}) []string {
	raw, ok := value.([]interface{})
	if !ok {
		if typed, typedOK := value.([]string); typedOK {
			raw = make([]interface{}, len(typed))
			for i := range typed {
				raw[i] = typed[i]
			}
		} else {
			return nil
		}
	}
	reasons := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if ok {
			if _, allowed := coverageReasonAllowlist[text]; allowed {
				reasons = append(reasons, text)
			}
		}
	}
	return reasons
}

func Summarize(spans []Span) Summary {
	summary := Summary{SpanCount: len(spans), CriticalPath: []string{}}
	if len(spans) == 0 {
		return summary
	}
	minStart, maxEnd := int64(0), int64(0)
	var durationFallback float64
	var rootDuration float64
	var rootDurationKnown bool
	var rootDurationPresent bool
	var rootDurationSource, rootDurationScope string
	allTimed := true
	unconfirmedTerminal := false
	for _, span := range spans {
		if coverageReasonPresent(span.Coverage, "terminal_event_not_recorded") {
			unconfirmedTerminal = true
			summary.ExecutionState = "unknown"
		}
		if !rootDurationPresent && span.ExecutionDurationPresent {
			rootDuration, rootDurationKnown = span.ExecutionDurationMS, span.ExecutionDurationKnown
			rootDurationPresent = true
			rootDurationSource, rootDurationScope = span.ExecutionDurationSource, span.ExecutionDurationScope
		}
		if summary.ExecutionState == "" {
			summary.ExecutionState = span.ExecutionState
		}
		if strings.EqualFold(span.Status, "error") {
			summary.ErrorCount++
		}
		summary.RetryCount += span.RetryCount
		if span.DurationKnown && span.DurationMS > durationFallback {
			durationFallback = span.DurationMS
		}
		if span.DurationMS > summary.CriticalPathMS {
			summary.CriticalPathMS = span.DurationMS
			summary.SlowestSpan = span.Name
		}
		if span.StartNS > 0 && (minStart == 0 || span.StartNS < minStart) {
			minStart = span.StartNS
		}
		if span.EndNS > maxEnd {
			maxEnd = span.EndNS
		}
		if span.StartNS <= 0 || span.EndNS <= 0 {
			allTimed = false
		}
	}
	if summary.ErrorCount == 0 && (strings.EqualFold(summary.ExecutionState, "failed") || strings.EqualFold(summary.ExecutionState, "error") || strings.EqualFold(summary.ExecutionState, "timeout") || strings.EqualFold(summary.ExecutionState, "timed_out")) {
		summary.ErrorCount = 1
	}
	if rootDurationPresent {
		summary.DurationMS = rootDuration
		summary.DurationSource = rootDurationSource
		summary.DurationScope = rootDurationScope
		if rootDurationKnown {
			summary.DurationComplete = !unconfirmedTerminal
		}
	} else if minStart > 0 && maxEnd >= minStart {
		summary.DurationMS = float64(maxEnd-minStart) / 1e6
		if allTimed {
			summary.DurationSource = "recorded_span"
			summary.DurationScope = "span_elapsed_ms"
		} else {
			summary.DurationSource = "recorded_span_estimate"
			summary.DurationScope = "span_elapsed_estimate_ms"
		}
		summary.DurationComplete = allTimed
	} else if durationFallback > 0 {
		summary.DurationMS = durationFallback
		summary.DurationSource = "recorded_span_estimate"
		summary.DurationScope = "span_duration_estimate_ms"
	} else {
		summary.DurationSource = "unknown"
		summary.DurationScope = "unknown"
	}
	if unconfirmedTerminal {
		summary.ExecutionState = "unknown"
		summary.DurationComplete = false
	}
	if !terminalExecutionState(summary.ExecutionState) {
		summary.DurationComplete = false
	}
	summary.CriticalPath, summary.CriticalPathMS = criticalPath(spans)
	return summary
}

func terminalExecutionState(state string) bool {
	switch normalizeExecutionState(state) {
	case "completed", "failed", "timeout", "cancelled", "refused":
		return true
	default:
		return false
	}
}

func diagnosticRootDuration(root map[string]interface{}) (float64, bool, string, string, bool) {
	if rawTiming, present := root["timing"]; present {
		if timing, ok := rawTiming.(map[string]interface{}); ok {
			if raw, exists := timing["wall_elapsed_ms"]; exists {
				if duration, known := nonNegativeNumber(raw); known {
					return duration, true, "timing.wall_elapsed_ms", "wall_elapsed_ms", true
				}
			}
		}
		return 0, false, "unknown", "unknown", true
	}
	if declared, present := root["duration_scope"]; present {
		scope, _ := declared.(string)
		if scope == "wall_elapsed_ms" || scope == "legacy_runtime_duration_ms" {
			if duration, known := nonNegativeNumber(root["duration_ms"]); known {
				source := "legacy_runtime_duration_ms"
				if scope == "wall_elapsed_ms" {
					source = "declared_wall_elapsed_ms"
					if root["duration_source"] == "timing.wall_elapsed_ms" {
						source = "timing.wall_elapsed_ms"
					}
				}
				return duration, true, source, scope, true
			}
			return 0, false, "unknown", scope, true
		}
		return 0, false, "unknown", "unknown", true
	}
	if raw, ok := root["duration_ms"]; ok && raw != nil {
		if duration, known := nonNegativeNumber(raw); known {
			return duration, true, "legacy_runtime_duration_ms", "legacy_runtime_duration_ms", true
		}
	}
	if usage, ok := root["usage"].(map[string]interface{}); ok {
		if raw, exists := usage["duration_ms"]; exists && raw != nil {
			if duration, known := nonNegativeNumber(raw); known {
				return duration, true, "legacy_runtime_duration_ms", "legacy_runtime_duration_ms", true
			}
		}
	}
	return 0, false, "unknown", "unknown", false
}

func nonNegativeNumber(value interface{}) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	case float64:
		number = typed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return 0, false
	}
	return number, true
}

var sensitiveMetadataValue = regexp.MustCompile(`(?i)(@[a-z0-9]|(?:sk|pk|rk)_(?:live|test)_[a-z0-9_-]{6,}|sk-proj-[a-z0-9_-]{6,}|(?:ghp|glpat|github_pat|AIza|rk_live|rk_test)[_-]?[a-z0-9_-]{6,}|(?:^|[^a-z0-9])(?:sk|pk|amb|amc)[_-][a-z0-9_-]{6,}|(?:xox[baprs]|ya29)[._-][a-z0-9_-]+|AKIA[A-Z0-9]{16}|eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+)`)

var phoneLikeValue = regexp.MustCompile(`(?i)^\+?(?:[0-9][ .-]?){10,15}$`)
var phoneLikeEmbeddedValue = regexp.MustCompile(`(?i)(?:\+?[0-9][ .-]?){10,15}`)

var pseudonymizationKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("observe: cannot initialize pseudonymization key")
	}
	return key
}()

func unsafeMetadataValue(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(value, "@") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || sensitiveMetadataValue.MatchString(value) || phoneLike(value)
}

func phoneLike(value string) bool {
	if phoneLikeValue.MatchString(value) {
		return true
	}
	return phoneLikeEmbeddedValue.MatchString(value)
}

func hasCredentialMarker(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(value, "@") || strings.Contains(lower, "secret") || sensitiveMetadataValue.MatchString(value) || phoneLike(value)
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 128 || hasCredentialMarker(value) {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:/-", r) {
			continue
		}
		return false
	}
	return true
}

func sanitizeSpanText(value, field string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if field == "span_id" && isCanonicalSpanID(value) {
		return strings.ToLower(value)
	}
	if field == "error_code" {
		switch value {
		case "tool_failed", "guest_panic", "wasm_guest_panic", "wasm_execution_failed", "execution_timeout", "timeout", "token_limit_exceeded", "authorization_refused", "host_operation_unsupported", "code_syntax_error", "code_reference_error", "code_type_error", "decision_provider_unavailable", "input_resolution_failed", "output_validation_failed", "execution_cancelled", "cancelled", "approval_required", "rate_limited", "provider_overloaded", "unknown":
			return value
		}
	}
	// Names, action IDs and unknown error codes are free-form metadata.  Even
	// values that look technical remain HMAC-pseudonymized unless they are one
	// of the closed error-code enums above or a canonical span reference.
	return pseudonymize(value)
}

func isCanonicalSpanID(value string) bool {
	if len(value) != 16 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func sanitizeRuntime(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "deno", "wasm", "wasi", "runtime", "artifact", "llm", "provider":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		if value == "" {
			return ""
		}
		return "[redacted]"
	}
}

func normalizeExecutionState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "completed", "success", "succeeded":
		return "completed"
	case "failed", "error":
		return "failed"
	case "timeout", "timed_out":
		return "timeout"
	case "cancelled", "canceled":
		return "cancelled"
	case "human_waiting", "waiting_for_form", "waiting_for_approval":
		return "human_waiting"
	case "refused", "denied", "policy_denied", "admission_denied":
		return "refused"
	case "running":
		return "running"
	case "pending", "waiting", "queued":
		return "pending"
	default:
		return "unknown"
	}
}

func sanitizeTraceID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 32 {
		for _, r := range value {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return pseudonymize(value)
			}
		}
		return strings.ToLower(value)
	}
	if value == "" {
		return ""
	}
	return pseudonymize(value)
}

func pseudonymize(value string) string {
	mac := hmac.New(sha256.New, pseudonymizationKey)
	_, _ = mac.Write([]byte(value))
	return "ref_" + hex.EncodeToString(mac.Sum(nil)[:8])
}

func Explain(spans []Span) map[string]interface{} {
	summary := Summarize(spans)
	errors := make([]string, 0)
	errorSpans := make([]Span, 0)
	for _, span := range spans {
		if strings.EqualFold(span.Status, "error") {
			errors = append(errors, span.Name)
			errorSpans = append(errorSpans, span)
		}
	}
	if len(errors) == 0 && (strings.EqualFold(summary.ExecutionState, "failed") || strings.EqualFold(summary.ExecutionState, "error") || strings.EqualFold(summary.ExecutionState, "timeout") || strings.EqualFold(summary.ExecutionState, "timed_out")) {
		errors = append(errors, "execution")
	}
	return map[string]interface{}{
		"summary":     summary,
		"errors":      errors,
		"error_spans": errorSpans,
		"diagnosis":   diagnosis(summary),
	}
}

func Compare(left, right []Span) map[string]interface{} {
	a, b := Summarize(left), Summarize(right)
	comparable := a.DurationScope == b.DurationScope && a.DurationScope != "unknown" && a.DurationScope != ""
	valuesKnown := a.DurationSource != "" && a.DurationSource != "unknown" && b.DurationSource != "" && b.DurationSource != "unknown"
	var durationDelta interface{}
	if comparable && valuesKnown {
		durationDelta = b.DurationMS - a.DurationMS
	}
	return map[string]interface{}{
		"baseline":                  a,
		"candidate":                 b,
		"baseline_errors":           Explain(left)["error_spans"],
		"candidate_errors":          Explain(right)["error_spans"],
		"duration_delta_ms":         durationDelta,
		"duration_scope_compatible": comparable,
		"duration_delta_complete":   comparable && valuesKnown && a.DurationComplete && b.DurationComplete,
		"error_delta":               b.ErrorCount - a.ErrorCount,
		"retry_delta":               b.RetryCount - a.RetryCount,
	}
}

func walk(value interface{}, spans *[]Span) {
	switch current := value.(type) {
	case []interface{}:
		for _, item := range current {
			walk(item, spans)
		}
	case map[string]interface{}:
		if span, ok := decodeSpan(current); ok {
			*spans = append(*spans, span)
			return
		}
		for _, child := range current {
			walk(child, spans)
		}
	}
}

func decodeSpan(raw map[string]interface{}) (Span, bool) {
	name := stringValue(raw, "name")
	if name == "" || (raw["spanId"] == nil && raw["span_id"] == nil && raw["duration_ms"] == nil) {
		return Span{}, false
	}
	start := int64Value(raw, "startTimeUnixNano", "start_unix_nano")
	end := int64Value(raw, "endTimeUnixNano", "end_unix_nano")
	duration := floatValue(raw, "duration_ms")
	if duration == 0 && end >= start && start > 0 {
		duration = float64(end-start) / 1e6
	}
	status := strings.ToLower(stringValue(raw, "status"))
	if status == "" {
		if statusMap, ok := raw["status"].(map[string]interface{}); ok {
			status = strings.ToLower(stringValue(statusMap, "code"))
		}
	}
	if status == "status_code_error" || status == "2" {
		status = "error"
	}
	if status == "" {
		status = "ok"
	}
	status = normalizeSpanStatus(status)
	_, durationKnown := nonNegativeNumber(raw["duration_ms"])
	return Span{Name: sanitizeSpanText(name, "name"), SpanID: sanitizeSpanText(firstString(raw, "spanId", "span_id"), "span_id"), ParentSpanID: sanitizeSpanText(firstString(raw, "parentSpanId", "parent_span_id"), "span_id"), TraceID: sanitizeTraceID(firstString(raw, "traceId", "trace_id")), StartNS: start, EndNS: end, DurationMS: duration, DurationKnown: durationKnown || end > start, Status: status, RetryCount: int(int64Value(raw, "retry_count"))}, true
}

func normalizeSpanStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "ok", "success", "succeeded", "status_code_ok":
		return "ok"
	case "error", "failed", "timeout", "timed_out", "status_code_error", "2":
		return "error"
	case "cancelled", "canceled":
		return "cancelled"
	case "human_waiting", "waiting_for_form", "waiting_for_approval":
		return "human_waiting"
	case "refused", "denied", "policy_denied", "admission_denied":
		return "refused"
	default:
		return "unknown"
	}
}

func sortSpans(spans []Span) {
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].StartNS != spans[j].StartNS {
			return spans[i].StartNS < spans[j].StartNS
		}
		if spans[i].Source == "local_execution_journal" && spans[j].Source == "local_execution_journal" {
			return false // Equal/unknown timestamps retain the persisted journal order.
		}
		if spans[i].Name != spans[j].Name {
			return spans[i].Name < spans[j].Name
		}
		return spans[i].SpanID < spans[j].SpanID
	})
}

func criticalPath(spans []Span) ([]string, float64) {
	byParent := map[string][]Span{}
	roots := make([]Span, 0)
	ids := map[string]bool{}
	for _, span := range spans {
		ids[span.SpanID] = true
		byParent[span.ParentSpanID] = append(byParent[span.ParentSpanID], span)
	}
	for _, span := range spans {
		if span.ParentSpanID == "" || !ids[span.ParentSpanID] {
			roots = append(roots, span)
		}
	}
	var best []string
	var bestDuration float64
	var visit func(Span, []string, float64)
	visit = func(span Span, path []string, duration float64) {
		path = append(path, span.Name)
		duration += span.DurationMS
		children := byParent[span.SpanID]
		if len(children) == 0 && duration > bestDuration {
			best, bestDuration = append([]string(nil), path...), duration
		}
		for _, child := range children {
			visit(child, path, duration)
		}
	}
	for _, root := range roots {
		visit(root, nil, 0)
	}
	return best, bestDuration
}

func diagnosis(summary Summary) string {
	if summary.ErrorCount > 0 {
		return fmt.Sprintf("run contains %d error span(s); inspect the reported errors first", summary.ErrorCount)
	}
	if summary.RetryCount > 0 {
		return fmt.Sprintf("run completed with %d retry attempt(s)", summary.RetryCount)
	}
	return "no error or retry was detected in the supplied trace"
}

func stringValue(values map[string]interface{}, key string) string {
	value, _ := values[key].(string)
	return value
}

func firstString(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(values, key); value != "" {
			return value
		}
	}
	return ""
}

func int64Value(values map[string]interface{}, keys ...string) int64 {
	for _, key := range keys {
		switch value := values[key].(type) {
		case json.Number:
			result, _ := value.Int64()
			return result
		case string:
			result, _ := strconv.ParseInt(value, 10, 64)
			return result
		case float64:
			return int64(value)
		}
	}
	return 0
}

func floatValue(values map[string]interface{}, key string) float64 {
	value, ok := nonNegativeNumber(values[key])
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

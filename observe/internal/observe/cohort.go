package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	CohortSchemaVersion      = "agentmaurice.execution_cohort/v1"
	DiagnosticSchemaVersion  = "agentmaurice.execution_diagnostic/v1"
	CohortMaxReports         = 1000
	CohortMaxEventsPerReport = 5000
	CohortMaxGroups          = 1000
	CohortMaxGroupBy         = 3
)

// DiagnosticEvent is the redacted event subset accepted by the cohort rail.
// Raw prompt, credential and business payload fields are intentionally absent.
type DiagnosticEvent struct {
	ActionID            string   `json:"action_id,omitempty"`
	ActionType          string   `json:"action_type,omitempty"`
	Source              string   `json:"source,omitempty"`
	Name                string   `json:"name,omitempty"`
	Error               string   `json:"error,omitempty"`
	Status              string   `json:"status,omitempty"`
	CostStatus          string   `json:"cost_status,omitempty"`
	DurationMS          float64  `json:"duration_ms,omitempty"`
	Model               string   `json:"model,omitempty"`
	Provider            string   `json:"provider,omitempty"`
	AttemptID           string   `json:"attempt_id,omitempty"`
	RecordedAtMS        int64    `json:"recorded_at_ms,omitempty"`
	TokensIn            *int64   `json:"tokens_in,omitempty"`
	TokensOut           *int64   `json:"tokens_out,omitempty"`
	CacheReadTokens     *int64   `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens *int64   `json:"cache_creation_tokens,omitempty"`
	CostUSD             *float64 `json:"cost_usd,omitempty"`
}

type DiagnosticReport struct {
	SchemaVersion        string             `json:"schema_version"`
	AgentID              string             `json:"agent_id,omitempty"`
	ExecutionID          string             `json:"execution_id"`
	TraceID              string             `json:"trace_id,omitempty"`
	WorkflowID           string             `json:"workflow_id"`
	WorkflowVersion      string             `json:"workflow_version,omitempty"`
	Model                string             `json:"model,omitempty"`
	Status               string             `json:"state"`
	DurationMS           float64            `json:"duration_ms"`
	DurationSource       string             `json:"duration_source,omitempty"`
	DurationScope        string             `json:"duration_scope,omitempty"`
	DurationKnown        bool               `json:"-"`
	ErrorCode            string             `json:"error_code,omitempty"`
	CreatedAt            string             `json:"created_at,omitempty"`
	UpdatedAt            string             `json:"updated_at,omitempty"`
	Events               []DiagnosticEvent  `json:"events,omitempty"`
	Coverage             DiagnosticCoverage `json:"coverage,omitempty"`
	schemaVersionPresent bool
}

// MarshalJSON preserves an unknown zero and the normalized duration axis.
func (r DiagnosticReport) MarshalJSON() ([]byte, error) {
	type alias DiagnosticReport
	value := alias(r)
	value.DurationScope = reportDurationScope(r)
	encoded, err := json.Marshal(value)
	if err != nil || durationKnown(r) {
		return encoded, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, err
	}
	document["duration_ms"] = json.RawMessage("null")
	return json.Marshal(document)
}

// UnmarshalJSON preserves the distinction between an absent duration and an
// explicit zero, and accepts the canonical execution shape usage.duration_ms.
func (r *DiagnosticReport) UnmarshalJSON(data []byte) error {
	type alias DiagnosticReport
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	decoded.DurationKnown = false
	decoded.DurationSource = "unknown"
	decoded.DurationScope = "unknown"
	// A report decoded from JSON must carry the schema marker; an absent marker
	// is therefore distinguishable from a legacy in-memory struct literal.
	decoded.schemaVersionPresent = true
	var envelope map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return err
	}
	decoded.DurationMS, decoded.DurationKnown, decoded.DurationSource, decoded.DurationScope, _ = diagnosticRootDuration(envelope)
	*r = DiagnosticReport(decoded)
	return nil
}

type DiagnosticCoverage struct {
	Partial            bool     `json:"partial,omitempty"`
	EventsTruncated    bool     `json:"events_truncated,omitempty"`
	ArchiveUnavailable bool     `json:"archive_unavailable,omitempty"`
	PayloadState       string   `json:"payload_state,omitempty"`
	Reasons            []string `json:"reasons,omitempty"`
}

type CohortWindow struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

type CohortQuery struct {
	Reports         []DiagnosticReport `json:"reports,omitempty"`
	BaselineReports []DiagnosticReport `json:"-"`
	Window          CohortWindow       `json:"window,omitempty"`
	WorkflowID      string             `json:"workflow_id,omitempty"`
	WorkflowVersion string             `json:"workflow_version,omitempty"`
	Model           string             `json:"model,omitempty"`
	Status          string             `json:"status,omitempty"`
	GroupBy         []string           `json:"group_by,omitempty"`
}

type CohortResult struct {
	SchemaVersion     string            `json:"schema_version"`
	Status            string            `json:"status"`
	Filters           CohortQuery       `json:"filters"`
	Groups            []CohortGroup     `json:"groups"`
	Coverage          CohortCoverage    `json:"coverage"`
	Provenance        CohortProvenance  `json:"provenance"`
	ResultTruncated   bool              `json:"result_truncated"`
	TruncationReasons []string          `json:"truncation_reasons"`
	Comparison        *CohortComparison `json:"comparison,omitempty"`
}

// CohortComparison is optional so the original single-cohort response stays
// backward compatible while exposing a bounded baseline comparison.
type CohortComparison struct {
	Baseline  *CohortResult      `json:"baseline"`
	Candidate *CohortResult      `json:"candidate"`
	Deltas    []CohortGroupDelta `json:"deltas"`
}

type CohortGroupDelta struct {
	Dimensions          map[string]string `json:"dimensions"`
	ExecutionCountDelta int               `json:"execution_count_delta"`
	ErrorRateDelta      *float64          `json:"error_rate_delta"`
	P50Delta            *float64          `json:"p50_delta,omitempty"`
	P95Delta            *float64          `json:"p95_delta,omitempty"`
	CostDeltaUSD        *float64          `json:"cost_delta_usd,omitempty"`
	CostDeltaUnknown    bool              `json:"cost_delta_unknown"`
	LatencyDeltaUnknown bool              `json:"latency_delta_unknown"`
}

type CohortGroup struct {
	Dimensions                 map[string]string `json:"dimensions"`
	ExecutionCount             int               `json:"execution_count"`
	EventCount                 int               `json:"event_count"`
	ErrorCount                 int               `json:"error_count"`
	RefusedCount               int               `json:"refused_count"`
	TimeoutCount               int               `json:"timeout_count"`
	UnknownStatusCount         int               `json:"unknown_status_count"`
	HumanWaitingCount          int               `json:"human_waiting_count"`
	CancelledCount             int               `json:"cancelled_count"`
	NonTerminalCount           int               `json:"non_terminal_count"`
	LatencyUnknownCount        int               `json:"latency_unknown_count"`
	LatencyScope               string            `json:"latency_scope"`
	LatencyMS                  CohortQuantiles   `json:"latency_ms"`
	Cost                       CohortCost        `json:"cost"`
	CacheReadTokensTotal       int64             `json:"cache_read_tokens_total"`
	CacheCreationTokensTotal   int64             `json:"cache_creation_tokens_total"`
	CacheKnownEvents           int               `json:"cache_known_events"`
	CacheUnknownLLMEvents      int               `json:"cache_unknown_llm_events"`
	CacheUnknownReadEvents     int               `json:"cache_unknown_read_events"`
	CacheUnknownCreationEvents int               `json:"cache_unknown_creation_events"`
	ExecutionIDs               []string          `json:"execution_ids,omitempty"`
	TraceIDs                   []string          `json:"trace_ids,omitempty"`
	RefusedLatencyMS           CohortQuantiles   `json:"refused_latency_ms"`
	CancelledLatencyMS         CohortQuantiles   `json:"cancelled_latency_ms"`
}

type CohortQuantiles struct {
	Count  int     `json:"count"`
	P50    float64 `json:"p50"`
	P95    float64 `json:"p95"`
	values []float64
}

func (q CohortQuantiles) MarshalJSON() ([]byte, error) {
	var p50, p95 interface{}
	if q.Count > 0 {
		p50, p95 = q.P50, q.P95
	}
	return json.Marshal(struct {
		Count int         `json:"count"`
		P50   interface{} `json:"p50"`
		P95   interface{} `json:"p95"`
	}{q.Count, p50, p95})
}

type CohortCost struct {
	Unit              string  `json:"unit"`
	Source            string  `json:"source"`
	Status            string  `json:"status"`
	KnownUSD          float64 `json:"known_usd"`
	KnownCount        int     `json:"known_count"`
	ExactUSD          float64 `json:"exact_usd"`
	EstimatedUSD      float64 `json:"estimated_usd"`
	UnclassifiedUSD   float64 `json:"unclassified_usd"`
	ExactCount        int     `json:"exact_count"`
	EstimatedCount    int     `json:"estimated_count"`
	UnclassifiedCount int     `json:"unclassified_count"`
	UnknownCount      int     `json:"unknown_count"`
	Incomplete        bool    `json:"incomplete"`
}

type CohortCoverage struct {
	ReportsSeen           int      `json:"reports_seen"`
	ReportsDeduplicated   int      `json:"reports_deduplicated"`
	ReportsMatched        int      `json:"reports_matched"`
	EventsSeen            int      `json:"events_seen"`
	EventsMatched         int      `json:"events_matched"`
	CostKnownCount        int      `json:"cost_known_count"`
	CostUnknownCount      int      `json:"cost_unknown_count"`
	ValuesExcluded        bool     `json:"values_excluded"`
	Partial               bool     `json:"partial"`
	EventsTruncated       bool     `json:"events_truncated"`
	Complete              bool     `json:"complete"`
	DuplicateConflicts    int      `json:"duplicate_conflicts"`
	CrossCohortDuplicates int      `json:"cross_cohort_duplicates"`
	Reasons               []string `json:"reasons,omitempty"`
}

type CohortProvenance struct {
	Source           string `json:"source"`
	BackendAccess    bool   `json:"backend_access"`
	InputSchema      string `json:"input_schema"`
	RedactedOnly     bool   `json:"redacted_only"`
	References       string `json:"references"`
	ModelSemantics   string `json:"model_semantics"`
	MetricsSemantics string `json:"metrics_semantics"`
}

// QueryCohort computes deterministic metrics from the complete inline corpus.
// It has no storage or backend access and never returns raw input values.
func QueryCohort(q CohortQuery) (CohortResult, error) {
	return QueryCohortContext(context.Background(), q)
}

// QueryCohortContext is the bounded variant used by the MCP handler.
func QueryCohortContext(ctx context.Context, q CohortQuery) (CohortResult, error) {
	if len(q.BaselineReports) > 0 {
		if len(q.Reports)+len(q.BaselineReports) > CohortMaxReports {
			return CohortResult{}, fmt.Errorf("combined reports exceed %d", CohortMaxReports)
		}
		filters := q
		filters.BaselineReports = nil
		candidate, err := queryCohortSingleContext(ctx, filters)
		if err != nil {
			return CohortResult{}, err
		}
		baselineFilters := filters
		baselineFilters.Reports = q.BaselineReports
		baseline, err := queryCohortSingleContext(ctx, baselineFilters)
		if err != nil {
			return CohortResult{}, err
		}
		crossDuplicates := crossCohortDuplicateCount(q.Reports, q.BaselineReports)
		if crossDuplicates > 0 {
			candidate.Coverage.CrossCohortDuplicates = crossDuplicates
			candidate.Coverage.Partial = true
			candidate.Coverage.Complete = false
			candidate.Coverage.Reasons = appendUniqueReason(candidate.Coverage.Reasons, "cross_cohort_duplicate")
			for index := range candidate.Groups {
				candidate.Groups[index].Cost.Incomplete = true
			}
			baseline.Coverage.CrossCohortDuplicates = crossDuplicates
			baseline.Coverage.Partial = true
			baseline.Coverage.Complete = false
			baseline.Coverage.Reasons = appendUniqueReason(baseline.Coverage.Reasons, "cross_cohort_duplicate")
			for index := range baseline.Groups {
				baseline.Groups[index].Cost.Incomplete = true
			}
		}
		candidateSnapshot := candidate
		candidateSnapshot.Groups = append([]CohortGroup(nil), candidate.Groups...)
		candidateSnapshot.Comparison = nil
		if candidate.Coverage.Partial || baseline.Coverage.Partial || !candidate.Coverage.Complete || !baseline.Coverage.Complete {
			candidate.Status = "partial"
			candidate.Coverage.Partial = true
			candidate.Coverage.Complete = false
			if baseline.Coverage.Partial || !baseline.Coverage.Complete {
				candidate.Coverage.Reasons = appendUniqueReason(candidate.Coverage.Reasons, "baseline_partial")
			}
		}
		if baseline.Coverage.Partial || !baseline.Coverage.Complete {
			baseline.Status = "partial"
			baseline.Coverage.Partial = true
			baseline.Coverage.Complete = false
		}
		if crossDuplicates > 0 {
			candidateSnapshot.Status = "partial"
			candidateSnapshot.Coverage.Partial = true
			candidateSnapshot.Coverage.Complete = false
		}
		candidate.Comparison = &CohortComparison{Baseline: &baseline, Candidate: &candidateSnapshot, Deltas: compareGroups(baseline.Groups, candidate.Groups, baseline.Coverage.Partial || candidate.Coverage.Partial)}
		return candidate, nil
	}
	return queryCohortSingleContext(ctx, q)
}

func queryCohortSingleContext(ctx context.Context, q CohortQuery) (CohortResult, error) {
	if len(q.Reports) == 0 {
		return CohortResult{}, fmt.Errorf("reports must not be empty")
	}
	if len(q.Reports) > CohortMaxReports {
		return CohortResult{}, fmt.Errorf("reports exceed %d", CohortMaxReports)
	}
	if len(q.GroupBy) > CohortMaxGroupBy {
		return CohortResult{}, fmt.Errorf("group_by supports at most %d dimensions", CohortMaxGroupBy)
	}
	if q.Status != "" && !validStatusFilter(q.Status) {
		return CohortResult{}, fmt.Errorf("unsupported status filter")
	}
	allowed := map[string]bool{"workflow_id": true, "workflow_version": true, "model": true, "status": true}
	seen := map[string]bool{}
	for _, dimension := range q.GroupBy {
		if !allowed[dimension] || seen[dimension] {
			return CohortResult{}, fmt.Errorf("unsupported group_by dimension")
		}
		seen[dimension] = true
	}
	start, end, err := parseWindow(q.Window)
	if err != nil {
		return CohortResult{}, err
	}
	groups := map[string]*CohortGroup{}
	dimensionOrigins := map[string]map[string]string{}
	resultTruncated := false
	coverage := CohortCoverage{ReportsSeen: len(q.Reports), ValuesExcluded: true, Reasons: []string{}}
	uniqueReports, deduplicated, conflicts, dedupErr := deduplicateReports(ctx, q.Reports)
	if dedupErr != nil {
		return CohortResult{}, dedupErr
	}
	coverage.ReportsDeduplicated = deduplicated
	coverage.DuplicateConflicts = conflicts
	if conflicts > 0 {
		coverage.Reasons = append(coverage.Reasons, "duplicate_conflict")
		coverage.Partial = true
	}
	for _, report := range uniqueReports {
		select {
		case <-ctx.Done():
			return CohortResult{}, fmt.Errorf("cohort query timeout: %w", ctx.Err())
		default:
		}
		if strings.TrimSpace(report.ExecutionID) == "" {
			return CohortResult{}, fmt.Errorf("execution_id required")
		}
		if report.SchemaVersion != "" && report.SchemaVersion != DiagnosticSchemaVersion {
			return CohortResult{}, fmt.Errorf("unsupported report schema_version")
		}
		if report.SchemaVersion == "" && report.schemaVersionPresent {
			coverage.Partial = true
			coverage.Reasons = appendUniqueReason(coverage.Reasons, "missing_schema_version")
		}
		if len(report.Events) > CohortMaxEventsPerReport {
			return CohortResult{}, fmt.Errorf("events exceed %d", CohortMaxEventsPerReport)
		}
		coverage.EventsSeen += len(report.Events)
		if !matchesReport(report, q, start, end) {
			if matchesNonStatusFilters(report, q) {
				if reason := windowExclusionReason(report, start, end); reason != "" {
					coverage.Partial = true
					coverage.Reasons = appendUniqueReason(coverage.Reasons, reason)
				} else if reportWithinWindow(report, start, end) && q.Status != "" {
					reportStatus := effectiveReportStatus(report)
					if reportStatus == "unknown" {
						coverage.Partial = true
						coverage.Reasons = appendUniqueReason(coverage.Reasons, "unknown_status")
					} else if reportStatus == "pending" || reportStatus == "human_waiting" {
						coverage.Partial = true
						coverage.Reasons = appendUniqueReason(coverage.Reasons, "nonterminal_status")
					}
				}
			}
			continue
		}
		coverage.ReportsMatched++
		if report.Coverage.Partial {
			coverage.Partial = true
			coverage.Reasons = appendUniqueReason(coverage.Reasons, "report_partial")
		}
		if report.Coverage.EventsTruncated {
			coverage.EventsTruncated = true
			coverage.Partial = true
			coverage.Reasons = appendUniqueReason(coverage.Reasons, "events_truncated")
		}
		payloadIncomplete := normalizeReportCoverage(report, &coverage)
		if hasReason(report.Coverage.Reasons, "terminal_event_not_recorded") {
			coverage.Reasons = appendUniqueReason(coverage.Reasons, "terminal_event_not_recorded")
		}
		if err := checkRedactedDimensionCollision(dimensionOrigins, report, q.GroupBy); err != nil {
			return CohortResult{}, err
		}
		key, dimensions := groupKey(report, q.GroupBy)
		group := groups[key]
		if group == nil {
			if len(groups) >= CohortMaxGroups {
				return CohortResult{}, fmt.Errorf("cohort has more than %d groups", CohortMaxGroups)
			}
			group = &CohortGroup{Dimensions: dimensions}
			groups[key] = group
		}
		group.ExecutionCount++
		terminal := effectiveReportStatus(report)
		reportScope := reportDurationScope(report)
		if terminal != "pending" && terminal != "human_waiting" && terminal != "unknown" && reportScope != "unknown" && durationKnown(report) && validNonNegative(report.DurationMS) {
			if group.LatencyScope == "" {
				group.LatencyScope = reportScope
			} else if group.LatencyScope != reportScope && group.LatencyScope != "mixed_incomparable" {
				group.LatencyScope = "mixed_incomparable"
				group.LatencyMS = CohortQuantiles{}
				group.LatencyUnknownCount = group.ExecutionCount
				coverage.Partial = true
				coverage.Reasons = appendUniqueReason(coverage.Reasons, "mixed_latency_scopes")
			}
		}
		executionReference := sanitizeReference(report.ExecutionID, "execution")
		traceReference := sanitizeReference(report.TraceID, "trace")
		executionRefsTruncated := appendReference(&group.ExecutionIDs, executionReference)
		traceRefsTruncated := appendReference(&group.TraceIDs, traceReference)
		if executionRefsTruncated || traceRefsTruncated {
			resultTruncated = true
		}
		if executionReference == "" || (strings.TrimSpace(report.TraceID) != "" && traceReference == "") {
			coverage.Partial = true
			coverage.Reasons = appendUniqueReason(coverage.Reasons, "invalid_reference_redacted")
		}
		metricEvents := 0
		if terminal == "cancelled" {
			group.CancelledCount++
			addStatusLatency(&group.CancelledLatencyMS, &group.LatencyUnknownCount, &coverage, report)
		} else if terminal == "refused" {
			group.RefusedCount++
			addStatusLatency(&group.RefusedLatencyMS, &group.LatencyUnknownCount, &coverage, report)
		} else if terminal == "timeout" {
			group.TimeoutCount++
			if durationKnown(report) && validNonNegative(report.DurationMS) {
				group.LatencyMS.add(report.DurationMS)
			} else {
				group.LatencyUnknownCount++
				coverage.Partial = true
				coverage.Reasons = appendUniqueReason(coverage.Reasons, "missing_duration")
			}
		} else if terminal == "pending" {
			group.NonTerminalCount++
			group.LatencyUnknownCount++
			coverage.Partial = true
			coverage.Reasons = appendUniqueReason(coverage.Reasons, "non_terminal_execution")
		} else if terminal == "human_waiting" {
			group.HumanWaitingCount++
			group.LatencyUnknownCount++
			coverage.Partial = true
			coverage.Reasons = appendUniqueReason(coverage.Reasons, "human_waiting")
		} else if terminal == "unknown" {
			group.UnknownStatusCount++
			group.LatencyUnknownCount++
			coverage.Partial = true
			coverage.Reasons = appendUniqueReason(coverage.Reasons, "unknown_status")
		} else if durationKnown(report) && validNonNegative(report.DurationMS) {
			group.LatencyMS.add(report.DurationMS)
		} else {
			group.LatencyUnknownCount++
			coverage.Partial = true
			coverage.Reasons = appendUniqueReason(coverage.Reasons, "missing_duration")
		}
		if report.Coverage.Partial || report.Coverage.EventsTruncated || payloadIncomplete {
			group.Cost.Incomplete = true
		}
		reportErrored := terminal == "error" || terminal == "timeout"
		for _, event := range report.Events {
			group.EventCount++
			coverage.EventsMatched++
			if !diagnosticMetricEvent(event) {
				continue
			}
			metricEvents++
			if isLLMEvent(event) {
				if event.CacheReadTokens != nil && *event.CacheReadTokens >= 0 {
					if *event.CacheReadTokens > math.MaxInt64-group.CacheReadTokensTotal {
						return CohortResult{}, fmt.Errorf("cache_read_tokens overflow")
					}
					group.CacheReadTokensTotal += positiveTokenCount(event.CacheReadTokens)
				} else {
					group.CacheUnknownReadEvents++
				}
				if event.CacheCreationTokens != nil && *event.CacheCreationTokens >= 0 {
					if *event.CacheCreationTokens > math.MaxInt64-group.CacheCreationTokensTotal {
						return CohortResult{}, fmt.Errorf("cache_creation_tokens overflow")
					}
					group.CacheCreationTokensTotal += positiveTokenCount(event.CacheCreationTokens)
				} else {
					group.CacheUnknownCreationEvents++
				}
				if (event.CacheReadTokens != nil && *event.CacheReadTokens >= 0) || (event.CacheCreationTokens != nil && *event.CacheCreationTokens >= 0) {
					group.CacheKnownEvents++
				} else {
					group.CacheUnknownLLMEvents++
				}
			}
			costStatus := normalizeCostStatus(event.CostStatus)
			if event.CostUSD == nil || !validNonNegative(*event.CostUSD) || costStatus == "unknown" || (costStatus == "unclassified" && *event.CostUSD == 0) {
				group.Cost.UnknownCount++
				coverage.CostUnknownCount++
			} else {
				if group.Cost.KnownUSD > math.MaxFloat64-*event.CostUSD {
					return CohortResult{}, fmt.Errorf("cost overflow")
				}
				group.Cost.KnownUSD += *event.CostUSD
				group.Cost.KnownCount++
				coverage.CostKnownCount++
				switch costStatus {
				case "exact":
					group.Cost.ExactUSD += *event.CostUSD
					group.Cost.ExactCount++
				case "estimated":
					group.Cost.EstimatedUSD += *event.CostUSD
					group.Cost.EstimatedCount++
				default:
					group.Cost.UnclassifiedUSD += *event.CostUSD
					group.Cost.UnclassifiedCount++
				}
			}
		}
		if metricEvents == 0 {
			group.Cost.Incomplete = true
		}
		if reportErrored {
			group.ErrorCount++
		}
	}
	resultGroups := make([]CohortGroup, 0, len(groups))
	for _, group := range groups {
		if group.LatencyScope == "" {
			group.LatencyScope = "unknown"
		}
		if group.LatencyScope == "mixed_incomparable" {
			group.LatencyMS = CohortQuantiles{}
			group.RefusedLatencyMS = CohortQuantiles{}
			group.CancelledLatencyMS = CohortQuantiles{}
			group.LatencyUnknownCount = group.ExecutionCount
		}
		group.LatencyMS.finish()
		finalizeCost(&group.Cost)
		group.Cost.Incomplete = group.Cost.Incomplete || group.Cost.UnknownCount > 0
		group.RefusedLatencyMS.finish()
		group.CancelledLatencyMS.finish()
		resultGroups = append(resultGroups, *group)
	}
	sort.Slice(resultGroups, func(i, j int) bool { return groupSortKey(resultGroups[i]) < groupSortKey(resultGroups[j]) })
	filterOutput := sanitizeFilterOutput(q)
	filterOutput.Reports = nil
	coverage.Complete = !coverage.Partial && !coverage.EventsTruncated
	status := "ok"
	if !coverage.Complete {
		status = "partial"
	}
	return CohortResult{
		SchemaVersion: CohortSchemaVersion, Status: status, Filters: filterOutput, Groups: resultGroups,
		Coverage: coverage, Provenance: CohortProvenance{Source: "inline", BackendAccess: false, InputSchema: DiagnosticSchemaVersion, RedactedOnly: true, References: "identifiers_not_authorization", ModelSemantics: "filter_matches_report_root_or_any_event_model; group_model_is_union_root_and_events_or_multiple_models", MetricsSemantics: "all_metric_events_in_a_model-matched_run"},
		ResultTruncated: resultTruncated, TruncationReasons: func() []string {
			if resultTruncated {
				return []string{"references_truncated"}
			}
			return []string{}
		}(),
	}, nil
}

const cohortCostCoverageReasons = "payload_purged,payload_expired,persisted_logs_truncated,archive_unavailable,lifecycle_not_recorded,no_recorded_events,terminal_event_not_recorded,interrupted_segment_calls_unknown"

func normalizeReportCoverage(report DiagnosticReport, coverage *CohortCoverage) bool {
	incomplete := report.Coverage.ArchiveUnavailable || report.Coverage.PayloadState == "purged" || report.Coverage.PayloadState == "expired"
	if incomplete {
		coverage.Partial = true
		coverage.Reasons = appendUniqueReason(coverage.Reasons, "incomplete_payload_coverage")
	}
	for _, reason := range strings.Split(cohortCostCoverageReasons, ",") {
		if !hasReason(report.Coverage.Reasons, reason) {
			continue
		}
		coverage.Partial = true
		coverage.Reasons = appendUniqueReason(coverage.Reasons, reason)
		coverage.Reasons = appendUniqueReason(coverage.Reasons, "incomplete_payload_coverage")
		incomplete = true
	}
	return incomplete
}

func validStatusFilter(status string) bool {
	switch terminalStatus(status) {
	case "completed", "error", "timeout", "refused", "cancelled", "pending", "human_waiting", "unknown":
		return strings.TrimSpace(status) != "" && terminalStatus(status) != "unknown" || strings.EqualFold(strings.TrimSpace(status), "unknown")
	}
	return false
}

func terminalStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "success", "succeeded":
		return "completed"
	case "failed", "error":
		return "error"
	case "refused", "denied", "policy_denied", "admission_denied":
		return "refused"
	case "timeout", "timed_out":
		return "timeout"
	case "cancelled", "canceled":
		return "cancelled"
	case "human_waiting":
		return "human_waiting"
	case "running", "pending", "waiting", "waiting_for_child", "waiting_for_form", "waiting_for_approval", "queued":
		if strings.EqualFold(strings.TrimSpace(status), "waiting_for_form") || strings.EqualFold(strings.TrimSpace(status), "waiting_for_approval") {
			return "human_waiting"
		}
		return "pending"
	default:
		return "unknown"
	}
}

func normalizeCostStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "exact", "reported_exact", "producer_reported_exact":
		return "exact"
	case "estimated", "producer_estimate", "producer_estimated":
		return "estimated"
	case "unknown", "byok_unknown", "unavailable", "not_available":
		return "unknown"
	default:
		return "unclassified"
	}
}

func finalizeCost(cost *CohortCost) {
	cost.Unit = "USD"
	cost.Source = "inline_reported_costs_not_billing_authority"
	switch {
	case cost.KnownCount == 0:
		cost.Status = "unknown"
	case cost.UnclassifiedCount > 0 || (cost.ExactCount > 0 && cost.EstimatedCount > 0) || (cost.KnownCount > 0 && cost.UnknownCount > 0):
		cost.Status = "mixed"
	case cost.EstimatedCount > 0:
		cost.Status = "estimated"
	default:
		cost.Status = "exact"
	}
}

func validNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func durationKnown(report DiagnosticReport) bool {
	return report.DurationScope != "unknown" && (report.DurationKnown || report.DurationMS != 0)
}

func reportDurationScope(report DiagnosticReport) string {
	if strings.TrimSpace(report.DurationScope) != "" {
		return report.DurationScope
	}
	if durationKnown(report) {
		return "legacy_runtime_duration_ms"
	}
	return "unknown"
}

func addObservedLatency(latency *CohortQuantiles, unknown *int, coverage *CohortCoverage, report DiagnosticReport) {
	if durationKnown(report) && validNonNegative(report.DurationMS) {
		latency.add(report.DurationMS)
		return
	}
	*unknown = *unknown + 1
	coverage.Partial = true
	coverage.Reasons = appendUniqueReason(coverage.Reasons, "missing_duration")
}

func addStatusLatency(latency *CohortQuantiles, unknown *int, coverage *CohortCoverage, report DiagnosticReport) {
	if durationKnown(report) && validNonNegative(report.DurationMS) {
		latency.add(report.DurationMS)
		return
	}
	*unknown = *unknown + 1
	coverage.Partial = true
	coverage.Reasons = appendUniqueReason(coverage.Reasons, "missing_duration")
}

func compareGroups(baseline, candidate []CohortGroup, cohortPartial bool) []CohortGroupDelta {
	base := make(map[string]CohortGroup, len(baseline))
	for _, group := range baseline {
		base[groupSortKey(group)] = group
	}
	cand := make(map[string]CohortGroup, len(candidate))
	for _, group := range candidate {
		cand[groupSortKey(group)] = group
	}
	keys := make([]string, 0, len(base)+len(cand))
	seen := map[string]bool{}
	for key := range base {
		keys = append(keys, key)
		seen[key] = true
	}
	for key := range cand {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	deltas := make([]CohortGroupDelta, 0, len(keys))
	for _, key := range keys {
		before, hasBefore := base[key]
		after, hasAfter := cand[key]
		if !hasBefore {
			before = CohortGroup{Dimensions: after.Dimensions}
		}
		if !hasAfter {
			after = CohortGroup{Dimensions: before.Dimensions}
		}
		delta := CohortGroupDelta{Dimensions: after.Dimensions, ExecutionCountDelta: after.ExecutionCount - before.ExecutionCount}
		beforeRate, beforeOK := errorRate(before)
		afterRate, afterOK := errorRate(after)
		if beforeOK && afterOK && !cohortPartial {
			value := afterRate - beforeRate
			delta.ErrorRateDelta = &value
		}
		delta.LatencyDeltaUnknown = true
		if !cohortPartial && hasBefore && hasAfter && before.LatencyMS.Count > 0 && after.LatencyMS.Count > 0 && before.LatencyScope == after.LatencyScope && before.LatencyScope != "unknown" && before.LatencyScope != "mixed_incomparable" {
			p50, p95 := after.LatencyMS.P50-before.LatencyMS.P50, after.LatencyMS.P95-before.LatencyMS.P95
			delta.P50Delta, delta.P95Delta = &p50, &p95
			delta.LatencyDeltaUnknown = false
		}
		if cohortPartial || !hasBefore || !hasAfter || before.Cost.Incomplete || after.Cost.Incomplete {
			delta.CostDeltaUnknown = true
		} else {
			cost := after.Cost.KnownUSD - before.Cost.KnownUSD
			delta.CostDeltaUSD = &cost
		}
		deltas = append(deltas, delta)
	}
	return deltas
}

func errorRate(group CohortGroup) (float64, bool) {
	eligible := group.ExecutionCount - group.NonTerminalCount - group.CancelledCount - group.RefusedCount - group.UnknownStatusCount - group.HumanWaitingCount
	if eligible <= 0 {
		return 0, false
	}
	return float64(group.ErrorCount) / float64(eligible), true
}

// deduplicateReports collapses replayed snapshots by agent/execution identity.
// An absent agent is intentionally not replaced with another agent: only the
// empty-agent execution key can be deduplicated. Newest updated_at wins, then
// created_at; ties are deterministic and contradictory untimestamped snapshots
// are surfaced as partial rather than silently treated as complete.
func deduplicateReports(ctx context.Context, reports []DiagnosticReport) ([]DiagnosticReport, int, int, error) {
	type candidate struct {
		report DiagnosticReport
		index  int
	}
	chosen := map[string]candidate{}
	order := make([]string, 0, len(reports))
	fingerprints := make(map[int]string)
	agentsByExecution := make(map[string]map[string]struct{})
	for _, report := range reports {
		executionID, agentID := strings.TrimSpace(report.ExecutionID), strings.TrimSpace(report.AgentID)
		if executionID != "" && agentID != "" {
			if agentsByExecution[executionID] == nil {
				agentsByExecution[executionID] = map[string]struct{}{}
			}
			agentsByExecution[executionID][agentID] = struct{}{}
		}
	}
	fingerprint := func(index int, report DiagnosticReport) (string, error) {
		if value, ok := fingerprints[index]; ok {
			return value, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("cohort query timeout: %w", ctx.Err())
		default:
		}
		value := reportFingerprint(report)
		fingerprints[index] = value
		return value, nil
	}
	deduplicated, conflicts := 0, 0
	for index, report := range reports {
		select {
		case <-ctx.Done():
			return nil, 0, 0, fmt.Errorf("cohort query timeout: %w", ctx.Err())
		default:
		}
		if strings.TrimSpace(report.ExecutionID) == "" {
			key := fmt.Sprintf("__missing_execution__:%d", index)
			chosen[key] = candidate{report: report, index: index}
			order = append(order, key)
			continue
		}
		executionID, agentID := strings.TrimSpace(report.ExecutionID), strings.TrimSpace(report.AgentID)
		key := agentID + "\x00" + executionID
		if agentID == "" {
			if agents := agentsByExecution[executionID]; len(agents) == 1 {
				for knownAgent := range agents {
					key = knownAgent + "\x00" + executionID
				}
				conflicts++
			} else if len(agents) > 1 {
				conflicts++
				// There is no safe identity assignment: omit this snapshot rather
				// than counting it as a third execution.
				continue
			}
		}
		previous, exists := chosen[key]
		if !exists {
			chosen[key] = candidate{report: report, index: index}
			order = append(order, key)
			continue
		}
		deduplicated++
		previousFingerprint, err := fingerprint(previous.index, previous.report)
		if err != nil {
			return nil, 0, 0, err
		}
		currentFingerprint, err := fingerprint(index, report)
		if err != nil {
			return nil, 0, 0, err
		}
		if previousFingerprint == currentFingerprint {
			continue
		}
		previousTime, previousSource, previousOK := reportSnapshotTime(previous.report)
		currentTime, currentSource, currentOK := reportSnapshotTime(report)
		switch {
		case previousOK && currentOK && previousSource != currentSource:
			conflicts++
			if preferReportOnConflict(previous.report, report, previousFingerprint, currentFingerprint) {
				chosen[key] = candidate{report: report, index: index}
			}
		case previousOK && currentOK && currentTime.After(previousTime):
			chosen[key] = candidate{report: report, index: index}
		case previousOK && currentOK && currentTime.Equal(previousTime):
			conflicts++
			if preferReportOnConflict(previous.report, report, previousFingerprint, currentFingerprint) {
				chosen[key] = candidate{report: report, index: index}
			}
		case !previousOK || !currentOK:
			conflicts++
			if preferReportOnConflict(previous.report, report, previousFingerprint, currentFingerprint) {
				chosen[key] = candidate{report: report, index: index}
			}
		}
	}
	result := make([]DiagnosticReport, 0, len(order))
	for _, key := range order {
		result = append(result, chosen[key].report)
	}
	return result, deduplicated, conflicts, nil
}

func preferReportOnConflict(previous, current DiagnosticReport, previousFingerprint, currentFingerprint string) bool {
	previousError := terminalStatus(previous.Status) == "error" || terminalStatus(previous.Status) == "timeout"
	currentError := terminalStatus(current.Status) == "error" || terminalStatus(current.Status) == "timeout"
	if currentError != previousError {
		return currentError
	}
	return currentFingerprint > previousFingerprint
}

func crossCohortDuplicateCount(candidate, baseline []DiagnosticReport) int {
	seen := map[string]map[string]struct{}{}
	for _, report := range candidate {
		if strings.TrimSpace(report.ExecutionID) != "" {
			execution := strings.TrimSpace(report.ExecutionID)
			if seen[execution] == nil {
				seen[execution] = map[string]struct{}{}
			}
			seen[execution][strings.TrimSpace(report.AgentID)] = struct{}{}
		}
	}
	count := 0
	for _, report := range baseline {
		execution := strings.TrimSpace(report.ExecutionID)
		if agents, ok := seen[execution]; ok {
			agent := strings.TrimSpace(report.AgentID)
			if agent == "" {
				count++
				continue
			}
			if _, exact := agents[agent]; exact {
				count++
				continue
			}
			if _, missing := agents[""]; missing {
				count++
			}
		}
	}
	return count
}

func reportSnapshotTime(report DiagnosticReport) (time.Time, string, bool) {
	for _, candidate := range []struct{ value, source string }{{report.UpdatedAt, "updated_at"}, {report.CreatedAt, "created_at"}} {
		raw := candidate.value
		if strings.TrimSpace(raw) == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err == nil {
			return parsed, candidate.source, true
		}
	}
	return time.Time{}, "", false
}

func reportFingerprint(report DiagnosticReport) string {
	return fmt.Sprintf("%s|duration_known=%t", reportFingerprintJSON(report), report.DurationKnown)
}

func reportFingerprintJSON(report DiagnosticReport) string {
	encoded, _ := json.Marshal(report)
	return string(encoded)
}

func isLLMEvent(event DiagnosticEvent) bool {
	switch strings.ToLower(strings.TrimSpace(event.ActionType)) {
	case "llm_call", "llm_call_custom", "decision_llm", "llm", "model_call":
		return true
	default:
		return false
	}
}

func positiveTokenCount(value *int64) int64 {
	if value == nil || *value < 0 {
		return 0
	}
	return *value
}

func (q *CohortQuantiles) add(value float64) { q.values = append(q.values, value) }
func (q *CohortQuantiles) finish() {
	sort.Float64s(q.values)
	q.Count = len(q.values)
	if q.Count > 0 {
		q.P50, q.P95 = nearestRank(q.values, .50), nearestRank(q.values, .95)
	}
	q.values = nil
}
func nearestRank(values []float64, percentile float64) float64 {
	index := int((percentile*float64(len(values)))+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func parseWindow(window CohortWindow) (time.Time, time.Time, error) {
	var start, end time.Time
	var err error
	if window.Start != "" {
		start, err = time.Parse(time.RFC3339Nano, window.Start)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid window.start")
		}
	}
	if window.End != "" {
		end, err = time.Parse(time.RFC3339Nano, window.End)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid window.end")
		}
	}
	if !start.IsZero() && !end.IsZero() && end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("window.end precedes window.start")
	}
	return start, end, nil
}
func matchesReport(report DiagnosticReport, q CohortQuery, start, end time.Time) bool {
	if !matchesNonWindowFilters(report, q) {
		return false
	}
	if !start.IsZero() || !end.IsZero() {
		if report.CreatedAt == "" {
			return false
		}
		at, err := time.Parse(time.RFC3339Nano, report.CreatedAt)
		if err != nil || !start.IsZero() && at.Before(start) || !end.IsZero() && at.After(end) {
			return false
		}
	}
	return true
}

func matchesNonWindowFilters(report DiagnosticReport, q CohortQuery) bool {
	return matchesNonStatusFilters(report, q) &&
		(q.Status == "" || statusMatchesFilter(effectiveReportStatus(report), q.Status))
}

func matchesNonStatusFilters(report DiagnosticReport, q CohortQuery) bool {
	return (q.WorkflowID == "" || report.WorkflowID == q.WorkflowID) &&
		(q.WorkflowVersion == "" || report.WorkflowVersion == q.WorkflowVersion) &&
		(q.Model == "" || modelMatches(report, q.Model))
}

func effectiveReportStatus(report DiagnosticReport) string {
	if hasReason(report.Coverage.Reasons, "terminal_event_not_recorded") {
		return "unknown"
	}
	return terminalStatus(report.Status)
}

func modelMatches(report DiagnosticReport, model string) bool {
	if strings.TrimSpace(report.Model) == strings.TrimSpace(model) {
		return true
	}
	for _, event := range report.Events {
		if strings.TrimSpace(event.Model) == strings.TrimSpace(model) {
			return true
		}
	}
	return false
}

func effectiveReportModel(report DiagnosticReport) string {
	models := map[string]struct{}{}
	if value := strings.TrimSpace(report.Model); value != "" {
		models[value] = struct{}{}
	}
	for _, event := range report.Events {
		if value := strings.TrimSpace(event.Model); value != "" {
			models[value] = struct{}{}
		}
	}
	if len(models) == 1 {
		for model := range models {
			return model
		}
	}
	if len(models) > 1 {
		return "multiple_models"
	}
	return ""
}

func statusMatchesFilter(reportStatus, filterStatus string) bool {
	reportTerminal, filterTerminal := terminalStatus(reportStatus), terminalStatus(filterStatus)
	// Timeout is a distinct terminal outcome, but it is also an execution
	// error for the error filter and error-rate denominator.
	return reportTerminal == filterTerminal || (filterTerminal == "error" && reportTerminal == "timeout")
}

func windowExclusionReason(report DiagnosticReport, start, end time.Time) string {
	if start.IsZero() && end.IsZero() {
		return ""
	}
	if strings.TrimSpace(report.CreatedAt) == "" {
		return "missing_created_at"
	}
	if _, err := time.Parse(time.RFC3339Nano, report.CreatedAt); err != nil {
		return "invalid_created_at"
	}
	return ""
}

func reportWithinWindow(report DiagnosticReport, start, end time.Time) bool {
	if start.IsZero() && end.IsZero() {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, report.CreatedAt)
	if err != nil {
		return false
	}
	return (start.IsZero() || !at.Before(start)) && (end.IsZero() || !at.After(end))
}

func appendUniqueReason(reasons []string, reason string) []string {
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

const maxGroupReferences = 20

func appendReference(references *[]string, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, existing := range *references {
		if existing == value {
			return false
		}
	}
	if len(*references) >= maxGroupReferences {
		return true
	}
	*references = append(*references, value)
	return false
}

// XIDs use base32hex with a canonical final padding nibble. Syntactic
// identity never skips the credential or phone checks below.
func isCanonicalExecutionReference(value string) bool {
	if len(value) != 20 || !strings.ContainsRune("048cgkos", rune(value[19])) {
		return false
	}
	hasLetter := false
	for _, c := range value {
		if c >= 'a' && c <= 'v' {
			hasLetter = true
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'v') {
			return false
		}
	}
	return hasLetter
}

func sanitizeReference(value, kind string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if kind == "trace" && len(value) == 32 {
		for _, r := range value {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return ""
			}
		}
		return strings.ToLower(value)
	}
	if kind == "execution" && len(value) == 20 {
		if isCanonicalExecutionReference(value) && !hasCredentialMarker(value) && !phoneLike(value) {
			return value
		}
	}
	if hasCredentialMarker(value) {
		return ""
	}
	if unsafeMetadataValue(value) {
		return pseudonymize(value)
	}

	return pseudonymize(value)
}
func hasReason(reasons []string, wanted string) bool {
	for _, reason := range reasons {
		if reason == wanted {
			return true
		}
	}
	return false
}
func groupKey(report DiagnosticReport, by []string) (string, map[string]string) {
	dimensions := map[string]string{}
	for _, dimension := range by {
		value := ""
		switch dimension {
		case "workflow_id":
			value = safeFilterValueForDimension(report.WorkflowID, "workflow_id")
		case "workflow_version":
			value = safeFilterValueForDimension(report.WorkflowVersion, "workflow_version")
		case "model":
			value = safeFilterValueForDimension(effectiveReportModel(report), "model")
		case "status":
			value = effectiveReportStatus(report)
		}
		dimensions[dimension] = value
	}
	b, _ := json.Marshal(dimensions)
	return string(b), dimensions
}

func checkRedactedDimensionCollision(origins map[string]map[string]string, report DiagnosticReport, dimensions []string) error {
	for _, dimension := range dimensions {
		raw := strings.TrimSpace(rawDimensionValue(report, dimension))
		projected := safeFilterValueForDimension(raw, dimension)
		if !strings.HasPrefix(projected, "ref_") || raw == "" {
			continue
		}
		if origins[dimension] == nil {
			origins[dimension] = map[string]string{}
		}
		if prior, exists := origins[dimension][projected]; exists && prior != raw {
			return fmt.Errorf("redacted_group_dimension_conflict")
		}
		origins[dimension][projected] = raw
	}
	return nil
}

func rawDimensionValue(report DiagnosticReport, dimension string) string {
	switch dimension {
	case "workflow_id":
		return report.WorkflowID
	case "workflow_version":
		return report.WorkflowVersion
	case "model":
		return effectiveReportModel(report)
	default:
		return ""
	}
}

func sanitizeFilterOutput(query CohortQuery) CohortQuery {
	query.Reports = nil
	query.BaselineReports = nil
	query.WorkflowID = safeFilterValue(query.WorkflowID)
	query.WorkflowVersion = safeFilterValueForDimension(query.WorkflowVersion, "workflow_version")
	query.Model = safeFilterValueForDimension(query.Model, "model")
	if query.Status != "" {
		query.Status = terminalStatus(query.Status)
	}
	return query
}

func safeFilterValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	// Free-form filter values are metadata, not a public identifier contract.
	// Keep matching on the raw query before this projection, and expose only a
	// process-local HMAC so arbitrary names/addresses/PII cannot escape.
	return pseudonymize(value)
}

func safeFilterValueForDimension(value, dimension string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if dimension == "workflow_version" && validWorkflowVersion(value) {
		return value
	}
	if dimension == "model" && value == "multiple_models" {
		return value
	}
	return safeFilterValue(value)
}

func validWorkflowVersion(value string) bool {
	if len(value) > 64 {
		return false
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02-1504"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for i, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
		// Long numeric suffixes are admitted only as a validated calendar date.
		if i == len(parts)-1 && len(part) == 8 {
			if _, err := time.Parse("20060102", part); err != nil {
				return false
			}
			continue
		}
		max := 3
		if len(parts) == 4 {
			max = 2
		}
		if len(part) > max {
			return false
		}
	}
	return true
}

func validDatedModel(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) < 4 {
		return false
	}
	knownPrefix := map[string]bool{"claude": true, "deepseek": true, "gemini": true, "gpt": true, "llama": true, "mistral": true, "qwen": true}
	if !knownPrefix[strings.ToLower(parts[0])] {
		return false
	}
	date := parts[len(parts)-1]
	if len(date) != 8 {
		return false
	}
	for _, r := range date {
		if r < '0' || r > '9' {
			return false
		}
	}
	if _, err := time.Parse("20060102", date); err != nil {
		return false
	}
	hasNumericSegment := false
	for _, part := range parts[1 : len(parts)-1] {
		if part == "" {
			return false
		}
		allNumeric := true
		for _, r := range part {
			if r < '0' || r > '9' {
				allNumeric = false
				if r < 'a' || r > 'z' {
					return false
				}
			}
		}
		if allNumeric {
			hasNumericSegment = true
		}
	}
	return hasNumericSegment
}
func groupSortKey(group CohortGroup) string { b, _ := json.Marshal(group.Dimensions); return string(b) }

func diagnosticMetricEvent(event DiagnosticEvent) bool {
	actionType := strings.ToLower(strings.TrimSpace(event.ActionType))
	name := strings.ToLower(strings.TrimSpace(event.Name))
	source := strings.ToLower(strings.TrimSpace(event.Source))
	if source == "diagnostic_step" || source == "diagnostic_lifecycle" || source == "diagnostic_child_copy" || actionType == "diagnostic_step" || actionType == "lifecycle" {
		return false
	}
	return !(actionType == "" && (name == "diagnostic_step" || name == "lifecycle"))
}

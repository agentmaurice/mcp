package observe

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Span struct {
	Name         string  `json:"name"`
	SpanID       string  `json:"span_id,omitempty"`
	ParentSpanID string  `json:"parent_span_id,omitempty"`
	StartNS      int64   `json:"start_unix_nano,omitempty"`
	EndNS        int64   `json:"end_unix_nano,omitempty"`
	DurationMS   float64 `json:"duration_ms"`
	Status       string  `json:"status"`
	RetryCount   int     `json:"retry_count,omitempty"`
}

type Summary struct {
	SpanCount      int      `json:"span_count"`
	ErrorCount     int      `json:"error_count"`
	RetryCount     int      `json:"retry_count"`
	DurationMS     float64  `json:"duration_ms"`
	SlowestSpan    string   `json:"slowest_span,omitempty"`
	CriticalPath   []string `json:"critical_path"`
	CriticalPathMS float64  `json:"critical_path_ms"`
}

func Parse(content string) ([]Span, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var root interface{}
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("invalid JSON trace: %w", err)
	}
	spans := make([]Span, 0)
	walk(root, &spans)
	if len(spans) == 0 {
		return nil, fmt.Errorf("no spans found")
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].StartNS != spans[j].StartNS {
			return spans[i].StartNS < spans[j].StartNS
		}
		return spans[i].Name < spans[j].Name
	})
	return spans, nil
}

func Summarize(spans []Span) Summary {
	summary := Summary{SpanCount: len(spans), CriticalPath: []string{}}
	if len(spans) == 0 {
		return summary
	}
	minStart, maxEnd := spans[0].StartNS, spans[0].EndNS
	for _, span := range spans {
		if strings.EqualFold(span.Status, "error") {
			summary.ErrorCount++
		}
		summary.RetryCount += span.RetryCount
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
	}
	if minStart > 0 && maxEnd >= minStart {
		summary.DurationMS = float64(maxEnd-minStart) / 1e6
	}
	summary.CriticalPath, summary.CriticalPathMS = criticalPath(spans)
	return summary
}

func Explain(spans []Span) map[string]interface{} {
	summary := Summarize(spans)
	errors := make([]string, 0)
	for _, span := range spans {
		if strings.EqualFold(span.Status, "error") {
			errors = append(errors, span.Name)
		}
	}
	return map[string]interface{}{
		"summary":   summary,
		"errors":    errors,
		"diagnosis": diagnosis(summary),
	}
}

func Compare(left, right []Span) map[string]interface{} {
	a, b := Summarize(left), Summarize(right)
	return map[string]interface{}{
		"baseline":          a,
		"candidate":         b,
		"duration_delta_ms": b.DurationMS - a.DurationMS,
		"error_delta":       b.ErrorCount - a.ErrorCount,
		"retry_delta":       b.RetryCount - a.RetryCount,
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
	return Span{Name: name, SpanID: firstString(raw, "spanId", "span_id"), ParentSpanID: firstString(raw, "parentSpanId", "parent_span_id"), StartNS: start, EndNS: end, DurationMS: duration, Status: status, RetryCount: int(int64Value(raw, "retry_count"))}, true
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
	switch value := values[key].(type) {
	case json.Number:
		result, _ := value.Float64()
		return result
	case float64:
		return value
	}
	return 0
}

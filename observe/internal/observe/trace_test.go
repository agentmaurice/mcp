package observe

import "testing"

func TestParseSummarizeAndExplain(t *testing.T) {
	spans, err := Parse(`{"spans":[{"name":"run","span_id":"a","start_unix_nano":"1000000","end_unix_nano":"11000000","status":"ok"},{"name":"tool","span_id":"b","parent_span_id":"a","duration_ms":4,"status":"error","retry_count":1}]}`)
	if err != nil {
		t.Fatal(err)
	}
	summary := Summarize(spans)
	if summary.SpanCount != 2 || summary.ErrorCount != 1 || summary.RetryCount != 1 || summary.DurationMS != 10 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(summary.CriticalPath) != 2 || summary.CriticalPath[1] != "tool" {
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

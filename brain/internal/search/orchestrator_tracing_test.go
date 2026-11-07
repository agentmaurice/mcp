//go:build duckdb

package search

import (
	"context"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

// A keyword search runs in a brain.search span under the caller's span, with
// the full-text index check and every DuckDB statement below it. No span
// carries the query text, the tenant or a chunk.
func TestKeywordSearchSpans(t *testing.T) {
	env := newSearchEnv(t)
	const tenant = "tenant-traced"
	env.upsert(t, tenant, "note.md", "# Procédure\nLa révision annuelle du réseau se fait en mars.")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	const query = "révision annuelle"
	ctx, caller := provider.Tracer("test").Start(context.Background(), "caller")
	resp, err := o.Search(ctx, tenant, query, shared.SearchOpts{Mode: "bm25", MaxResults: 5})
	caller.End()
	if err != nil || resp.TotalResults == 0 {
		t.Fatalf("search: %v (results=%v)", err, resp)
	}

	var search sdktrace.ReadOnlySpan
	counts := map[string]int{}
	for _, span := range recorder.Ended() {
		counts[span.Name()]++
		if span.Name() == "brain.search" {
			search = span
		}
		if span.SpanContext().TraceID() != caller.SpanContext().TraceID() {
			t.Fatalf("span %s left the caller's trace", span.Name())
		}
		for _, kv := range span.Attributes() {
			switch kv.Value.Emit() {
			case query, tenant:
				t.Fatalf("span %s attribute %s leaks the query or the tenant", span.Name(), kv.Key)
			}
		}
	}
	if search == nil || search.Parent().SpanID() != caller.SpanContext().SpanID() {
		t.Fatalf("brain.search missing or not under the caller: %v", counts)
	}
	if counts["brain.fts.ensure"] != 1 || counts["db.query"] == 0 {
		t.Fatalf("want one brain.fts.ensure and db.query spans, got %v", counts)
	}
	for _, kv := range search.Attributes() {
		if kv.Key == "brain.search.mode" && kv.Value.AsString() != "bm25" {
			t.Fatalf("brain.search.mode = %q", kv.Value.AsString())
		}
	}
}

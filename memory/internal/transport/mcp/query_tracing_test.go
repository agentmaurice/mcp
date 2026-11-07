//go:build duckdb

package mcp

import (
	"context"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

// A memory.query call carrying the caller's traceparent in params._meta
// produces its spans inside the caller's trace: the tool span is the remote
// child, validation, tenant opening and every DuckDB statement hang below it.
// No span carries the SQL text, the tenant id or a row value.
func TestMemoryQueryJoinsCallerTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	cfg, manager := newMemoryQueryEnv(t)
	const tenant = "tenant-traced"
	seedEntities(t, manager, tenant, "alice", 2)

	traced := storage.WithTracing(manager)
	server := NewServer(traced, cfg, NewResponseWrapper(nil, zap.NewNop()), zap.NewNop())
	handler := server.wrapToolHandler("memory.query", NewQueryTool(traced, cfg, nil, zap.NewNop()).Handler())

	const callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	request := mcp.CallToolRequest{}
	request.Params.Name = "memory.query"
	request.Params.Arguments = map[string]any{"tenant_id": tenant, "sql": "SELECT name FROM entities"}
	request.Params.Meta = &mcp.Meta{AdditionalFields: map[string]any{
		"traceparent": "00-" + callerTrace + "-00f067aa0ba902b7-01",
	}}
	result, err := handler(context.Background(), request)
	if err != nil || result == nil || result.IsError {
		t.Fatalf("memory.query failed: %v %+v", err, result)
	}

	spans := recorder.Ended()
	byName := map[string][]sdktrace.ReadOnlySpan{}
	var tool sdktrace.ReadOnlySpan
	for _, span := range spans {
		byName[span.Name()] = append(byName[span.Name()], span)
		if span.Name() == "mcp.server.tool" {
			tool = span
		}
		if got := span.SpanContext().TraceID().String(); got != callerTrace {
			t.Fatalf("span %s is in trace %s, want the caller's", span.Name(), got)
		}
		for _, kv := range span.Attributes() {
			switch kv.Value.Emit() {
			case "SELECT name FROM entities", tenant, "alice-1", "alice-2":
				t.Fatalf("span %s attribute %s leaks query content", span.Name(), kv.Key)
			}
		}
	}
	if tool == nil {
		t.Fatalf("no mcp.server.tool span among %d spans", len(spans))
	}
	for _, name := range []string{"memory.query.validate", "memory.tenant.open"} {
		if len(byName[name]) != 1 {
			t.Fatalf("want one %s span, got %d", name, len(byName[name]))
		}
		if byName[name][0].Parent().SpanID() != tool.SpanContext().SpanID() {
			t.Fatalf("%s is not a child of the tool span", name)
		}
	}
	// The query itself, the pii_policy lookup and the tenant emptiness check.
	if len(byName["db.query"]) < 3 {
		t.Fatalf("want at least 3 db.query spans, got %d", len(byName["db.query"]))
	}
	for _, span := range byName["db.query"] {
		if span.Parent().SpanID() != tool.SpanContext().SpanID() {
			t.Fatal("db.query is not a child of the tool span")
		}
	}
}

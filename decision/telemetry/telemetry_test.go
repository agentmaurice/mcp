package telemetry

import (
	"context"
	"net/http"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestMetaPropagatorPreservesTraceContext(t *testing.T) {
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    [16]byte{1, 2, 3},
		SpanID:     [8]byte{4, 5, 6},
		TraceFlags: trace.FlagsSampled,
		Remote:     false,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	meta := (MetaPropagator{}).InjectMeta(ctx, &mcp.Meta{AdditionalFields: map[string]any{"request_id": "bounded"}})
	if meta.AdditionalFields["traceparent"] == "" {
		t.Fatal("traceparent was not injected into MCP _meta")
	}
	got := trace.SpanContextFromContext((MetaPropagator{}).ExtractMeta(context.Background(), meta))
	if got.TraceID() != sc.TraceID() || got.SpanID() != sc.SpanID() || got.TraceFlags() != sc.TraceFlags() {
		t.Fatalf("trace context mismatch: got %s/%s/%s, want %s/%s/%s", got.TraceID(), got.SpanID(), got.TraceFlags(), sc.TraceID(), sc.SpanID(), sc.TraceFlags())
	}
}

type captureTransport struct{ header http.Header }

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.header = req.Header.Clone()
	return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
}

func TestHTTPTransportInjectsTraceContext(t *testing.T) {
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)

	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: [16]byte{7, 8, 9}, SpanID: [8]byte{10, 11, 12}, TraceFlags: trace.FlagsSampled})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	base := &captureTransport{}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.invalid/v1/systemone", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := HTTPTransport(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	got := propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(base.header))
	gotSC := trace.SpanContextFromContext(got)
	if gotSC.TraceID() != sc.TraceID() || !gotSC.IsValid() || gotSC.SpanID() == sc.SpanID() {
		t.Fatalf("provider header context mismatch: got %s/%s, want trace %s and a child span", gotSC.TraceID(), gotSC.SpanID(), sc.TraceID())
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Parent().SpanID() != sc.SpanID() || spans[0].SpanContext().TraceID() != sc.TraceID() {
		t.Fatalf("provider span parent mismatch: got %v, want parent %s", spans, sc.SpanID())
	}
}

func TestHTTPTransportDoesNotForwardBaggage(t *testing.T) {
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	base := &captureTransport{}
	member, err := baggage.NewMember("user.email", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}
	ctx := baggage.ContextWithBaggage(context.Background(), b)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.invalid/v1/systemone", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := HTTPTransport(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := base.header.Get("baggage"); got != "" {
		t.Fatalf("provider received baggage %q", got)
	}
}

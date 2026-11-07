package telemetry

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const tracerName = "agentmaurice.mcp.decision"

// Init installs the W3C propagator used by MCP _meta and provider HTTP calls.
// It returns a shutdown function so process entrypoints can restore the prior
// global propagator in tests and during orderly shutdown.
func Init(ctx context.Context) (func(context.Context) error, error) {
	previousProvider := otel.GetTracerProvider()
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") || strings.EqualFold(os.Getenv("OTEL_EXPORTER"), "none") {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error {
			otel.SetTracerProvider(previousProvider)
			otel.SetTextMapPropagator(previous)
			return nil
		}, nil
	}
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", "mcp-decision")), resource.WithFromEnv())
	if err != nil {
		return nil, errors.New("telemetry resource initialization failed")
	}
	options := []trace.TracerProviderOption{trace.WithResource(res), trace.WithSampler(trace.ParentBased(trace.AlwaysSample()))}
	var provider *trace.TracerProvider
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	}
	if endpoint != "" {
		protocol := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")
		if protocol == "" {
			protocol = os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
		}
		if protocol != "" && protocol != "http/protobuf" && protocol != "http" {
			otel.SetTextMapPropagator(previous)
			return nil, errors.New("Decision OTLP exporter requires http/protobuf")
		}
		// The SDK resolves the general base endpoint versus the exact traces
		// endpoint, headers and TLS using its standard environment contract.
		exporter, err := otlptrace.New(ctx, otlptracehttp.NewClient())
		if err != nil {
			otel.SetTextMapPropagator(previous)
			return nil, errors.New("OTLP trace exporter initialization failed")
		}
		options = append(options, trace.WithBatcher(exporter))
	}
	provider = trace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	return func(shutdownCtx context.Context) error {
		err := provider.Shutdown(shutdownCtx)
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previous)
		return err
	}, nil
}

// MetaPropagator carries W3C context through MCP _meta, including STDIO.
type MetaPropagator struct{}

func (MetaPropagator) InjectMeta(ctx context.Context, meta *mcp.Meta) *mcp.Meta {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return meta
	}
	out := &mcp.Meta{AdditionalFields: make(map[string]any, len(carrier))}
	if meta != nil {
		out.ProgressToken = meta.ProgressToken
		for key, value := range meta.AdditionalFields {
			out.AdditionalFields[key] = value
		}
	}
	for key, value := range carrier {
		out.AdditionalFields[key] = value
	}
	return out
}

func (MetaPropagator) ExtractMeta(ctx context.Context, meta *mcp.Meta) context.Context {
	if meta == nil {
		return ctx
	}
	carrier := propagation.MapCarrier{}
	for key, value := range meta.AdditionalFields {
		if text, ok := value.(string); ok {
			carrier[key] = text
		}
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

type tracer struct{}

func (tracer) Start(ctx context.Context, name string, kind tracing.SpanKind, attrs ...tracing.Attribute) (context.Context, tracing.Span) {
	spanKind := oteltrace.SpanKindInternal
	switch kind {
	case tracing.SpanKindServer:
		spanKind = oteltrace.SpanKindServer
	case tracing.SpanKindClient:
		spanKind = oteltrace.SpanKindClient
	}
	converted := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		converted = append(converted, attribute.String(attr.Key, attr.Value))
	}
	child, otelSpan := otel.Tracer(tracerName).Start(ctx, name, oteltrace.WithSpanKind(spanKind), oteltrace.WithAttributes(converted...))
	return child, span{Span: otelSpan}
}

type span struct{ oteltrace.Span }

func (s span) SetAttributes(attrs ...tracing.Attribute) {
	converted := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		converted = append(converted, attribute.String(attr.Key, attr.Value))
	}
	s.Span.SetAttributes(converted...)
}

func (s span) RecordError(error) { s.Span.RecordError(errors.New("mcp operation failed")) }
func (s span) SetStatus(code tracing.StatusCode, description string) {
	if code == tracing.StatusError {
		// Keep provider/server error text out of span attributes and status.
		s.Span.SetStatus(codes.Error, "operation failed")
	}
}
func (s span) End() { s.Span.End() }

// TracerAdapter returns the mcp-go tracing adapter backed by OTel.
func TracerAdapter() tracing.Tracer { return tracer{} }

// HTTPTransport wraps provider calls with OTel client spans and W3C headers.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripper{base: base}
}

type roundTripper struct{ base http.RoundTripper }

func (t roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, span := otel.Tracer(tracerName).Start(req.Context(), "provider.http.request", oteltrace.WithSpanKind(oteltrace.SpanKindClient), oteltrace.WithAttributes(attribute.String("server.address", req.URL.Host)))
	defer span.End()
	out := req.Clone(ctx)
	// Baggage is accepted at the MCP boundary for local correlation, but it
	// must never cross the hosted-provider privacy boundary. Only W3C trace
	// context is allowed on provider requests.
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(out.Header))
	resp, err := t.base.RoundTrip(out)
	if err != nil {
		span.RecordError(errors.New("provider request failed"))
		span.SetStatus(codes.Error, "provider request failed")
	}
	if resp != nil {
		span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
		if resp.StatusCode >= 500 {
			span.SetStatus(codes.Error, "provider response failed")
		}
	}
	return resp, err
}

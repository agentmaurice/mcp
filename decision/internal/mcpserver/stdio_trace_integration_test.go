package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/decision/pkg/systemone"
	"github.com/agentmaurice/mcpchatui/mcp/decision/telemetry"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const stdioHelperEnv = "AGENT486_STDIO_HELPER"

func newSTDIOTraceServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}

// TestSTDIOClientTraceparentReachesServerAndProvider exercises the complete
// SDK-client -> real stdio subprocess -> Decision server -> HTTP provider path.
func TestSTDIOClientTraceparentReachesServerAndProvider(t *testing.T) {
	if os.Getenv(stdioHelperEnv) == "1" {
		if err := runSTDIOTraceHelper(); err != nil {
			t.Fatal(err)
		}
		return
	}

	var providerMu sync.Mutex
	var providerTraceparent, providerTracestate, providerBaggage string
	provider := newSTDIOTraceServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerMu.Lock()
		providerTraceparent = r.Header.Get("traceparent")
		providerTracestate = r.Header.Get("tracestate")
		providerBaggage = r.Header.Get("baggage")
		providerMu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "jev-latest",
			"answers": map[string]any{"answer": map[string]any{"type": "noul", "noul": 0.9}},
			"usage":   map[string]int{"input_tokens": 0, "output_tokens": 0},
		})
	}))
	defer provider.Close()

	var exportedMu sync.Mutex
	var exported []*collector.ExportTraceServiceRequest
	collectorServer := newSTDIOTraceServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		req := new(collector.ExportTraceServiceRequest)
		if err := proto.Unmarshal(body, req); err != nil {
			http.Error(w, "invalid OTLP", http.StatusBadRequest)
			return
		}
		exportedMu.Lock()
		exported = append(exported, req)
		exportedMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer collectorServer.Close()

	state, err := trace.ParseTraceState("vendor=qualification")
	if err != nil {
		t.Fatal(err)
	}
	root := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    [16]byte{31, 32, 33, 34},
		SpanID:     [8]byte{35, 36, 37, 38},
		TraceFlags: trace.FlagsSampled,
		TraceState: state,
		Remote:     true,
	})
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	defer otel.SetTextMapPropagator(previousPropagator)
	previousProvider := otel.GetTracerProvider()
	clientRecorder := tracetest.NewSpanRecorder()
	clientProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(clientRecorder))
	otel.SetTracerProvider(clientProvider)
	defer func() {
		otel.SetTracerProvider(previousProvider)
		_ = clientProvider.Shutdown(context.Background())
	}()

	childEnv := make([]string, 0, len(os.Environ())+6)
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "OTEL_") {
			childEnv = append(childEnv, variable)
		}
	}
	childEnv = append(childEnv,
		stdioHelperEnv+"=1",
		"MCP_DECISION_TYPESAFE_API_KEY=stdio-fixture-key",
		"MCP_DECISION_TYPESAFE_URL="+provider.URL,
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="+collectorServer.URL+"/v1/traces",
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf",
		"OTEL_SERVICE_NAME=decision-stdio-fixture",
	)
	stdio := transport.NewStdio(os.Args[0], childEnv, "-test.run=TestSTDIOClientTraceparentReachesServerAndProvider")
	session := client.NewClient(stdio,
		client.WithMetaPropagator(telemetry.MetaPropagator{}),
		client.WithTracer(telemetry.TracerAdapter()),
	)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			if err := session.Close(); err != nil && !strings.Contains(err.Error(), "already closed") {
				t.Logf("stdio close: %v", err)
			}
		}
	}()

	baggageMember, err := baggage.NewMember("qualification.secret", "baggage-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	baggageValue, err := baggage.New(baggageMember)
	if err != nil {
		t.Fatal(err)
	}
	rootContext := baggage.ContextWithBaggage(trace.ContextWithSpanContext(context.Background(), root), baggageValue)
	ctx, cancel := context.WithTimeout(rootContext, 10*time.Second)
	defer cancel()
	if _, err := session.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		Capabilities:    mcp.ClientCapabilities{},
		ClientInfo:      mcp.Implementation{Name: "stdio-trace-test", Version: "1"},
	}}); err != nil {
		t.Fatal(err)
	}
	callCtx, clientContextSpan := otel.Tracer("stdio-trace-client").Start(ctx, "mcp.client.request", trace.WithSpanKind(trace.SpanKindClient))
	result, err := session.CallTool(callCtx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "decision_noul_v1",
		Meta:      &mcp.Meta{AdditionalFields: map[string]any{"request_id": "stdio-trace-test"}},
		Arguments: map[string]any{"state": "synthetic", "instructions": "Is this synthetic?"},
	}})
	clientContextSpan.End()
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("stdio tool returned error: %+v", result)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true

	providerMu.Lock()
	gotProviderTraceparent, gotProviderTracestate, gotProviderBaggage := providerTraceparent, providerTracestate, providerBaggage
	providerMu.Unlock()
	if !strings.HasPrefix(gotProviderTraceparent, "00-") {
		t.Fatalf("provider did not receive W3C traceparent: %q", gotProviderTraceparent)
	}
	providerContext := propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(http.Header{
		"Traceparent": []string{gotProviderTraceparent},
		"Tracestate":  []string{gotProviderTracestate},
	}))
	providerSC := trace.SpanContextFromContext(providerContext)
	if !providerSC.IsValid() || providerSC.TraceID() != root.TraceID() || providerSC.SpanID() == root.SpanID() {
		t.Fatalf("provider trace context mismatch: %q %q", gotProviderTraceparent, gotProviderTracestate)
	}
	if gotProviderTracestate != "vendor=qualification" {
		t.Fatalf("provider lost tracestate: %q", gotProviderTracestate)
	}
	if gotProviderBaggage != "" {
		t.Fatalf("provider received baggage: %q", gotProviderBaggage)
	}

	var spans []*tracev1.Span
	exportedMu.Lock()
	exportedRequests := append([]*collector.ExportTraceServiceRequest(nil), exported...)
	exportedMu.Unlock()
	for _, request := range exportedRequests {
		for _, resourceSpans := range request.ResourceSpans {
			spans = append(spans, stdioTraceSpansFromResource(resourceSpans)...)
		}
	}
	if len(spans) == 0 {
		t.Fatal("no OTLP spans exported by the stdio subprocess")
	}
	var providerSpan *tracev1.Span
	for _, span := range spans {
		if span.Name == "provider.http.request" {
			providerSpan = span
			break
		}
	}
	if providerSpan == nil {
		t.Fatal("exported spans did not include provider.http.request")
	}
	rootTraceID := root.TraceID()
	if got := providerSpan.TraceId; !bytes.Equal(got, rootTraceID[:]) {
		t.Fatalf("provider span trace id mismatch: %x", got)
	}
	if len(providerSpan.ParentSpanId) != 8 || bytes.Equal(providerSpan.ParentSpanId, make([]byte, 8)) {
		t.Fatal("provider span has no parent server span")
	}
	var serverSpan *tracev1.Span
	for _, span := range spans {
		if bytes.Equal(span.SpanId, providerSpan.ParentSpanId) {
			serverSpan = span
			break
		}
	}
	if serverSpan == nil {
		t.Fatalf("provider span parent %x was not exported by the stdio server", providerSpan.ParentSpanId)
	}
	providerSpanID := providerSC.SpanID()
	if providerSpanID.IsValid() && !bytes.Equal(providerSpanID[:], providerSpan.SpanId) {
		t.Fatalf("provider header span id %x does not match exported span %x", providerSpanID, providerSpan.SpanId)
	}
	var clientSpan sdktrace.ReadOnlySpan
	for _, span := range clientRecorder.Ended() {
		if span.Name() == "mcp.client.request" {
			clientSpan = span
			break
		}
	}
	if clientSpan == nil {
		t.Fatal("client SDK did not record mcp.tools/call span")
	}
	clientSpanID := clientSpan.SpanContext().SpanID()
	if !bytes.Equal(serverSpan.ParentSpanId, clientSpanID[:]) {
		current := serverSpan
		foundClientParent := false
		mcpSeen := strings.HasPrefix(current.Name, "mcp.")
		for depth := 0; depth < 4; depth++ {
			if bytes.Equal(current.ParentSpanId, clientSpanID[:]) {
				foundClientParent = true
				break
			}
			var parent *tracev1.Span
			for _, candidate := range spans {
				if bytes.Equal(candidate.SpanId, current.ParentSpanId) {
					parent = candidate
					break
				}
			}
			if parent == nil {
				break
			}
			current = parent
			mcpSeen = mcpSeen || strings.HasPrefix(current.Name, "mcp.")
		}
		if !foundClientParent || !mcpSeen {
			t.Fatalf("stdio span chain from %s does not reach SDK client span %x", serverSpan.Name, clientSpanID)
		}
	}
	for _, request := range exportedRequests {
		data := mustMarshalOTLP(request)
		if bytes.Contains(data, []byte("baggage-sentinel")) || bytes.Contains(data, []byte("stdio-fixture-key")) {
			t.Fatal("OTLP export contained baggage or fixture credential")
		}
	}
}

func runSTDIOTraceHelper() error {
	ctx := context.Background()
	shutdownTelemetry, err := telemetry.Init(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()
	evaluator, err := systemone.New(systemone.Config{
		APIKey: "stdio-fixture-key",
		URL:    os.Getenv("MCP_DECISION_TYPESAFE_URL"),
	})
	if err != nil {
		return err
	}
	return server.NewStdioServer(New(evaluator, "stdio-test")).Listen(ctx, os.Stdin, os.Stdout)
}

func stdioTraceSpansFromResource(resourceSpans *tracev1.ResourceSpans) []*tracev1.Span {
	if resourceSpans == nil {
		return nil
	}
	var spans []*tracev1.Span
	for _, scopeSpans := range resourceSpans.ScopeSpans {
		if scopeSpans != nil {
			spans = append(spans, scopeSpans.Spans...)
		}
	}
	return spans
}

func mustMarshalOTLP(request *collector.ExportTraceServiceRequest) []byte {
	data, err := proto.Marshal(request)
	if err != nil {
		panic(err)
	}
	return data
}

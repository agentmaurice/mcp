package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/decision/pkg/systemone"
	"github.com/agentmaurice/mcpchatui/mcp/decision/telemetry"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestHTTPTransportsAndTools(t *testing.T) {
	for _, transport := range []string{"streamable", "sse"} {
		t.Run(transport, func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req struct {
					Questions map[string]struct {
						Type string `json:"type"`
					} `json:"questions"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("invalid provider request")
					w.WriteHeader(422)
					return
				}
				answers := map[string]any{}
				for id, q := range req.Questions {
					switch q.Type {
					case "choice":
						answers[id] = map[string]any{"type": "choice", "choice": "article", "confidence": 0.8, "probabilities": map[string]float64{"article": 0.9, "product": 0.1}}
					case "score":
						answers[id] = map[string]any{"type": "score", "score": 0.75, "confidence": 0.8, "probabilities": map[string]float64{"0": 0.25, "1": 0.75}, "legend": map[string]string{"0": "Low", "1": "High"}}
					case "noul":
						answers[id] = map[string]any{"type": "noul", "noul": 0.9}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-latest", "answers": answers, "usage": map[string]int{"input_tokens": 10, "output_tokens": 20}})
			}))
			defer provider.Close()
			evaluator, e := systemone.New(systemone.Config{APIKey: "secret", URL: provider.URL})
			if e != nil {
				t.Fatal(e)
			}
			srv := httptest.NewServer(Handler(New(evaluator, "test")))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var session *client.Client
			if transport == "sse" {
				session, e = client.NewSSEMCPClient(srv.URL + "/sse")
			} else {
				session, e = client.NewStreamableHttpClient(srv.URL + "/mcp")
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = session.Start(ctx); e != nil {
				t.Fatal(e)
			}
			if _, e = session.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				Capabilities:    mcp.ClientCapabilities{},
				ClientInfo:      mcp.Implementation{Name: "decision-test", Version: "1.0.0"},
			}}); e != nil {
				t.Fatal(e)
			}
			defer session.Close()
			tools, e := session.ListTools(ctx, mcp.ListToolsRequest{})
			if e != nil {
				t.Fatal(e)
			}
			if len(tools.Tools) != 6 {
				t.Fatalf("want six tools, got %d", len(tools.Tools))
			}
			names := map[string]bool{}
			for _, tool := range tools.Tools {
				names[tool.Name] = true
				if tool.Description == "" || tool.InputSchema.Type == "" || tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
					t.Fatalf("incomplete tool %s", tool.Name)
				}
			}
			for _, name := range []string{"health", "capabilities", "ask", "choice", "score", "noul"} {
				if !names["decision_"+name+"_v1"] {
					t.Fatal("missing tool", name)
				}
			}
			call := func(name string, args any) *mcp.CallToolResult {
				t.Helper()
				out, e := session.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "decision_" + name + "_v1", Arguments: args}})
				if e != nil {
					t.Fatal(e)
				}
				return out
			}
			for _, name := range []string{"health", "capabilities"} {
				if call(name, map[string]any{}).IsError {
					t.Fatal("local tool failed")
				}
			}
			if calls.Load() != 0 {
				t.Fatal("health/capabilities spent provider tokens")
			}
			choice := map[string]any{"instructions": "Classify", "criteria": map[string]string{"article": "News", "product": "Product"}}
			for _, kind := range []string{"choice", "score", "noul"} {
				args := map[string]any{"state": "Synthetic public example", "instructions": "Evaluate"}
				if kind == "choice" {
					args["criteria"] = choice["criteria"]
					args["min_confidence"] = 0.9
				}
				if kind == "score" {
					args["criteria"] = []string{"Low", "High"}
					args["min_confidence"] = 0.9
				}
				out := call(kind, args)
				if out.IsError || out.StructuredContent == nil {
					t.Fatalf("%s failed: %+v", kind, out)
				}
				b, _ := json.Marshal(out.StructuredContent)
				if kind == "choice" && !strings.Contains(string(b), `"choice":"uncertain"`) {
					t.Fatal("choice threshold ignored")
				}
				if kind == "score" && !strings.Contains(string(b), `"score":null`) {
					t.Fatal("score threshold ignored")
				}
			}
			before := calls.Load()
			out := call("ask", map[string]any{"state": map[string]any{"text": "Synthetic"}, "questions": map[string]any{
				"kind":    map[string]any{"type": "choice", "instructions": "Classify", "criteria": choice["criteria"]},
				"quality": map[string]any{"type": "score", "instructions": "Quality", "criteria": []string{"Low", "High"}},
				"urgent":  map[string]any{"type": "noul", "instructions": "Urgent?"},
			}})
			if out.IsError || calls.Load() != before+1 {
				t.Fatal("MCP batch did not preserve one call")
			}
			before = calls.Load()
			for _, args := range []any{
				map[string]any{"state": "PRIVATE_STATE", "instructions": "Q", "criteria": map[string]string{"article": "A", "uncertain": "U"}},
				map[string]any{"state": "PRIVATE_STATE", "instructions": "Q", "criteria": choice["criteria"], "confidence_kind": "calibrated"},
				map[string]any{"state": "PRIVATE_STATE", "instructions": "Q", "criteria": choice["criteria"], "PRIVATE_KEY": "SECRET"},
				map[string]any{"state": "PRIVATE_STATE", "instructions": "Q", "criteria": choice["criteria"], "min_confidence": 2},
			} {
				out := call("choice", args)
				if !out.IsError {
					t.Fatal("invalid arguments accepted")
				}
				b, _ := json.Marshal(out)
				if strings.Contains(string(b), "PRIVATE_") || strings.Contains(string(b), "SECRET") {
					t.Fatal("secret in tool error")
				}
			}
			if calls.Load() != before {
				t.Fatal("invalid MCP calls reached provider")
			}
		})
	}
}

func TestDecodeStrictObject(t *testing.T) {
	for _, b := range []string{"null", "[]", "{} {}", `{"unknown":"private"}`} {
		var v struct{}
		if decode([]byte(b), &v) == nil {
			t.Fatalf("accepted %s", b)
		}
	}
}

func TestHTTPClientTraceparentReachesServerAndProvider(t *testing.T) {
	var providerTraceparent, providerTracestate string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerTraceparent = r.Header.Get("traceparent")
		providerTracestate = r.Header.Get("tracestate")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-latest", "answers": map[string]any{
			"answer": map[string]any{"type": "noul", "noul": 0.9},
		}, "usage": map[string]int{}})
	}))
	defer provider.Close()
	evaluator, err := systemone.New(systemone.Config{APIKey: "secret", URL: provider.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"OTEL_SDK_DISABLED", "OTEL_EXPORTER", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_PROTOCOL"} {
		t.Setenv(key, "")
	}
	previousPropagator := otel.GetTextMapPropagator()
	defer otel.SetTextMapPropagator(previousPropagator)
	shutdown, err := telemetry.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tracerProvider)
	defer func() { otel.SetTracerProvider(previous); _ = tracerProvider.Shutdown(context.Background()) }()
	state, err := trace.ParseTraceState("vendor=qualification")
	if err != nil {
		t.Fatal(err)
	}
	root := trace.NewSpanContext(trace.SpanContextConfig{TraceID: [16]byte{21, 22, 23}, SpanID: [8]byte{24, 25, 26}, TraceFlags: trace.FlagsSampled, TraceState: state})
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(trace.ContextWithSpanContext(context.Background(), root), carrier)
	srv := httptest.NewServer(Handler(New(evaluator, "test")))
	defer srv.Close()
	session, err := client.NewStreamableHttpClient(srv.URL+"/mcp", transport.WithHTTPHeaderFunc(func(context.Context) map[string]string {
		return map[string]string{"traceparent": carrier.Get("traceparent"), "tracestate": carrier.Get("tracestate")}
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = session.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err = session.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION, Capabilities: mcp.ClientCapabilities{}, ClientInfo: mcp.Implementation{Name: "trace-test", Version: "1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = session.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "decision_noul_v1", Arguments: map[string]any{"state": "synthetic", "instructions": "Is this synthetic?"}}}); err != nil {
		t.Fatal(err)
	}
	providerContext := propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(http.Header{"Traceparent": []string{providerTraceparent}}))
	providerSC := trace.SpanContextFromContext(providerContext)
	if !providerSC.IsValid() || providerSC.TraceID() != root.TraceID() {
		t.Fatalf("provider lost incoming traceparent: %q", providerTraceparent)
	}
	if providerTracestate != "vendor=qualification" {
		t.Fatalf("provider lost tracestate: %q", providerTracestate)
	}
	foundProvider := false
	foundRootChild := false
	for _, span := range recorder.Ended() {
		if span.Parent().SpanID() == root.SpanID() {
			foundRootChild = true
		}
		if span.Name() == "provider.http.request" {
			foundProvider = true
			if span.Parent().TraceID() != root.TraceID() || !span.Parent().SpanID().IsValid() {
				t.Fatalf("provider span parent mismatch: %v", span.Parent())
			}
		}
	}
	if !foundProvider {
		t.Fatal("provider span was not recorded")
	}
	if !foundRootChild {
		t.Fatal("no span descended from incoming root span")
	}
}

func TestHealthEndpoints(t *testing.T) {
	c, _ := systemone.New(systemone.Config{APIKey: "secret"})
	s := httptest.NewServer(Handler(New(c, "test")))
	defer s.Close()
	for _, p := range []string{"/health", "/ready"} {
		r, e := http.Get(s.URL + p)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal(fmt.Sprint(r.StatusCode))
		}
	}
}

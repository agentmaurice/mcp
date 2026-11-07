package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestInitExportsNamedServiceToExactTracesEndpoint(t *testing.T) {
	received := make(chan *collector.ExportTraceServiceRequest, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/traces" {
			t.Errorf("path=%s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		req := new(collector.ExportTraceServiceRequest)
		if err := proto.Unmarshal(body, req); err != nil {
			t.Error(err)
		}
		received <- req
		w.WriteHeader(200)
	}))
	defer receiver.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", receiver.URL+"/custom/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_SERVICE_NAME", "decision-fixture")
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("fixture").Start(context.Background(), "safe")
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-received:
		for _, resource := range req.ResourceSpans {
			for _, attr := range resource.Resource.Attributes {
				if attr.Key == "service.name" && attr.Value.GetStringValue() == "decision-fixture" {
					return
				}
			}
		}
		t.Fatal("service name was not exported")
	default:
		t.Fatal("no OTLP spans were exported")
	}
}

func TestInitDisabledRecordsNoSpan(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(context.Background())
	_, span := otel.Tracer("fixture").Start(context.Background(), "disabled")
	defer span.End()
	if span.IsRecording() {
		t.Fatal("disabled SDK recorded a span")
	}
}

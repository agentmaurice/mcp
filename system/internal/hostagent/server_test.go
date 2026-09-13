package hostagent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/system/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/protocol"
)

type fakeBackend struct {
	restarted []string
	logs      string
}

func (f *fakeBackend) Inspect(context.Context) (protocol.Inspection, error) {
	return protocol.Inspection{Hostname: "vm-test", OS: "linux", Architecture: "amd64"}, nil
}
func (f *fakeBackend) Services(_ context.Context, names []string) ([]protocol.Service, error) {
	result := make([]protocol.Service, 0, len(names))
	for _, name := range names {
		result = append(result, protocol.Service{Name: name, Active: "active", Sub: "running"})
	}
	return result, nil
}
func (f *fakeBackend) Logs(context.Context, string, int) (string, error) { return f.logs, nil }
func (f *fakeBackend) RestartService(_ context.Context, service string) (string, error) {
	f.restarted = append(f.restarted, service)
	return "restarted", nil
}
func (f *fakeBackend) RestartRuntime(context.Context) (string, error) {
	f.restarted = append(f.restarted, "runtime")
	return "restarted", nil
}

func TestDiagnosticsAndRedactedLogs(t *testing.T) {
	backend := &fakeBackend{logs: "ready\napi_key=super-secret\nAuthorization: Bearer abc123\n"}
	agent := testAgent("disabled", backend)
	server := httptest.NewServer(agent.Handler())
	defer server.Close()

	var inspection protocol.Inspection
	getJSON(t, server.URL+"/v1/inspect", &inspection)
	if inspection.Hostname != "vm-test" {
		t.Fatalf("unexpected hostname %q", inspection.Hostname)
	}
	var logs protocol.Logs
	postJSON(t, server.URL+"/v1/logs", protocol.LogsRequest{Service: "agentmaurice.service", Lines: 10}, http.StatusOK, &logs)
	joined := strings.Join(logs.Lines, "\n")
	if !logs.Redacted || strings.Contains(joined, "super-secret") || strings.Contains(joined, "abc123") {
		t.Fatalf("secrets were not redacted: %#v", logs)
	}
}

func TestLogsAreTruncatedAtServerBoundary(t *testing.T) {
	backend := &fakeBackend{logs: strings.Repeat("x", maxLogBytes+1024)}
	agent := testAgent("disabled", backend)
	server := httptest.NewServer(agent.Handler())
	defer server.Close()

	var logs protocol.Logs
	postJSON(t, server.URL+"/v1/logs", protocol.LogsRequest{Service: "agentmaurice.service", Lines: 10}, http.StatusOK, &logs)
	if !logs.Truncated || len(strings.Join(logs.Lines, "\n")) != maxLogBytes {
		t.Fatalf("expected bounded logs, got truncated=%v bytes=%d", logs.Truncated, len(strings.Join(logs.Lines, "\n")))
	}
}

func TestRejectsNonAllowlistedServiceAndDisabledMutation(t *testing.T) {
	agent := testAgent("disabled", &fakeBackend{})
	server := httptest.NewServer(agent.Handler())
	defer server.Close()

	postJSON(t, server.URL+"/v1/logs", protocol.LogsRequest{Service: "ssh.service", Lines: 10}, http.StatusForbidden, nil)
	postJSON(t, server.URL+"/v1/actions/apply", protocol.ApplyRequest{PlanID: "x", PlanHash: strings.Repeat("0", 64), Actor: "test"}, http.StatusForbidden, nil)
}

func TestPlanApplyRejectsHashMismatchAndReplay(t *testing.T) {
	backend := &fakeBackend{}
	agent := testAgent("standing_grant", backend)
	server := httptest.NewServer(agent.Handler())
	defer server.Close()

	plan := createPlan(t, server.URL, "service_restart", "agentmaurice.service")
	postJSON(t, server.URL+"/v1/actions/apply", protocol.ApplyRequest{PlanID: plan.ID, PlanHash: strings.Repeat("0", 64), Actor: "test"}, http.StatusConflict, nil)
	postJSON(t, server.URL+"/v1/actions/apply", protocol.ApplyRequest{PlanID: plan.ID, PlanHash: plan.Hash, Actor: "test"}, http.StatusOK, nil)
	postJSON(t, server.URL+"/v1/actions/apply", protocol.ApplyRequest{PlanID: plan.ID, PlanHash: plan.Hash, Actor: "test"}, http.StatusConflict, nil)
	if len(backend.restarted) != 1 || backend.restarted[0] != "agentmaurice.service" {
		t.Fatalf("unexpected actions: %#v", backend.restarted)
	}
}

func TestRejectsExpiredAndStalePlans(t *testing.T) {
	agent := testAgent("standing_grant", &fakeBackend{})
	server := httptest.NewServer(agent.Handler())
	defer server.Close()

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	agent.plans.now = func() time.Time { return base }
	expired := createPlan(t, server.URL, "service_restart", "agentmaurice.service")
	agent.plans.now = func() time.Time { return base.Add(6 * time.Minute) }
	postJSON(t, server.URL+"/v1/actions/apply", protocol.ApplyRequest{PlanID: expired.ID, PlanHash: expired.Hash, Actor: "test"}, http.StatusConflict, nil)

	agent.plans.now = func() time.Time { return base }
	stale := createPlan(t, server.URL, "service_restart", "agentmaurice.service")
	agent.cfg.AllowedServices = append(agent.cfg.AllowedServices, "worker.service")
	postJSON(t, server.URL+"/v1/actions/apply", protocol.ApplyRequest{PlanID: stale.ID, PlanHash: stale.Hash, Actor: "test"}, http.StatusConflict, nil)
}

func testAgent(mode string, backend Backend) *Agent {
	cfg := config.Host{
		SocketPath: "/tmp/system-test.sock", MutationMode: mode,
		AllowedServices: []string{"agentmaurice.service"}, RuntimeRestartScript: "/usr/local/libexec/agentmaurice-runtime-restart",
		MaxLogLines: 100, PlanTTL: 5 * time.Minute, CommandTimeout: time.Second,
	}
	return New(cfg, backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func createPlan(t *testing.T, baseURL, action, target string) protocol.Plan {
	t.Helper()
	var plan protocol.Plan
	postJSON(t, baseURL+"/v1/actions/plan", protocol.PlanRequest{Action: action, Target: target}, http.StatusOK, &plan)
	return plan
}

func getJSON(t *testing.T, url string, target any) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected HTTP status %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func postJSON(t *testing.T, url string, value any, expectedStatus int, target any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected HTTP status %d: %s", response.StatusCode, body)
	}
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
}

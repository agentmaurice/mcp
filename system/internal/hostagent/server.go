package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/system/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/protocol"
)

const (
	serviceVersion  = "0.1.0"
	maxRequestBytes = 64 * 1024
	maxLogBytes     = 256 * 1024
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)((?:password|token|secret|api[_-]?key)\s*[=:]\s*)[^\s]+`),
}

type Agent struct {
	cfg     config.Host
	backend Backend
	plans   *planStore
	logger  *slog.Logger
	applyMu sync.Mutex
}

func New(cfg config.Host, backend Backend, logger *slog.Logger) *Agent {
	return &Agent{cfg: cfg, backend: backend, plans: newPlanStore(cfg.PlanTTL), logger: logger}
}

func (a *Agent) Serve(ctx context.Context) error {
	if err := prepareSocket(a.cfg.SocketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", a.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on Unix socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(a.cfg.SocketPath)
	if err := os.Chmod(a.cfg.SocketPath, 0o660); err != nil {
		return fmt.Errorf("restrict Unix socket: %w", err)
	}
	server := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       30 * time.Second,
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-stopped
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", a.health)
	mux.HandleFunc("GET /v1/capabilities", a.capabilities)
	mux.HandleFunc("GET /v1/inspect", a.inspect)
	mux.HandleFunc("GET /v1/services", a.services)
	mux.HandleFunc("POST /v1/logs", a.logs)
	mux.HandleFunc("POST /v1/actions/plan", a.plan)
	mux.HandleFunc("POST /v1/actions/apply", a.apply)
	return http.MaxBytesHandler(mux, maxRequestBytes)
}

func (a *Agent) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, protocol.Health{Status: "ok", Version: serviceVersion, MutationMode: a.cfg.MutationMode})
}

func (a *Agent) capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, protocol.Capabilities{
		Version: protocol.Version, Transport: "unix", Diagnostics: []string{"inspect", "services", "logs"},
		Actions: []string{"service_restart", "runtime_restart"}, AllowedServices: a.cfg.AllowedServices,
		MutationsEnabled: a.cfg.MutationMode == "standing_grant", MaxLogLines: a.cfg.MaxLogLines,
		ArbitraryShell: false, ArbitraryFilesystem: false,
	})
}

func (a *Agent) inspect(w http.ResponseWriter, r *http.Request) {
	result, err := a.backend.Inspect(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "inspection_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *Agent) services(w http.ResponseWriter, r *http.Request) {
	result, err := a.backend.Services(r.Context(), a.cfg.AllowedServices)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "service_inspection_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, protocol.Services{Services: result})
}

func (a *Agent) logs(w http.ResponseWriter, r *http.Request) {
	var request protocol.LogsRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !contains(a.cfg.AllowedServices, request.Service) {
		writeError(w, http.StatusForbidden, "service_not_allowed", "service is not allowlisted")
		return
	}
	if request.Lines == 0 {
		request.Lines = min(100, a.cfg.MaxLogLines)
	}
	if request.Lines < 1 || request.Lines > a.cfg.MaxLogLines {
		writeError(w, http.StatusBadRequest, "invalid_lines", fmt.Sprintf("lines must be between 1 and %d", a.cfg.MaxLogLines))
		return
	}
	output, err := a.backend.Logs(r.Context(), request.Service, request.Lines)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "logs_failed", err.Error())
		return
	}
	redacted, changed := redact(output)
	truncated := false
	if len(redacted) > maxLogBytes {
		redacted = redacted[:maxLogBytes]
		truncated = true
	}
	lines := strings.Split(strings.TrimRight(redacted, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, protocol.Logs{Service: request.Service, Lines: lines, Truncated: truncated, Redacted: changed})
}

func (a *Agent) plan(w http.ResponseWriter, r *http.Request) {
	var request protocol.PlanRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := a.plans.create(a.cfg, request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_action", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (a *Agent) apply(w http.ResponseWriter, r *http.Request) {
	if a.cfg.MutationMode != "standing_grant" {
		writeError(w, http.StatusForbidden, "mutations_disabled", "administrator standing grant is not enabled")
		return
	}
	var request protocol.ApplyRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(request.Actor) == "" {
		writeError(w, http.StatusBadRequest, "actor_required", "actor is required for the audit trail")
		return
	}
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	plan, err := a.plans.consume(a.cfg, request.PlanID, request.PlanHash)
	if err != nil {
		writeError(w, http.StatusConflict, "plan_rejected", err.Error())
		return
	}
	auditID, err := randomID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit_id_failed", "could not create audit identifier")
		return
	}
	a.logger.Info("system action started", "audit_id", auditID, "actor", request.Actor, "organization_id", request.OrganizationID, "deployment_id", request.DeploymentID, "tool", "system_action_apply_v1", "plan_id", plan.ID, "action", plan.Action, "target", plan.Target)
	startedAt := time.Now()
	var output string
	switch plan.Action {
	case "service_restart":
		output, err = a.backend.RestartService(r.Context(), plan.Target)
	case "runtime_restart":
		output, err = a.backend.RestartRuntime(r.Context())
	default:
		err = fmt.Errorf("unsupported planned action")
	}
	if err != nil {
		a.logger.Error("system action failed", "audit_id", auditID, "actor", request.Actor, "plan_id", plan.ID, "duration_ms", time.Since(startedAt).Milliseconds(), "error", err)
		writeError(w, http.StatusServiceUnavailable, "action_failed", err.Error())
		return
	}
	if plan.Action == "service_restart" {
		states, verifyErr := a.backend.Services(r.Context(), []string{plan.Target})
		if verifyErr != nil || len(states) != 1 || states[0].Active != "active" {
			a.logger.Error("system action health verification failed", "audit_id", auditID, "actor", request.Actor, "plan_id", plan.ID, "duration_ms", time.Since(startedAt).Milliseconds())
			writeError(w, http.StatusServiceUnavailable, "health_verification_failed", "service did not return to active state")
			return
		}
	}
	output, _ = redact(output)
	if len(output) > 4096 {
		output = output[:4096]
	}
	a.logger.Info("system action completed", "audit_id", auditID, "actor", request.Actor, "plan_id", plan.ID, "duration_ms", time.Since(startedAt).Milliseconds())
	writeJSON(w, http.StatusOK, protocol.ApplyResult{Status: "applied", PlanID: plan.ID, AuditID: auditID, Action: plan.Action, Target: plan.Target, Output: strings.TrimSpace(output)})
}

func prepareSocket(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket path %s", path)
	}
	return os.Remove(path)
}

func decode(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSONStatus(w, status, protocol.Error{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	writeJSONStatus(w, status, value)
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func redact(value string) (string, bool) {
	result := value
	for _, pattern := range secretPatterns {
		result = pattern.ReplaceAllString(result, "${1}[REDACTED]")
	}
	return result, result != value
}

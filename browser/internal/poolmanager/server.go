package poolmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

type Server struct {
	manager       *Manager
	authToken     string
	defaultPoolID string
	logger        *zap.Logger
	http          *http.Server
}

type acquireRequest struct {
	SessionKey    string `json:"session_key"`
	ClientID      string `json:"client_id,omitempty"`
	PoolID        string `json:"pool_id,omitempty"`
	QueueTimeoutS int    `json:"queue_timeout_s,omitempty"`
}

type releaseRequest struct {
	SessionKey string `json:"session_key"`
	ClientID   string `json:"client_id,omitempty"`
	PoolID     string `json:"pool_id,omitempty"`
}

type reconcileRequest struct {
	PoolID                 string         `json:"pool_id"`
	Provider               string         `json:"provider"`
	Mode                   string         `json:"mode"`
	SelectionPolicy        string         `json:"selection_policy,omitempty"`
	MinWarmInstances       int            `json:"min_warm_instances,omitempty"`
	MaxInstances           int            `json:"max_instances,omitempty"`
	MaxSessionsPerInstance int            `json:"max_sessions_per_instance,omitempty"`
	ScaleUpCooldownS       int            `json:"scale_up_cooldown_s,omitempty"`
	ScaleDownCooldownS     int            `json:"scale_down_cooldown_s,omitempty"`
	IdleTTLS               int            `json:"idle_ttl_s,omitempty"`
	StartupTimeoutS        int            `json:"startup_timeout_s,omitempty"`
	QueueTimeoutS          int            `json:"queue_timeout_s,omitempty"`
	ProviderConfig         map[string]any `json:"provider_config,omitempty"`
}

func NewServer(cfg *ServiceConfig, logger *zap.Logger) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("pool-manager config is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	manager, err := NewManager(cfg, logger)
	if err != nil {
		return nil, err
	}
	defaultPoolID := "default"
	if cfg.DefaultPool != nil {
		defaultPoolID = normalizePoolID(cfg.DefaultPool.PoolID)
	}

	server := &Server{
		manager:       manager,
		authToken:     strings.TrimSpace(cfg.AuthToken),
		defaultPoolID: defaultPoolID,
		logger:        logger.Named("pool-manager"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", server.handleHealth)
	mux.HandleFunc("/v1/pools/reconcile", server.handleReconcile)
	mux.HandleFunc("/v1/pools/", server.handlePoolRoutes)

	// Backward compatibility endpoints.
	mux.HandleFunc("/v1/sessions/acquire", server.handleAcquireLegacy)
	mux.HandleFunc("/v1/sessions/release", server.handleReleaseLegacy)
	mux.HandleFunc("/v1/pool/status", server.handleStatusLegacy)

	server.http = &http.Server{
		Addr:              cfg.Address,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return server, nil
}

func (s *Server) ListenAndServe() error {
	s.logger.Info("pool-manager starting", zap.String("address", s.http.Addr))
	if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	s.manager.Shutdown(ctx)
	return s.http.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "healthy"})
}

func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req reconcileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	spec := PoolSpec{
		PoolID:                 normalizePoolID(req.PoolID),
		Provider:               strings.ToLower(strings.TrimSpace(req.Provider)),
		Mode:                   strings.ToLower(strings.TrimSpace(req.Mode)),
		SelectionPolicy:        normalizeSelectionPolicy(req.SelectionPolicy),
		MinWarmInstances:       req.MinWarmInstances,
		MaxInstances:           req.MaxInstances,
		MaxSessionsPerInstance: req.MaxSessionsPerInstance,
		ScaleUpCooldown:        secondsToDuration(req.ScaleUpCooldownS),
		ScaleDownCooldown:      secondsToDuration(req.ScaleDownCooldownS),
		IdleTTL:                secondsToDuration(req.IdleTTLS),
		StartupTimeout:         secondsToDuration(req.StartupTimeoutS),
		QueueTimeout:           secondsToDuration(req.QueueTimeoutS),
		ProviderConfig:         req.ProviderConfig,
	}
	status, err := s.manager.ReconcilePool(r.Context(), spec)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handlePoolRoutes(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/pools/")
	path = strings.Trim(path, "/")
	if path == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pool path not found"})
		return
	}
	parts := strings.Split(path, "/")
	poolID := normalizePoolID(parts[0])
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "allocate-session" && r.Method == http.MethodPost:
		s.handleAllocateSession(w, r, poolID)
		return
	case action == "release-session" && r.Method == http.MethodPost:
		s.handleReleaseSession(w, r, poolID)
		return
	case action == "status" && r.Method == http.MethodGet:
		s.handlePoolStatus(w, r, poolID)
		return
	case action == "" && r.Method == http.MethodDelete:
		s.handleDeletePool(w, r, poolID)
		return
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "unsupported pool action"})
	}
}

func (s *Server) handleAllocateSession(w http.ResponseWriter, r *http.Request, poolID string) {
	var req acquireRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	queueTimeout := secondsToDuration(req.QueueTimeoutS)
	lease, err := s.manager.AllocateSession(r.Context(), poolID, req.SessionKey, queueTimeout)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "capacity_exhausted") {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) handleReleaseSession(w http.ResponseWriter, r *http.Request, poolID string) {
	var req releaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.manager.ReleaseSession(poolID, req.SessionKey); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

func (s *Server) handlePoolStatus(w http.ResponseWriter, _ *http.Request, poolID string) {
	status, err := s.manager.PoolStatus(poolID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleDeletePool(w http.ResponseWriter, r *http.Request, poolID string) {
	if err := s.manager.DeletePool(r.Context(), poolID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleAcquireLegacy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req acquireRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	poolID := normalizePoolID(req.PoolID)
	if poolID == "default" {
		poolID = s.defaultPoolID
	}
	lease, err := s.manager.AllocateSession(r.Context(), poolID, req.SessionKey, secondsToDuration(req.QueueTimeoutS))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "capacity_exhausted") {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) handleReleaseLegacy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req releaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	poolID := normalizePoolID(req.PoolID)
	if poolID == "default" {
		poolID = s.defaultPoolID
	}
	if err := s.manager.ReleaseSession(poolID, req.SessionKey); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

func (s *Server) handleStatusLegacy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	status, err := s.manager.PoolStatus(s.defaultPoolID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) authorized(r *http.Request) bool {
	if s.authToken == "" {
		return true
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	return provided == s.authToken
}

func decodeJSON(r *http.Request, out any) error {
	if out == nil {
		return nil
	}
	defer r.Body.Close()
	if r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "eof") {
			return nil
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func secondsToDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

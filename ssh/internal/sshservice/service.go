package sshservice

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Mode                 string
	AllowedTargets       map[string]struct{}
	ConnectTimeout       time.Duration
	CommandTimeout       time.Duration
	SessionIdleTTL       time.Duration
	SessionAbsoluteTTL   time.Duration
	MaxSessions          int
	MaxSessionsPerTarget int
	MaxOutputBytes       int
	MaxCommandTimeout    time.Duration
}

func DefaultConfig() Config {
	return Config{
		Mode:                 ModeRun,
		ConnectTimeout:       15 * time.Second,
		CommandTimeout:       30 * time.Second,
		SessionIdleTTL:       15 * time.Minute,
		SessionAbsoluteTTL:   time.Hour,
		MaxSessions:          32,
		MaxSessionsPerTarget: 8,
		MaxOutputBytes:       256 * 1024,
		MaxCommandTimeout:    5 * time.Minute,
	}
}

func (c Config) Validate() error {
	if c.Mode != ModeBuild && c.Mode != ModeRun {
		return NewError("invalid_configuration", "SSH execution mode must be build or run")
	}
	if c.ConnectTimeout <= 0 || c.CommandTimeout <= 0 || c.SessionIdleTTL <= 0 || c.SessionAbsoluteTTL <= 0 || c.MaxCommandTimeout <= 0 {
		return NewError("invalid_configuration", "SSH timeouts must be positive")
	}
	if c.CommandTimeout > c.MaxCommandTimeout {
		return NewError("invalid_configuration", "default command timeout exceeds its maximum")
	}
	if c.MaxSessions < 1 || c.MaxSessionsPerTarget < 1 || c.MaxSessionsPerTarget > c.MaxSessions {
		return NewError("invalid_configuration", "SSH session limits are invalid")
	}
	if c.MaxOutputBytes < 1024 || c.MaxOutputBytes > 4*1024*1024 {
		return NewError("invalid_configuration", "SSH output limit must be between 1 KiB and 4 MiB")
	}
	return nil
}

type Service struct {
	config      Config
	targets     TargetStore
	credentials CredentialProvider
	connector   Connector
	now         func() time.Time

	mu       sync.Mutex
	sessions map[string]*managedSession
}

type managedSession struct {
	view       SessionView
	connection Connection
	busy       chan struct{}
}

func New(config Config, targets TargetStore, credentials CredentialProvider, connector Connector) (*Service, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if targets == nil || credentials == nil || connector == nil {
		return nil, NewError("invalid_configuration", "targets, credentials and connector are required")
	}
	return &Service{config: config, targets: targets, credentials: credentials, connector: connector, now: time.Now, sessions: map[string]*managedSession{}}, nil
}

func (s *Service) Health(ctx context.Context) (Health, error) {
	snapshot, err := s.targets.Snapshot(ctx)
	if err != nil {
		return Health{}, NewError("ssh_projection_unavailable", "target projection is unavailable")
	}
	s.sweepExpired()
	visible := s.visibleTargets(snapshot)
	s.mu.Lock()
	active := len(s.sessions)
	s.mu.Unlock()
	status := "ready"
	if !s.credentials.Available() {
		status = "degraded"
	}
	return Health{Status: status, Mode: s.config.Mode, Revision: snapshot.Revision, TargetsVisible: len(visible), ActiveSessions: active, CredentialsProvider: s.credentials.Available()}, nil
}

func (s *Service) Capabilities(ctx context.Context) (Capabilities, error) {
	snapshot, err := s.targets.Snapshot(ctx)
	if err != nil {
		return Capabilities{}, NewError("ssh_projection_unavailable", "target projection is unavailable")
	}
	return Capabilities{
		Mode: s.config.Mode, Revision: snapshot.Revision,
		AuthenticationMethods: []string{"private_key", "password"},
		Functions:             []string{"targets", "target_check", "persistent_sessions", "synchronous_exec", "strict_host_key"},
		MaxSessions:           s.config.MaxSessions, MaxSessionsPerTarget: s.config.MaxSessionsPerTarget,
		MaxCommandSeconds: int(s.config.MaxCommandTimeout.Seconds()), MaxOutputBytes: s.config.MaxOutputBytes,
	}, nil
}

func (s *Service) Targets(ctx context.Context) (Targets, error) {
	snapshot, err := s.targets.Snapshot(ctx)
	if err != nil {
		return Targets{}, NewError("ssh_projection_unavailable", "target projection is unavailable")
	}
	visible := s.visibleTargets(snapshot)
	result := Targets{Revision: snapshot.Revision, Mode: s.config.Mode, Targets: make([]TargetSummary, 0, len(visible))}
	for _, target := range visible {
		summary := TargetSummary{ID: target.ID, Label: target.Label, Enabled: target.Enabled, Published: target.Published, PolicyTags: append([]string(nil), target.PolicyTags...)}
		if s.config.Mode == ModeBuild {
			summary.Host, summary.Port, summary.Username, summary.Fingerprint = target.Host, target.Port, target.Username, target.HostKeyFingerprint
		}
		result.Targets = append(result.Targets, summary)
	}
	return result, nil
}

func (s *Service) Check(ctx context.Context, targetID string, timeout time.Duration) (CheckResult, error) {
	if s.config.Mode != ModeBuild {
		return CheckResult{}, NewError("ssh_build_only", "target checks are available only in build mode")
	}
	snapshot, target, err := s.resolveTarget(ctx, targetID, true)
	if err != nil {
		return CheckResult{}, err
	}
	credential, err := s.credentials.Resolve(ctx, target.CredentialRef, Purpose{TargetID: target.ID, Revision: snapshot.Revision, Mode: s.config.Mode})
	if err != nil {
		return CheckResult{}, normalizeError(err, "ssh_credential_unavailable", "credential reference is unavailable")
	}
	started := s.now()
	connection, err := s.connector.Connect(ctx, target, credential, boundedTimeout(timeout, s.config.ConnectTimeout, s.config.MaxCommandTimeout))
	if err != nil {
		return CheckResult{}, normalizeError(err, "ssh_connection_failed", "SSH target check failed")
	}
	_ = connection.Close()
	return CheckResult{TargetID: target.ID, Reachable: true, HostKeyVerified: true, HandshakeDuration: s.now().Sub(started).Milliseconds()}, nil
}

func (s *Service) Open(ctx context.Context, targetID string, timeout time.Duration) (SessionView, error) {
	snapshot, target, err := s.resolveTarget(ctx, targetID, false)
	if err != nil {
		return SessionView{}, err
	}
	if !s.credentials.Available() {
		return SessionView{}, NewError("credentials_provider_unavailable", "no credential provider is configured")
	}
	if err := s.ensureCapacity(target.ID); err != nil {
		return SessionView{}, err
	}
	credential, err := s.credentials.Resolve(ctx, target.CredentialRef, Purpose{TargetID: target.ID, Revision: snapshot.Revision, Mode: s.config.Mode})
	if err != nil {
		return SessionView{}, normalizeError(err, "ssh_credential_unavailable", "credential reference is unavailable")
	}
	connection, err := s.connector.Connect(ctx, target, credential, boundedTimeout(timeout, s.config.ConnectTimeout, s.config.MaxCommandTimeout))
	if err != nil {
		return SessionView{}, normalizeError(err, "ssh_connection_failed", "SSH connection failed")
	}
	now := s.now().UTC()
	view := SessionView{SessionID: newSessionID(), TargetID: target.ID, TargetRevision: snapshot.Revision, Status: "open", OpenedAt: now, LastActivity: now, ExpiresAt: now.Add(s.config.SessionAbsoluteTTL)}
	s.mu.Lock()
	if len(s.sessions) >= s.config.MaxSessions || s.countTargetLocked(target.ID) >= s.config.MaxSessionsPerTarget {
		s.mu.Unlock()
		_ = connection.Close()
		return SessionView{}, NewError("ssh_session_limit", "SSH session limit reached")
	}
	s.sessions[view.SessionID] = &managedSession{view: view, connection: connection, busy: make(chan struct{}, 1)}
	s.mu.Unlock()
	return view, nil
}

func (s *Service) Exec(ctx context.Context, sessionID, command, cwd string, timeout time.Duration, maxOutput int) (ExecResult, error) {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsRune(command, 0) {
		return ExecResult{}, NewError("invalid_arguments", "command must be non-empty and contain no NUL byte")
	}
	if cwd != "" && (!filepath.IsAbs(cwd) || strings.ContainsRune(cwd, 0)) {
		return ExecResult{}, NewError("invalid_arguments", "cwd must be an absolute path without NUL bytes")
	}
	session, err := s.session(sessionID)
	if err != nil {
		return ExecResult{}, err
	}
	select {
	case session.busy <- struct{}{}:
		defer func() { <-session.busy }()
	default:
		return ExecResult{}, NewError("ssh_session_busy", "another command is already running on this session")
	}
	commandTimeout := boundedTimeout(timeout, s.config.CommandTimeout, s.config.MaxCommandTimeout)
	if maxOutput <= 0 {
		maxOutput = s.config.MaxOutputBytes
	}
	if maxOutput > s.config.MaxOutputBytes {
		return ExecResult{}, NewError("invalid_arguments", "max_output_bytes exceeds the configured limit")
	}
	execCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := session.connection.Exec(execCtx, command, cwd, maxOutput)
	now := s.now().UTC()
	s.mu.Lock()
	if current, ok := s.sessions[sessionID]; ok {
		current.view.LastActivity = now
	}
	s.mu.Unlock()
	if err != nil {
		return result, normalizeError(err, "ssh_execution_failed", "remote command failed")
	}
	return result, nil
}

func (s *Service) Status(_ context.Context, sessionID string) (SessionView, error) {
	session, err := s.session(sessionID)
	if err != nil {
		return SessionView{}, err
	}
	s.mu.Lock()
	view := session.view
	s.mu.Unlock()
	return view, nil
}

func (s *Service) CloseSession(_ context.Context, sessionID string) (SessionView, error) {
	s.mu.Lock()
	session, ok := s.sessions[sessionID]
	if ok {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()
	if !ok {
		return SessionView{}, NewError("ssh_session_unknown", "SSH session is unknown")
	}
	_ = session.connection.Close()
	session.view.Status = "closed"
	return session.view, nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	sessions := s.sessions
	s.sessions = map[string]*managedSession{}
	s.mu.Unlock()
	for _, session := range sessions {
		_ = session.connection.Close()
	}
	return nil
}

func (s *Service) visibleTargets(snapshot TargetSnapshot) []Target {
	result := make([]Target, 0, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		if s.config.Mode == ModeRun && !target.Published {
			continue
		}
		if !s.allowed(target.ID) {
			continue
		}
		result = append(result, target)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Service) resolveTarget(ctx context.Context, targetID string, allowDraft bool) (TargetSnapshot, Target, error) {
	targetID = strings.TrimSpace(targetID)
	if !targetIDPattern.MatchString(targetID) {
		return TargetSnapshot{}, Target{}, NewError("ssh_target_unknown", "SSH target is unknown")
	}
	snapshot, err := s.targets.Snapshot(ctx)
	if err != nil {
		return TargetSnapshot{}, Target{}, NewError("ssh_projection_unavailable", "target projection is unavailable")
	}
	for _, target := range snapshot.Targets {
		if target.ID != targetID {
			continue
		}
		if !s.allowed(target.ID) {
			return TargetSnapshot{}, Target{}, NewError("ssh_target_not_granted", "SSH target is not granted")
		}
		if s.config.Mode == ModeRun && !target.Published {
			return TargetSnapshot{}, Target{}, NewError("ssh_target_not_published", "SSH target is not published")
		}
		if !allowDraft && !target.Enabled {
			return TargetSnapshot{}, Target{}, NewError("ssh_target_disabled", "SSH target is disabled")
		}
		return snapshot, target, nil
	}
	return TargetSnapshot{}, Target{}, NewError("ssh_target_unknown", "SSH target is unknown")
}

func (s *Service) allowed(targetID string) bool {
	if len(s.config.AllowedTargets) == 0 {
		return true
	}
	_, ok := s.config.AllowedTargets[targetID]
	return ok
}

func (s *Service) ensureCapacity(targetID string) error {
	s.sweepExpired()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) >= s.config.MaxSessions || s.countTargetLocked(targetID) >= s.config.MaxSessionsPerTarget {
		return NewError("ssh_session_limit", "SSH session limit reached")
	}
	return nil
}

func (s *Service) countTargetLocked(targetID string) int {
	count := 0
	for _, session := range s.sessions {
		if session.view.TargetID == targetID {
			count++
		}
	}
	return count
}

func (s *Service) session(sessionID string) (*managedSession, error) {
	s.sweepExpired()
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[strings.TrimSpace(sessionID)]
	if !ok {
		return nil, NewError("ssh_session_unknown", "SSH session is unknown")
	}
	return session, nil
}

func (s *Service) sweepExpired() {
	now := s.now().UTC()
	var expired []*managedSession
	s.mu.Lock()
	for id, session := range s.sessions {
		if now.After(session.view.ExpiresAt) || now.Sub(session.view.LastActivity) > s.config.SessionIdleTTL {
			delete(s.sessions, id)
			expired = append(expired, session)
		}
	}
	s.mu.Unlock()
	for _, session := range expired {
		_ = session.connection.Close()
	}
}

func newSessionID() string {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		panic("crypto/rand unavailable")
	}
	return "ssh_sess_" + base64.RawURLEncoding.EncodeToString(buffer)
}

func boundedTimeout(requested, fallback, maximum time.Duration) time.Duration {
	if requested <= 0 {
		requested = fallback
	}
	if requested > maximum {
		requested = maximum
	}
	return requested
}

func normalizeError(err error, fallbackCode, fallbackMessage string) error {
	var serviceError *Error
	if errors.As(err, &serviceError) {
		return serviceError
	}
	return NewError(fallbackCode, "%s", fallbackMessage)
}

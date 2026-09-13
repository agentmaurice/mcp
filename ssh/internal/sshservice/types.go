package sshservice

import (
	"context"
	"fmt"
	"time"
)

const (
	ModeBuild = "build"
	ModeRun   = "run"
)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func NewError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

type Target struct {
	ID                 string   `json:"target_id"`
	Label              string   `json:"label"`
	Host               string   `json:"host"`
	Port               int      `json:"port"`
	Username           string   `json:"username"`
	HostKeyFingerprint string   `json:"host_key_fingerprint"`
	CredentialRef      string   `json:"credential_ref"`
	Enabled            bool     `json:"enabled"`
	Published          bool     `json:"published"`
	PolicyTags         []string `json:"policy_tags,omitempty"`
}

type TargetSnapshot struct {
	Revision string   `json:"revision"`
	Targets  []Target `json:"targets"`
}

type TargetStore interface {
	Snapshot(context.Context) (TargetSnapshot, error)
}

type Credential struct {
	Method     string `json:"method"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Password   string `json:"password,omitempty"`
}

type Purpose struct {
	TargetID string
	Revision string
	Mode     string
}

type CredentialProvider interface {
	Available() bool
	Resolve(context.Context, string, Purpose) (Credential, error)
}

type ExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Duration  int64  `json:"duration_ms"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

type Connection interface {
	Exec(context.Context, string, string, int) (ExecResult, error)
	Close() error
}

type Connector interface {
	Connect(context.Context, Target, Credential, time.Duration) (Connection, error)
}

type TargetSummary struct {
	ID          string   `json:"target_id"`
	Label       string   `json:"label"`
	Host        string   `json:"host,omitempty"`
	Port        int      `json:"port,omitempty"`
	Username    string   `json:"username,omitempty"`
	Fingerprint string   `json:"host_key_fingerprint,omitempty"`
	Enabled     bool     `json:"enabled"`
	Published   bool     `json:"published"`
	PolicyTags  []string `json:"policy_tags,omitempty"`
}

type Health struct {
	Status              string `json:"status"`
	Mode                string `json:"mode"`
	Revision            string `json:"revision"`
	TargetsVisible      int    `json:"targets_visible"`
	ActiveSessions      int    `json:"active_sessions"`
	CredentialsProvider bool   `json:"credentials_provider_available"`
}

type Capabilities struct {
	Mode                  string   `json:"mode"`
	Revision              string   `json:"revision"`
	AuthenticationMethods []string `json:"authentication_methods"`
	Functions             []string `json:"functions"`
	MaxSessions           int      `json:"max_sessions"`
	MaxSessionsPerTarget  int      `json:"max_sessions_per_target"`
	MaxCommandSeconds     int      `json:"max_command_seconds"`
	MaxOutputBytes        int      `json:"max_output_bytes"`
}

type Targets struct {
	Revision string          `json:"revision"`
	Mode     string          `json:"mode"`
	Targets  []TargetSummary `json:"targets"`
}

type CheckResult struct {
	TargetID          string `json:"target_id"`
	Reachable         bool   `json:"reachable"`
	HostKeyVerified   bool   `json:"host_key_verified"`
	HandshakeDuration int64  `json:"handshake_duration_ms"`
}

type SessionView struct {
	SessionID      string    `json:"session_id"`
	TargetID       string    `json:"target_id"`
	TargetRevision string    `json:"target_revision"`
	Status         string    `json:"status"`
	OpenedAt       time.Time `json:"opened_at"`
	LastActivity   time.Time `json:"last_activity_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

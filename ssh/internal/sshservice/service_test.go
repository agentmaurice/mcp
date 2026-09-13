package sshservice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeConnector struct {
	mu          sync.Mutex
	connections []*fakeConnection
	err         error
}

func (c *fakeConnector) Connect(_ context.Context, _ Target, _ Credential, _ time.Duration) (Connection, error) {
	if c.err != nil {
		return nil, c.err
	}
	connection := &fakeConnection{result: ExecResult{ExitCode: 0, Stdout: "ok"}}
	c.mu.Lock()
	c.connections = append(c.connections, connection)
	c.mu.Unlock()
	return connection, nil
}

type fakeConnection struct {
	mu     sync.Mutex
	result ExecResult
	err    error
	closed bool
	block  chan struct{}
}

func (c *fakeConnection) Exec(ctx context.Context, _, _ string, _ int) (ExecResult, error) {
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return ExecResult{TimedOut: true}, nil
		}
	}
	return c.result, c.err
}

func (c *fakeConnection) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func testService(t *testing.T, mode string) (*Service, *MemoryTargetStore, *fakeConnector) {
	t.Helper()
	store := &MemoryTargetStore{SnapshotValue: TargetSnapshot{Revision: "rev-1", Targets: []Target{
		{ID: "prod", Label: "Production", Host: "server.internal", Port: 22, Username: "agent", HostKeyFingerprint: "SHA256:test", CredentialRef: "prod-key", Enabled: true, Published: true},
		{ID: "draft", Label: "Draft", Host: "draft.internal", Port: 22, Username: "agent", HostKeyFingerprint: "", CredentialRef: "draft-key", Enabled: true, Published: false},
		{ID: "disabled", Label: "Disabled", Host: "disabled.internal", Port: 22, Username: "agent", HostKeyFingerprint: "SHA256:test", CredentialRef: "disabled-key", Enabled: false, Published: true},
	}}}
	provider := &MemoryCredentialProvider{Credentials: map[string]Credential{
		"prod-key":     {Method: "password", Password: "test-only"},
		"draft-key":    {Method: "password", Password: "test-only"},
		"disabled-key": {Method: "password", Password: "test-only"},
	}}
	connector := &fakeConnector{}
	config := DefaultConfig()
	config.Mode = mode
	service, err := New(config, store, provider, connector)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, connector
}

func TestTargetsRespectModeAndRedaction(t *testing.T) {
	run, _, _ := testService(t, ModeRun)
	result, err := run.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 2 {
		t.Fatalf("expected two published targets, got %d", len(result.Targets))
	}
	for _, target := range result.Targets {
		if target.Host != "" || target.Username != "" || target.Fingerprint != "" {
			t.Fatalf("run target leaked connection metadata: %#v", target)
		}
	}

	build, _, _ := testService(t, ModeBuild)
	result, err = build.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 3 || result.Targets[0].Host == "" {
		t.Fatalf("build projection should include drafts and connection metadata: %#v", result.Targets)
	}
}

func TestOpenExecStatusAndClose(t *testing.T) {
	service, _, connector := testService(t, ModeRun)
	opened, err := service.Open(context.Background(), "prod", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if opened.SessionID == "" || opened.TargetRevision != "rev-1" {
		t.Fatalf("unexpected session: %#v", opened)
	}
	result, err := service.Exec(context.Background(), opened.SessionID, "true", "/tmp", time.Second, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "ok" {
		t.Fatalf("unexpected exec result: %#v", result)
	}
	status, err := service.Status(context.Background(), opened.SessionID)
	if err != nil || status.Status != "open" {
		t.Fatalf("unexpected status: %#v %v", status, err)
	}
	closed, err := service.CloseSession(context.Background(), opened.SessionID)
	if err != nil || closed.Status != "closed" {
		t.Fatalf("unexpected close: %#v %v", closed, err)
	}
	if !connector.connections[0].closed {
		t.Fatal("connection was not closed")
	}
	_, err = service.Status(context.Background(), opened.SessionID)
	assertCode(t, err, "ssh_session_unknown")
}

func TestOpenRefusesDraftDisabledUnknownAndNotGranted(t *testing.T) {
	service, _, _ := testService(t, ModeRun)
	_, err := service.Open(context.Background(), "draft", time.Second)
	assertCode(t, err, "ssh_target_not_published")
	_, err = service.Open(context.Background(), "disabled", time.Second)
	assertCode(t, err, "ssh_target_disabled")
	_, err = service.Open(context.Background(), "missing", time.Second)
	assertCode(t, err, "ssh_target_unknown")

	config := DefaultConfig()
	config.AllowedTargets = map[string]struct{}{"another": {}}
	store := &MemoryTargetStore{SnapshotValue: TargetSnapshot{Targets: []Target{{ID: "prod", Published: true, Enabled: true}}}}
	limited, err := New(config, store, &MemoryCredentialProvider{}, &fakeConnector{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = limited.Open(context.Background(), "prod", time.Second)
	assertCode(t, err, "ssh_target_not_granted")
}

func TestCheckIsBuildOnlyAndUsesConnector(t *testing.T) {
	run, _, _ := testService(t, ModeRun)
	_, err := run.Check(context.Background(), "prod", time.Second)
	assertCode(t, err, "ssh_build_only")

	build, _, connector := testService(t, ModeBuild)
	result, err := build.Check(context.Background(), "prod", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reachable || !result.HostKeyVerified || len(connector.connections) != 1 || !connector.connections[0].closed {
		t.Fatalf("unexpected check result: %#v", result)
	}
}

func TestCredentialProviderUnavailableDegradesHealth(t *testing.T) {
	config := DefaultConfig()
	service, err := New(config, &MemoryTargetStore{SnapshotValue: TargetSnapshot{}}, UnavailableCredentialProvider{}, &fakeConnector{})
	if err != nil {
		t.Fatal(err)
	}
	health, err := service.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "degraded" || health.CredentialsProvider {
		t.Fatalf("unexpected health: %#v", health)
	}
}

func TestSessionExpiresAndClosesConnection(t *testing.T) {
	service, _, connector := testService(t, ModeRun)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	opened, err := service.Open(context.Background(), "prod", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	_, err = service.Status(context.Background(), opened.SessionID)
	assertCode(t, err, "ssh_session_unknown")
	if !connector.connections[0].closed {
		t.Fatal("expired connection was not closed")
	}
}

func TestExecValidatesArgumentsAndConfiguredOutputLimit(t *testing.T) {
	service, _, _ := testService(t, ModeRun)
	opened, err := service.Open(context.Background(), "prod", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Exec(context.Background(), opened.SessionID, "", "", time.Second, 1024)
	assertCode(t, err, "invalid_arguments")
	_, err = service.Exec(context.Background(), opened.SessionID, "true", "relative", time.Second, 1024)
	assertCode(t, err, "invalid_arguments")
	_, err = service.Exec(context.Background(), opened.SessionID, "true", "", time.Second, service.config.MaxOutputBytes+1)
	assertCode(t, err, "invalid_arguments")
}

func assertCode(t *testing.T, err error, expected string) {
	t.Helper()
	var serviceError *Error
	if !errors.As(err, &serviceError) || serviceError.Code != expected {
		t.Fatalf("expected %s, got %v", expected, err)
	}
}

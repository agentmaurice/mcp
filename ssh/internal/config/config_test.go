package config

import (
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/ssh/internal/sshservice"
)

func TestFromEnvDefaultsAndOverrides(t *testing.T) {
	t.Setenv("SSH_EXECUTION_MODE", "")
	t.Setenv("SSH_TARGETS_FILE", "")
	t.Setenv("SSH_CREDENTIALS_FILE", "")
	config, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Service.Mode != sshservice.ModeRun || config.TargetsFile != "/etc/agentmaurice/ssh-targets.json" {
		t.Fatalf("unexpected defaults: %#v", config)
	}

	t.Setenv("SSH_EXECUTION_MODE", "build")
	t.Setenv("SSH_ALLOWED_TARGETS", "prod, legacy ,prod")
	t.Setenv("SSH_CONNECT_TIMEOUT", "9s")
	t.Setenv("SSH_MAX_SESSIONS", "12")
	t.Setenv("SSH_MAX_SESSIONS_PER_TARGET", "3")
	config, err = FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Service.Mode != sshservice.ModeBuild || config.Service.MaxSessions != 12 || len(config.Service.AllowedTargets) != 2 {
		t.Fatalf("unexpected overrides: %#v", config.Service)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	t.Setenv("SSH_MAX_SESSIONS", "zero")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected invalid integer to fail")
	}
	t.Setenv("SSH_MAX_SESSIONS", "32")
	t.Setenv("SSH_EXECUTION_MODE", "unsafe")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected invalid mode to fail")
	}
}

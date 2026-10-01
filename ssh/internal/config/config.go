package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/ssh/internal/sshservice"
)

type Config struct {
	TargetsFile          string
	CredentialsFile      string
	VaultURL             string
	VaultConsumerPrivB64 string
	Service              sshservice.Config
}

func FromEnv() (Config, error) {
	service := sshservice.DefaultConfig()
	service.Mode = envOr("SSH_EXECUTION_MODE", sshservice.ModeRun)
	service.AllowedTargets = stringSet(os.Getenv("SSH_ALLOWED_TARGETS"))
	var err error
	if service.ConnectTimeout, err = durationEnv("SSH_CONNECT_TIMEOUT", service.ConnectTimeout); err != nil {
		return Config{}, err
	}
	if service.CommandTimeout, err = durationEnv("SSH_COMMAND_TIMEOUT", service.CommandTimeout); err != nil {
		return Config{}, err
	}
	if service.SessionIdleTTL, err = durationEnv("SSH_SESSION_IDLE_TTL", service.SessionIdleTTL); err != nil {
		return Config{}, err
	}
	if service.SessionAbsoluteTTL, err = durationEnv("SSH_SESSION_ABSOLUTE_TTL", service.SessionAbsoluteTTL); err != nil {
		return Config{}, err
	}
	if service.MaxCommandTimeout, err = durationEnv("SSH_MAX_COMMAND_TIMEOUT", service.MaxCommandTimeout); err != nil {
		return Config{}, err
	}
	if service.MaxSessions, err = intEnv("SSH_MAX_SESSIONS", service.MaxSessions); err != nil {
		return Config{}, err
	}
	if service.MaxSessionsPerTarget, err = intEnv("SSH_MAX_SESSIONS_PER_TARGET", service.MaxSessionsPerTarget); err != nil {
		return Config{}, err
	}
	if service.MaxOutputBytes, err = intEnv("SSH_MAX_OUTPUT_BYTES", service.MaxOutputBytes); err != nil {
		return Config{}, err
	}
	if err := service.Validate(); err != nil {
		return Config{}, err
	}
	return Config{
		TargetsFile:          envOr("SSH_TARGETS_FILE", "/etc/agentmaurice/ssh-targets.json"),
		CredentialsFile:      strings.TrimSpace(os.Getenv("SSH_CREDENTIALS_FILE")),
		VaultURL:             strings.TrimSpace(os.Getenv("SSH_VAULT_URL")),
		VaultConsumerPrivB64: strings.TrimSpace(os.Getenv("SSH_VAULT_CONSUMER_PRIV_B64")),
		Service:              service,
	}, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	return parsed, nil
}

func intEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func stringSet(value string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result[item] = struct{}{}
		}
	}
	return result
}

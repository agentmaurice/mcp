package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const DefaultSocketPath = "/run/agentmaurice/system.sock"

type Sidecar struct {
	SocketPath     string
	Actor          string
	OrganizationID string
	DeploymentID   string
}

type Host struct {
	SocketPath           string
	MutationMode         string
	AllowedServices      []string
	RuntimeRestartScript string
	MaxLogLines          int
	PlanTTL              time.Duration
	CommandTimeout       time.Duration
}

func SidecarFromEnv() Sidecar {
	return Sidecar{
		SocketPath:     envOr("SYSTEM_AGENT_SOCKET", DefaultSocketPath),
		Actor:          envOr("SYSTEM_ACTOR", "mcp-system"),
		OrganizationID: strings.TrimSpace(os.Getenv("SYSTEM_ORGANIZATION_ID")),
		DeploymentID:   strings.TrimSpace(os.Getenv("SYSTEM_DEPLOYMENT_ID")),
	}
}

func HostFromEnv() (Host, error) {
	maxLines, err := intEnv("SYSTEM_MAX_LOG_LINES", 500)
	if err != nil || maxLines < 1 || maxLines > 2000 {
		return Host{}, fmt.Errorf("SYSTEM_MAX_LOG_LINES must be between 1 and 2000")
	}
	planTTL, err := durationEnv("SYSTEM_PLAN_TTL", 5*time.Minute)
	if err != nil || planTTL < 10*time.Second || planTTL > 30*time.Minute {
		return Host{}, fmt.Errorf("SYSTEM_PLAN_TTL must be between 10s and 30m")
	}
	commandTimeout, err := durationEnv("SYSTEM_COMMAND_TIMEOUT", 30*time.Second)
	if err != nil || commandTimeout < time.Second || commandTimeout > 2*time.Minute {
		return Host{}, fmt.Errorf("SYSTEM_COMMAND_TIMEOUT must be between 1s and 2m")
	}
	mode := envOr("SYSTEM_MUTATION_MODE", "disabled")
	if mode != "disabled" && mode != "standing_grant" {
		return Host{}, fmt.Errorf("SYSTEM_MUTATION_MODE must be disabled or standing_grant")
	}
	socketPath := envOr("SYSTEM_AGENT_SOCKET", DefaultSocketPath)
	if !filepath.IsAbs(socketPath) {
		return Host{}, fmt.Errorf("SYSTEM_AGENT_SOCKET must be an absolute path")
	}
	script := strings.TrimSpace(os.Getenv("SYSTEM_RUNTIME_RESTART_SCRIPT"))
	if script != "" && !filepath.IsAbs(script) {
		return Host{}, fmt.Errorf("SYSTEM_RUNTIME_RESTART_SCRIPT must be an absolute path")
	}
	services, err := parseServices(os.Getenv("SYSTEM_ALLOWED_SERVICES"))
	if err != nil {
		return Host{}, err
	}
	return Host{
		SocketPath:           socketPath,
		MutationMode:         mode,
		AllowedServices:      services,
		RuntimeRestartScript: script,
		MaxLogLines:          maxLines,
		PlanTTL:              planTTL,
		CommandTimeout:       commandTimeout,
	}, nil
}

func parseServices(value string) ([]string, error) {
	seen := map[string]struct{}{}
	var result []string
	for _, raw := range strings.Split(value, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("_.@-", r) {
				return nil, fmt.Errorf("invalid service name %q", name)
			}
		}
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

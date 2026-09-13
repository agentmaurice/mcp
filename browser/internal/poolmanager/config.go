package poolmanager

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type ServiceConfig struct {
	Address           string
	AuthToken         string
	ReconcileInterval time.Duration
	DefaultPool       *PoolSpec
	DockerDefaults    DockerProviderConfig
	K8sDefaults       K8sProviderConfig
	KoyebDefaults     KoyebProviderConfig
}

type DockerProviderConfig struct {
	DockerHost   string            `json:"docker_host,omitempty"`
	Network      string            `json:"network,omitempty"`
	Image        string            `json:"image,omitempty"`
	CPULimit     string            `json:"cpu_limit,omitempty"`
	MemLimitMB   int               `json:"mem_limit_mb,omitempty"`
	EnvAllowlist []string          `json:"env_allowlist,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
}

type K8sProviderConfig struct {
	ClusterID        string            `json:"cluster_id,omitempty"`
	Namespace        string            `json:"namespace,omitempty"`
	Image            string            `json:"image,omitempty"`
	ServiceAccount   string            `json:"service_account,omitempty"`
	ResourceRequests map[string]string `json:"resource_requests,omitempty"`
	ResourceLimits   map[string]string `json:"resource_limits,omitempty"`
	NodeSelector     map[string]string `json:"node_selector,omitempty"`
	EnvAllowlist     []string          `json:"env_allowlist,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
}

type KoyebProviderConfig struct {
	APIURL            string            `json:"api_url,omitempty"`
	Token             string            `json:"token,omitempty"`
	OrganizationID    string            `json:"organization_id,omitempty"`
	ServiceNamePrefix string            `json:"service_name_prefix,omitempty"`
	Region            string            `json:"region,omitempty"`
	InstanceType      string            `json:"instance_type,omitempty"`
	Image             string            `json:"image,omitempty"`
	EnvAllowlist      []string          `json:"env_allowlist,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
}

func LoadServiceConfigFromEnv() (*ServiceConfig, error) {
	address := strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_ADDRESS"))
	if address == "" {
		address = ":8090"
	}

	reconcileInterval := 5 * time.Second
	if raw := strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_RECONCILE_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid BROWSER_POOL_MANAGER_RECONCILE_INTERVAL: %w", err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("BROWSER_POOL_MANAGER_RECONCILE_INTERVAL must be > 0")
		}
		reconcileInterval = parsed
	}

	cfg := &ServiceConfig{
		Address:           address,
		AuthToken:         strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_AUTH_TOKEN")),
		ReconcileInterval: reconcileInterval,
		DockerDefaults: DockerProviderConfig{
			DockerHost: strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_DOCKER_HOST")),
			Network:    strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_DOCKER_NETWORK")),
			Image:      strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_DOCKER_IMAGE")),
			CPULimit:   strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_DOCKER_CPU_LIMIT")),
			Env:        map[string]string{},
		},
		K8sDefaults: K8sProviderConfig{
			ClusterID:      strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_K8S_CLUSTER_ID")),
			Namespace:      strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_K8S_NAMESPACE")),
			Image:          strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_K8S_IMAGE")),
			ServiceAccount: strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_K8S_SERVICE_ACCOUNT")),
			Env:            map[string]string{},
		},
		KoyebDefaults: KoyebProviderConfig{
			APIURL:            strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_KOYEB_API_URL")),
			Token:             strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_KOYEB_TOKEN")),
			OrganizationID:    strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_KOYEB_ORG_ID")),
			ServiceNamePrefix: strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_KOYEB_SERVICE_PREFIX")),
			Region:            strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_KOYEB_REGION")),
			InstanceType:      strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_KOYEB_INSTANCE_TYPE")),
			Image:             strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_KOYEB_IMAGE")),
			Env:               map[string]string{},
		},
	}

	if cfg.K8sDefaults.Namespace == "" {
		cfg.K8sDefaults.Namespace = "default"
	}
	if cfg.KoyebDefaults.APIURL == "" {
		cfg.KoyebDefaults.APIURL = "https://app.koyeb.com"
	}
	if cfg.KoyebDefaults.Region == "" {
		cfg.KoyebDefaults.Region = "was"
	}
	if cfg.KoyebDefaults.ServiceNamePrefix == "" {
		cfg.KoyebDefaults.ServiceNamePrefix = "mcp-browser"
	}

	if raw := strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_DOCKER_MEM_LIMIT_MB")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid BROWSER_POOL_MANAGER_DOCKER_MEM_LIMIT_MB: %w", err)
		}
		cfg.DockerDefaults.MemLimitMB = parsed
	}
	cfg.DockerDefaults.EnvAllowlist = parseCSV(os.Getenv("BROWSER_POOL_MANAGER_DOCKER_ENV_ALLOWLIST"))
	cfg.K8sDefaults.EnvAllowlist = parseCSV(os.Getenv("BROWSER_POOL_MANAGER_K8S_ENV_ALLOWLIST"))
	cfg.KoyebDefaults.EnvAllowlist = parseCSV(os.Getenv("BROWSER_POOL_MANAGER_KOYEB_ENV_ALLOWLIST"))

	defaultPoolID := strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_DEFAULT_POOL_ID"))
	if defaultPoolID == "" {
		defaultPoolID = "default"
	}
	defaultProvider := strings.ToLower(strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_DEFAULT_PROVIDER")))
	if defaultProvider == "" {
		defaultProvider = "docker"
	}
	defaultMode := strings.ToLower(strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_DEFAULT_MODE")))
	if defaultMode == "" {
		defaultMode = "dynamic"
	}

	minWarm := parseIntWithDefault(os.Getenv("BROWSER_POOL_MANAGER_MIN_WARM_INSTANCES"), 1)
	maxInstances := parseIntWithDefault(os.Getenv("BROWSER_POOL_MANAGER_MAX_INSTANCES"), 8)
	maxSessions := parseIntWithDefault(os.Getenv("BROWSER_POOL_MANAGER_MAX_SESSIONS_PER_INSTANCE"), 5)
	scaleUpCooldown := parseDurationWithDefault(os.Getenv("BROWSER_POOL_MANAGER_SCALE_UP_COOLDOWN"), 5*time.Second)
	scaleDownCooldown := parseDurationWithDefault(os.Getenv("BROWSER_POOL_MANAGER_SCALE_DOWN_COOLDOWN"), 60*time.Second)
	idleTTL := parseDurationWithDefault(os.Getenv("BROWSER_POOL_MANAGER_SESSION_IDLE_TTL"), 300*time.Second)
	startupTimeout := parseDurationWithDefault(os.Getenv("BROWSER_POOL_MANAGER_STARTUP_TIMEOUT"), 90*time.Second)
	queueTimeout := parseDurationWithDefault(os.Getenv("BROWSER_POOL_MANAGER_QUEUE_TIMEOUT"), 20*time.Second)

	providerConfig := map[string]any{}
	if rawJSON := strings.TrimSpace(os.Getenv("BROWSER_POOL_MANAGER_DEFAULT_PROVIDER_CONFIG_JSON")); rawJSON != "" {
		if err := json.Unmarshal([]byte(rawJSON), &providerConfig); err != nil {
			return nil, fmt.Errorf("invalid BROWSER_POOL_MANAGER_DEFAULT_PROVIDER_CONFIG_JSON: %w", err)
		}
	}

	legacyEndpoints := parseCSV(os.Getenv("BROWSER_POOL_MANAGER_ENDPOINTS"))
	enableDefaultPool := parseBoolWithDefault(os.Getenv("BROWSER_POOL_MANAGER_ENABLE_DEFAULT_POOL"), false)
	if len(legacyEndpoints) > 0 {
		enableDefaultPool = true
		defaultProvider = "static"
		defaultMode = "static"
		providerConfig["endpoints"] = legacyEndpoints
	}

	if enableDefaultPool {
		spec := &PoolSpec{
			PoolID:                 defaultPoolID,
			Provider:               defaultProvider,
			Mode:                   defaultMode,
			MinWarmInstances:       minWarm,
			MaxInstances:           maxInstances,
			MaxSessionsPerInstance: maxSessions,
			ScaleUpCooldown:        scaleUpCooldown,
			ScaleDownCooldown:      scaleDownCooldown,
			IdleTTL:                idleTTL,
			StartupTimeout:         startupTimeout,
			QueueTimeout:           queueTimeout,
			ProviderConfig:         providerConfig,
		}
		if err := spec.Normalize(); err != nil {
			return nil, err
		}
		cfg.DefaultPool = spec
	}

	return cfg, nil
}

func parseDurationWithDefault(raw string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(v)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parseIntWithDefault(raw string, fallback int) int {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseBoolWithDefault(raw string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "t", "true", "y", "yes", "on":
		return true
	case "0", "f", "false", "n", "no", "off":
		return false
	default:
		return fallback
	}
}

func parseCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

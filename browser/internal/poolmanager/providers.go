package poolmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

type staticProvider struct{}

func newStaticProvider() Provider {
	return &staticProvider{}
}

func (p *staticProvider) Kind() string {
	return "static"
}

func (p *staticProvider) Reconcile(_ context.Context, _ string, desired int, spec PoolSpec) error {
	endpoints := providerConfigStringSlice(spec.ProviderConfig, "endpoints")
	if desired > len(endpoints) {
		return fmt.Errorf("static provider capacity exhausted: desired=%d available=%d", desired, len(endpoints))
	}
	return nil
}

func (p *staticProvider) ListInstances(_ context.Context, _ string, spec PoolSpec) ([]ProviderInstance, error) {
	endpoints := providerConfigStringSlice(spec.ProviderConfig, "endpoints")
	instances := make([]ProviderInstance, 0, len(endpoints))
	for i, endpoint := range endpoints {
		instances = append(instances, ProviderInstance{
			ID:        fmt.Sprintf("i%d", i+1),
			Endpoint:  endpoint,
			Ready:     strings.TrimSpace(endpoint) != "",
			CreatedAt: time.Now(),
			LastSeen:  time.Now(),
		})
	}
	return instances, nil
}

func (p *staticProvider) DeletePool(_ context.Context, _ string, _ PoolSpec) error {
	return nil
}

type dockerProvider struct {
	defaults DockerProviderConfig
	logger   *zap.Logger
}

type dockerProviderConfig struct {
	DockerHost   string
	Network      string
	Image        string
	CPULimit     string
	MemLimitMB   int
	EnvAllowlist []string
	Env          map[string]string
}

type dockerContainer struct {
	ID        string
	Name      string
	Endpoint  string
	Running   bool
	CreatedAt time.Time
}

func newDockerProvider(defaults DockerProviderConfig, logger *zap.Logger) Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
	if defaults.Env == nil {
		defaults.Env = map[string]string{}
	}
	return &dockerProvider{
		defaults: defaults,
		logger:   logger.Named("docker-provider"),
	}
}

func (p *dockerProvider) Kind() string {
	return "docker"
}

func (p *dockerProvider) Reconcile(ctx context.Context, poolID string, desired int, spec PoolSpec) error {
	cfg, err := p.effectiveConfig(spec)
	if err != nil {
		return err
	}
	if cfg.Image == "" {
		return fmt.Errorf("docker provider requires provider_config.image (or BROWSER_POOL_MANAGER_DOCKER_IMAGE)")
	}

	containers, err := p.listManagedContainers(ctx, cfg, poolID, true)
	if err != nil {
		return err
	}

	running := make([]dockerContainer, 0, len(containers))
	stopped := make([]dockerContainer, 0)
	for _, container := range containers {
		if container.Running {
			running = append(running, container)
		} else {
			stopped = append(stopped, container)
		}
	}

	for _, container := range stopped {
		if err := p.removeContainer(ctx, cfg, container.ID); err != nil {
			p.logger.Warn("failed to cleanup stopped container", zap.String("container_id", container.ID), zap.Error(err))
		}
	}

	if desired < 0 {
		desired = 0
	}

	if len(running) < desired {
		createCount := desired - len(running)
		for i := 0; i < createCount; i++ {
			if _, createErr := p.createContainer(ctx, cfg, poolID); createErr != nil {
				return createErr
			}
		}
		return nil
	}

	if len(running) > desired {
		sort.Slice(running, func(i, j int) bool {
			if running[i].CreatedAt.Equal(running[j].CreatedAt) {
				return running[i].ID < running[j].ID
			}
			return running[i].CreatedAt.Before(running[j].CreatedAt)
		})
		remove := running[desired:]
		for _, container := range remove {
			if err := p.removeContainer(ctx, cfg, container.ID); err != nil {
				return err
			}
		}
	}

	return nil
}

func (p *dockerProvider) ListInstances(ctx context.Context, poolID string, spec PoolSpec) ([]ProviderInstance, error) {
	cfg, err := p.effectiveConfig(spec)
	if err != nil {
		return nil, err
	}
	containers, err := p.listManagedContainers(ctx, cfg, poolID, false)
	if err != nil {
		return nil, err
	}

	instances := make([]ProviderInstance, 0, len(containers))
	now := time.Now()
	for _, container := range containers {
		instances = append(instances, ProviderInstance{
			ID:        container.ID,
			Endpoint:  container.Endpoint,
			Ready:     container.Running && strings.TrimSpace(container.Endpoint) != "",
			CreatedAt: container.CreatedAt,
			LastSeen:  now,
		})
	}
	return instances, nil
}

func (p *dockerProvider) DeletePool(ctx context.Context, poolID string, spec PoolSpec) error {
	cfg, err := p.effectiveConfig(spec)
	if err != nil {
		return err
	}
	containers, err := p.listManagedContainers(ctx, cfg, poolID, true)
	if err != nil {
		return err
	}
	for _, container := range containers {
		if err := p.removeContainer(ctx, cfg, container.ID); err != nil {
			return err
		}
	}
	return nil
}

func (p *dockerProvider) effectiveConfig(spec PoolSpec) (dockerProviderConfig, error) {
	cfg := dockerProviderConfig{
		DockerHost:   strings.TrimSpace(p.defaults.DockerHost),
		Network:      strings.TrimSpace(p.defaults.Network),
		Image:        strings.TrimSpace(p.defaults.Image),
		CPULimit:     strings.TrimSpace(p.defaults.CPULimit),
		MemLimitMB:   p.defaults.MemLimitMB,
		EnvAllowlist: append([]string{}, p.defaults.EnvAllowlist...),
		Env:          cloneStringMap(p.defaults.Env),
	}
	if spec.ProviderConfig == nil {
		return cfg, nil
	}
	if value := strings.TrimSpace(providerConfigString(spec.ProviderConfig, "docker_host")); value != "" {
		cfg.DockerHost = value
	}
	if value := strings.TrimSpace(providerConfigString(spec.ProviderConfig, "network")); value != "" {
		cfg.Network = value
	}
	if value := strings.TrimSpace(providerConfigString(spec.ProviderConfig, "image")); value != "" {
		cfg.Image = value
	}
	if value := strings.TrimSpace(providerConfigString(spec.ProviderConfig, "cpu_limit")); value != "" {
		cfg.CPULimit = value
	}
	if value := providerConfigInt(spec.ProviderConfig, "mem_limit_mb", 0); value > 0 {
		cfg.MemLimitMB = value
	}
	if allowlist := providerConfigStringSlice(spec.ProviderConfig, "env_allowlist"); len(allowlist) > 0 {
		cfg.EnvAllowlist = allowlist
	}
	if customEnv := providerConfigStringMap(spec.ProviderConfig, "env"); len(customEnv) > 0 {
		for key, value := range customEnv {
			cfg.Env[key] = value
		}
	}
	cfg.Env = applyEnvAllowlist(cfg.Env, cfg.EnvAllowlist)
	return cfg, nil
}

func (p *dockerProvider) listManagedContainers(ctx context.Context, cfg dockerProviderConfig, poolID string, includeStopped bool) ([]dockerContainer, error) {
	args := []string{"ps"}
	if includeStopped {
		args = append(args, "-a")
	}
	args = append(args,
		"--filter", "label=agentmaurice.managed_by=mcp-browser-pool",
		"--filter", "label=agentmaurice.pool_id="+poolID,
		"--format", "{{.ID}}",
	)
	out, err := runDockerCommand(ctx, cfg.DockerHost, nil, args...)
	if err != nil {
		return nil, err
	}
	ids := splitNonEmptyLines(out)
	if len(ids) == 0 {
		return []dockerContainer{}, nil
	}

	inspectArgs := append([]string{"inspect"}, ids...)
	inspectOut, err := runDockerCommand(ctx, cfg.DockerHost, nil, inspectArgs...)
	if err != nil {
		return nil, err
	}

	var inspected []struct {
		ID      string `json:"Id"`
		Name    string `json:"Name"`
		Created string `json:"Created"`
		State   struct {
			Running bool `json:"Running"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal([]byte(inspectOut), &inspected); err != nil {
		return nil, fmt.Errorf("decode docker inspect output: %w", err)
	}

	containers := make([]dockerContainer, 0, len(inspected))
	for _, item := range inspected {
		name := strings.TrimPrefix(strings.TrimSpace(item.Name), "/")
		endpoint := strings.TrimSpace(item.Config.Labels["agentmaurice.browser_endpoint"])
		if endpoint == "" {
			endpoint = fmt.Sprintf("ws://%s:9222", name)
		}
		createdAt := time.Time{}
		if parsed, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.Created)); parseErr == nil {
			createdAt = parsed
		}
		containers = append(containers, dockerContainer{
			ID:        strings.TrimSpace(item.ID),
			Name:      name,
			Endpoint:  endpoint,
			Running:   item.State.Running,
			CreatedAt: createdAt,
		})
	}
	return containers, nil
}

func (p *dockerProvider) createContainer(ctx context.Context, cfg dockerProviderConfig, poolID string) (string, error) {
	containerName := fmt.Sprintf("am-browser-%s-%d", sanitizeLabel(poolID), time.Now().UnixNano())
	endpoint := fmt.Sprintf("ws://%s:9222", containerName)
	args := []string{
		"run", "-d",
		"--name", containerName,
		"--label", "agentmaurice.managed_by=mcp-browser-pool",
		"--label", "agentmaurice.pool_id=" + poolID,
		"--label", "agentmaurice.browser_endpoint=" + endpoint,
	}
	if strings.TrimSpace(cfg.Network) != "" {
		args = append(args, "--network", cfg.Network)
	}
	if strings.TrimSpace(cfg.CPULimit) != "" {
		args = append(args, "--cpus", cfg.CPULimit)
	}
	if cfg.MemLimitMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", cfg.MemLimitMB))
	}

	envKeys := make([]string, 0, len(cfg.Env))
	for key := range cfg.Env {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		args = append(args, "-e", key+"="+cfg.Env[key])
	}

	args = append(args, cfg.Image)
	out, err := runDockerCommand(ctx, cfg.DockerHost, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (p *dockerProvider) removeContainer(ctx context.Context, cfg dockerProviderConfig, containerID string) error {
	if strings.TrimSpace(containerID) == "" {
		return nil
	}
	_, err := runDockerCommand(ctx, cfg.DockerHost, nil, "rm", "-f", containerID)
	return err
}

type k8sProvider struct {
	defaults K8sProviderConfig
	logger   *zap.Logger
}

type k8sProviderConfig struct {
	ClusterID        string
	Namespace        string
	Image            string
	ServiceAccount   string
	ResourceRequests map[string]string
	ResourceLimits   map[string]string
	NodeSelector     map[string]string
	EnvAllowlist     []string
	Env              map[string]string
}

func newK8sProvider(defaults K8sProviderConfig, logger *zap.Logger) Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
	if defaults.Namespace == "" {
		defaults.Namespace = "default"
	}
	if defaults.Env == nil {
		defaults.Env = map[string]string{}
	}
	return &k8sProvider{
		defaults: defaults,
		logger:   logger.Named("k8s-provider"),
	}
}

func (p *k8sProvider) Kind() string {
	return "k8s"
}

func (p *k8sProvider) Reconcile(ctx context.Context, poolID string, desired int, spec PoolSpec) error {
	cfg, err := p.effectiveConfig(spec)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Image) == "" {
		return fmt.Errorf("k8s provider requires provider_config.image")
	}
	if desired < 0 {
		desired = 0
	}
	deploymentName := "pool-" + sanitizeKubeName(poolID)
	labels := map[string]string{
		"app":                     deploymentName,
		"agentmaurice.pool_id":    poolID,
		"agentmaurice.managed_by": "mcp-browser-pool",
	}
	env := applyEnvAllowlist(cfg.Env, cfg.EnvAllowlist)
	envVars := make([]map[string]string, 0, len(env))
	envKeys := make([]string, 0, len(env))
	for key := range env {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		envVars = append(envVars, map[string]string{"name": key, "value": env[key]})
	}

	container := map[string]any{
		"name":  "browser",
		"image": cfg.Image,
		"ports": []map[string]int{{"containerPort": 9222}},
		"env":   envVars,
	}
	if len(cfg.ResourceRequests) > 0 || len(cfg.ResourceLimits) > 0 {
		container["resources"] = map[string]any{
			"requests": cfg.ResourceRequests,
			"limits":   cfg.ResourceLimits,
		}
	}

	podSpec := map[string]any{
		"containers": []map[string]any{container},
	}
	if cfg.ServiceAccount != "" {
		podSpec["serviceAccountName"] = cfg.ServiceAccount
	}
	if len(cfg.NodeSelector) > 0 {
		podSpec["nodeSelector"] = cfg.NodeSelector
	}

	manifest := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      deploymentName,
			"namespace": cfg.Namespace,
			"labels":    labels,
		},
		"spec": map[string]any{
			"replicas": desired,
			"selector": map[string]any{"matchLabels": labels},
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec":     podSpec,
			},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal k8s deployment manifest: %w", err)
	}
	_, err = runKubectl(ctx, cfg.ClusterID, cfg.Namespace, data, "apply", "-f", "-")
	return err
}

func (p *k8sProvider) ListInstances(ctx context.Context, poolID string, spec PoolSpec) ([]ProviderInstance, error) {
	cfg, err := p.effectiveConfig(spec)
	if err != nil {
		return nil, err
	}
	selector := "agentmaurice.pool_id=" + poolID + ",agentmaurice.managed_by=mcp-browser-pool"
	out, err := runKubectl(ctx, cfg.ClusterID, cfg.Namespace, nil, "get", "pods", "-l", selector, "-o", "json")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				CreationTimestamp string `json:"creationTimestamp"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
				PodIP string `json:"podIP"`
				Start string `json:"startTime"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return nil, fmt.Errorf("decode kubernetes pods payload: %w", err)
	}

	now := time.Now()
	instances := make([]ProviderInstance, 0, len(payload.Items))
	for _, item := range payload.Items {
		ready := strings.EqualFold(strings.TrimSpace(item.Status.Phase), "Running") && strings.TrimSpace(item.Status.PodIP) != ""
		endpoint := ""
		if strings.TrimSpace(item.Status.PodIP) != "" {
			endpoint = fmt.Sprintf("ws://%s:9222", item.Status.PodIP)
		}
		createdAt := parseRFC3339(item.Metadata.CreationTimestamp)
		instances = append(instances, ProviderInstance{
			ID:        strings.TrimSpace(item.Metadata.Name),
			Endpoint:  endpoint,
			Ready:     ready,
			CreatedAt: createdAt,
			LastSeen:  now,
		})
	}
	return instances, nil
}

func (p *k8sProvider) DeletePool(ctx context.Context, poolID string, spec PoolSpec) error {
	cfg, err := p.effectiveConfig(spec)
	if err != nil {
		return err
	}
	deploymentName := "pool-" + sanitizeKubeName(poolID)
	_, err = runKubectl(ctx, cfg.ClusterID, cfg.Namespace, nil, "delete", "deployment", deploymentName, "--ignore-not-found=true")
	return err
}

func (p *k8sProvider) effectiveConfig(spec PoolSpec) (k8sProviderConfig, error) {
	cfg := k8sProviderConfig{
		ClusterID:        strings.TrimSpace(p.defaults.ClusterID),
		Namespace:        strings.TrimSpace(p.defaults.Namespace),
		Image:            strings.TrimSpace(p.defaults.Image),
		ServiceAccount:   strings.TrimSpace(p.defaults.ServiceAccount),
		ResourceRequests: cloneStringMap(p.defaults.ResourceRequests),
		ResourceLimits:   cloneStringMap(p.defaults.ResourceLimits),
		NodeSelector:     cloneStringMap(p.defaults.NodeSelector),
		EnvAllowlist:     append([]string{}, p.defaults.EnvAllowlist...),
		Env:              cloneStringMap(p.defaults.Env),
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if spec.ProviderConfig == nil {
		return cfg, nil
	}
	if value := strings.TrimSpace(providerConfigString(spec.ProviderConfig, "cluster_id")); value != "" {
		cfg.ClusterID = value
	}
	if value := strings.TrimSpace(providerConfigString(spec.ProviderConfig, "namespace")); value != "" {
		cfg.Namespace = value
	}
	if value := strings.TrimSpace(providerConfigString(spec.ProviderConfig, "image")); value != "" {
		cfg.Image = value
	}
	if value := strings.TrimSpace(providerConfigString(spec.ProviderConfig, "service_account")); value != "" {
		cfg.ServiceAccount = value
	}
	if requests := providerConfigStringMap(spec.ProviderConfig, "resource_requests"); len(requests) > 0 {
		cfg.ResourceRequests = requests
	}
	if limits := providerConfigStringMap(spec.ProviderConfig, "resource_limits"); len(limits) > 0 {
		cfg.ResourceLimits = limits
	}
	if selector := providerConfigStringMap(spec.ProviderConfig, "node_selector"); len(selector) > 0 {
		cfg.NodeSelector = selector
	}
	if allowlist := providerConfigStringSlice(spec.ProviderConfig, "env_allowlist"); len(allowlist) > 0 {
		cfg.EnvAllowlist = allowlist
	}
	if env := providerConfigStringMap(spec.ProviderConfig, "env"); len(env) > 0 {
		for key, value := range env {
			cfg.Env[key] = value
		}
	}
	return cfg, nil
}

type koyebProvider struct {
	defaults KoyebProviderConfig
	logger   *zap.Logger
}

func newKoyebProvider(defaults KoyebProviderConfig, logger *zap.Logger) Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
	if defaults.Env == nil {
		defaults.Env = map[string]string{}
	}
	return &koyebProvider{
		defaults: defaults,
		logger:   logger.Named("koyeb-provider"),
	}
}

func (p *koyebProvider) Kind() string {
	return "koyeb"
}

func (p *koyebProvider) Reconcile(_ context.Context, _ string, desired int, spec PoolSpec) error {
	endpoints := providerConfigStringSlice(spec.ProviderConfig, "endpoints")
	if len(endpoints) == 0 {
		return fmt.Errorf("koyeb provider requires provider_config.endpoints in this version")
	}
	if desired > len(endpoints) {
		return fmt.Errorf("koyeb provider static endpoints exhausted: desired=%d available=%d", desired, len(endpoints))
	}
	return nil
}

func (p *koyebProvider) ListInstances(_ context.Context, _ string, spec PoolSpec) ([]ProviderInstance, error) {
	endpoints := providerConfigStringSlice(spec.ProviderConfig, "endpoints")
	now := time.Now()
	instances := make([]ProviderInstance, 0, len(endpoints))
	for i, endpoint := range endpoints {
		instances = append(instances, ProviderInstance{
			ID:        fmt.Sprintf("koyeb-%d", i+1),
			Endpoint:  endpoint,
			Ready:     strings.TrimSpace(endpoint) != "",
			CreatedAt: now,
			LastSeen:  now,
		})
	}
	return instances, nil
}

func (p *koyebProvider) DeletePool(_ context.Context, _ string, _ PoolSpec) error {
	return nil
}

func runDockerCommand(ctx context.Context, dockerHost string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if strings.TrimSpace(dockerHost) != "" {
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+strings.TrimSpace(dockerHost))
	}
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s failed: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runKubectl(ctx context.Context, clusterID, namespace string, stdin []byte, args ...string) (string, error) {
	finalArgs := make([]string, 0, len(args)+4)
	if strings.TrimSpace(clusterID) != "" {
		finalArgs = append(finalArgs, "--context", strings.TrimSpace(clusterID))
	}
	if strings.TrimSpace(namespace) != "" {
		finalArgs = append(finalArgs, "-n", strings.TrimSpace(namespace))
	}
	finalArgs = append(finalArgs, args...)

	cmd := exec.CommandContext(ctx, "kubectl", finalArgs...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("kubectl %s failed: %w (stderr: %s)", strings.Join(finalArgs, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func splitNonEmptyLines(raw string) []string {
	parts := strings.Split(raw, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func providerConfigString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", raw))
}

func providerConfigInt(cfg map[string]any, key string, fallback int) int {
	if cfg == nil {
		return fallback
	}
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func providerConfigStringSlice(cfg map[string]any, key string) []string {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		out := make([]string, 0, len(values))
		for _, value := range values {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			trimmed := strings.TrimSpace(fmt.Sprintf("%v", value))
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case string:
		return parseCSV(values)
	default:
		return nil
	}
}

func providerConfigStringMap(cfg map[string]any, key string) map[string]string {
	if cfg == nil {
		return nil
	}
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return nil
	}
	result := map[string]string{}
	switch values := raw.(type) {
	case map[string]string:
		for k, v := range values {
			key := strings.TrimSpace(k)
			if key == "" {
				continue
			}
			result[key] = strings.TrimSpace(v)
		}
	case map[string]any:
		for k, v := range values {
			key := strings.TrimSpace(k)
			if key == "" {
				continue
			}
			result[key] = strings.TrimSpace(fmt.Sprintf("%v", v))
		}
	default:
		return nil
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func applyEnvAllowlist(env map[string]string, allowlist []string) map[string]string {
	if len(env) == 0 {
		return map[string]string{}
	}
	if len(allowlist) == 0 {
		return cloneStringMap(env)
	}
	allowed := make(map[string]struct{}, len(allowlist))
	for _, key := range allowlist {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}
	out := map[string]string{}
	for key, value := range env {
		if _, ok := allowed[key]; !ok {
			continue
		}
		out[key] = value
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

var labelSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitizeLabel(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	lower = labelSanitizer.ReplaceAllString(lower, "-")
	lower = strings.Trim(lower, "-")
	if lower == "" {
		return "pool"
	}
	if len(lower) > 40 {
		return lower[:40]
	}
	return lower
}

var kubeNameSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitizeKubeName(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	v = kubeNameSanitizer.ReplaceAllString(v, "-")
	v = strings.Trim(v, "-")
	if v == "" {
		v = "pool"
	}
	if len(v) > 52 {
		v = v[:52]
	}
	return v
}

func parseRFC3339(raw string) time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed
	}
	return time.Time{}
}

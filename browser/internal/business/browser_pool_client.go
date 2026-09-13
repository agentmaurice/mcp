package business

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/browser"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

type poolInstanceStatus struct {
	ID             string `json:"id"`
	Endpoint       string `json:"endpoint"`
	ActiveSessions int    `json:"active_sessions"`
	Connected      bool   `json:"connected"`
}

type poolStatus struct {
	Mode             string               `json:"mode"`
	SelectionPolicy  string               `json:"selection_policy"`
	TotalInstances   int                  `json:"total_instances"`
	ConnectedCount   int                  `json:"connected_instances"`
	ActiveSessions   int                  `json:"active_sessions"`
	Waiters          int                  `json:"waiters"`
	MaxQueueDepth    int                  `json:"max_queue_depth"`
	AcquireTimeoutMS int64                `json:"acquire_timeout_ms"`
	Instances        []poolInstanceStatus `json:"instances"`
}

type poolInstance struct {
	id             string
	endpoint       string
	client         *browser.Client
	activeSessions int
}

type sessionBinding struct {
	instanceID string
	lastUsed   time.Time
}

type browserPoolClient struct {
	logger             *zap.Logger
	baseConfig         *config.BrowserConfig
	mode               string
	manager            *poolManagerClient
	selectionPolicy    string
	maxSessionsPerInst int
	sessionIdleTTL     time.Duration
	acquireTimeout     time.Duration
	maxQueueDepth      int
	instances          map[string]*poolInstance
	instanceOrder      []string
	sessions           map[string]*sessionBinding
	roundRobinCursor   int
	waiters            int
	onSessionEvictedFn func(string)
	janitorStop        chan struct{}
	janitorDone        chan struct{}
	mu                 sync.Mutex
}

func newBrowserPoolClient(cfg *config.BrowserConfig, logger *zap.Logger) (*browserPoolClient, error) {
	endpoints, mode, err := resolvePoolEndpoints(cfg)
	if err != nil {
		return nil, err
	}

	poolLogger := logger.Named("browser-pool")
	cfgCopy := *cfg
	instances := make(map[string]*poolInstance, len(endpoints))
	instanceOrder := make([]string, 0, len(endpoints))

	for i, endpoint := range endpoints {
		instanceID := fmt.Sprintf("i%d", i+1)
		instances[instanceID] = newPoolInstance(instanceID, endpoint, cfg, poolLogger)
		instanceOrder = append(instanceOrder, instanceID)
	}

	pool := &browserPoolClient{
		logger:             poolLogger,
		baseConfig:         &cfgCopy,
		mode:               mode,
		selectionPolicy:    normalizeSelectionPolicy(cfg.SelectionPolicy),
		maxSessionsPerInst: cfg.MaxSessionsPerInst,
		sessionIdleTTL:     cfg.SessionIdleTTL,
		acquireTimeout:     cfg.AcquireTimeout,
		maxQueueDepth:      cfg.MaxQueueDepth,
		instances:          instances,
		instanceOrder:      instanceOrder,
		sessions:           make(map[string]*sessionBinding),
	}

	if mode == "external" && strings.TrimSpace(cfg.PoolManagerURL) != "" {
		pool.manager = newPoolManagerClient(
			cfg.PoolManagerURL,
			cfg.PoolManagerToken,
			cfg.PoolClientID,
			cfg.PoolID,
			cfg.PoolManagerTimeout,
			poolLogger,
		)
		pool.logger.Info("using dedicated external pool-manager",
			zap.String("pool_manager_url", strings.TrimSpace(cfg.PoolManagerURL)))
	}

	pool.startJanitor()
	return pool, nil
}

func newPoolInstance(instanceID, endpoint string, cfg *config.BrowserConfig, logger *zap.Logger) *poolInstance {
	instanceCfg := *cfg
	instanceCfg.CDPEndpoint = endpoint
	instanceLogger := logger.With(zap.String("instance_id", instanceID), zap.String("cdp_endpoint", endpoint))
	return &poolInstance{
		id:       instanceID,
		endpoint: endpoint,
		client:   browser.NewClient(&instanceCfg, instanceLogger),
	}
}

func resolvePoolEndpoints(cfg *config.BrowserConfig) ([]string, string, error) {
	mode := normalizePoolMode(cfg.PoolMode)
	switch mode {
	case "single":
		if strings.TrimSpace(cfg.CDPEndpoint) == "" {
			return nil, mode, fmt.Errorf("browser.cdp_endpoint is required")
		}
		return []string{strings.TrimSpace(cfg.CDPEndpoint)}, mode, nil
	case "internal":
		return deriveInternalEndpoints(cfg.CDPEndpoint, cfg.PoolSize)
	case "external":
		if strings.TrimSpace(cfg.PoolManagerURL) != "" {
			return []string{}, mode, nil
		}
		if len(cfg.PoolEndpoints) == 0 {
			return nil, mode, fmt.Errorf("browser.pool_manager_url or browser.pool_endpoints is required when browser.pool_mode=external")
		}
		cleaned := uniqueNonEmpty(cfg.PoolEndpoints)
		if len(cleaned) == 0 {
			return nil, mode, fmt.Errorf("browser.pool_endpoints must contain at least one endpoint")
		}
		return cleaned, mode, nil
	default:
		return nil, mode, fmt.Errorf("unsupported browser.pool_mode=%s", mode)
	}
}

func deriveInternalEndpoints(baseEndpoint string, poolSize int) ([]string, string, error) {
	mode := "internal"
	baseEndpoint = strings.TrimSpace(baseEndpoint)
	if baseEndpoint == "" {
		return nil, mode, fmt.Errorf("browser.cdp_endpoint is required for internal pool mode")
	}
	if poolSize <= 1 {
		return []string{baseEndpoint}, mode, nil
	}

	scheme, host, port, path, err := parseEndpoint(baseEndpoint)
	if err != nil {
		return nil, mode, fmt.Errorf("invalid browser.cdp_endpoint for internal pool mode: %w", err)
	}

	endpoints := make([]string, 0, poolSize)
	for i := 0; i < poolSize; i++ {
		endpoints = append(endpoints, fmt.Sprintf("%s://%s:%d%s", scheme, host, port+i, path))
	}
	return endpoints, mode, nil
}

func parseEndpoint(endpoint string) (scheme, host string, port int, path string, err error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return "", "", 0, "", fmt.Errorf("empty endpoint")
	}
	if !strings.Contains(raw, "://") {
		raw = "ws://" + raw
	}
	u, parseErr := url.Parse(raw)
	if parseErr != nil {
		return "", "", 0, "", parseErr
	}
	if u.Scheme == "" {
		u.Scheme = "ws"
	}
	parsedHost, parsedPort, splitErr := net.SplitHostPort(u.Host)
	if splitErr != nil {
		return "", "", 0, "", splitErr
	}
	p, atoiErr := strconv.Atoi(parsedPort)
	if atoiErr != nil {
		return "", "", 0, "", atoiErr
	}
	if p <= 0 {
		return "", "", 0, "", fmt.Errorf("invalid port %d", p)
	}
	if u.Path == "" {
		u.Path = ""
	}
	return u.Scheme, parsedHost, p, u.Path, nil
}

func normalizePoolMode(mode string) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "internal", "external":
		return normalized
	default:
		return "single"
	}
}

func normalizeSelectionPolicy(policy string) string {
	normalized := strings.ToLower(strings.TrimSpace(policy))
	if normalized == "round_robin" {
		return normalized
	}
	return "least_loaded"
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func (p *browserPoolClient) SetSessionEvictedHook(fn func(string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onSessionEvictedFn = fn
}

func (p *browserPoolClient) startJanitor() {
	if p.manager != nil {
		return
	}
	if p.sessionIdleTTL <= 0 {
		return
	}

	interval := p.sessionIdleTTL / 2
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}

	p.janitorStop = make(chan struct{})
	p.janitorDone = make(chan struct{})

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(p.janitorDone)

		for {
			select {
			case <-ticker.C:
				p.evictExpiredSessions(time.Now())
			case <-p.janitorStop:
				return
			}
		}
	}()
}

func (p *browserPoolClient) stopJanitor() {
	p.mu.Lock()
	stop := p.janitorStop
	done := p.janitorDone
	p.janitorStop = nil
	p.janitorDone = nil
	p.mu.Unlock()

	if stop == nil || done == nil {
		return
	}
	close(stop)
	<-done
}

func (p *browserPoolClient) evictExpiredSessions(now time.Time) {
	if p.sessionIdleTTL <= 0 {
		return
	}

	p.mu.Lock()
	evicted := p.evictExpiredLocked(now)
	p.mu.Unlock()

	p.invokeEvictHook(evicted)
}

func (p *browserPoolClient) evictExpiredLocked(now time.Time) []string {
	if p.sessionIdleTTL <= 0 {
		return nil
	}

	evicted := make([]string, 0)
	for sessionKey, binding := range p.sessions {
		if now.Sub(binding.lastUsed) < p.sessionIdleTTL {
			continue
		}
		if inst, ok := p.instances[binding.instanceID]; ok && inst.activeSessions > 0 {
			inst.activeSessions--
		}
		delete(p.sessions, sessionKey)
		evicted = append(evicted, sessionKey)
	}
	return evicted
}

func (p *browserPoolClient) invokeEvictHook(sessions []string) {
	if len(sessions) == 0 {
		return
	}

	p.mu.Lock()
	hook := p.onSessionEvictedFn
	p.mu.Unlock()
	if hook == nil {
		return
	}

	for _, sessionKey := range sessions {
		hook(sessionKey)
	}
}

func (p *browserPoolClient) Acquire(ctx context.Context, sessionKey string) (*browser.Client, string, error) {
	sessionKey = normalizeSessionKey(sessionKey)
	if p.manager != nil {
		return p.acquireFromManager(ctx, sessionKey)
	}
	acquireCtx, cancel := withAcquireTimeout(ctx, p.acquireTimeout)
	defer cancel()

	waiting := false
	defer func() {
		if waiting {
			p.mu.Lock()
			if p.waiters > 0 {
				p.waiters--
			}
			p.mu.Unlock()
		}
	}()

	for {
		client, instanceID, err := p.tryAcquire(acquireCtx, sessionKey)
		if err == nil {
			return client, instanceID, nil
		}
		if err != errPoolExhausted {
			return nil, "", err
		}

		if !waiting {
			p.mu.Lock()
			if p.maxQueueDepth > 0 && p.waiters >= p.maxQueueDepth {
				p.mu.Unlock()
				return nil, "", shared.ErrServiceUnavailable("browser-pool queue")
			}
			p.waiters++
			p.mu.Unlock()
			waiting = true
		}

		select {
		case <-acquireCtx.Done():
			if ctx != nil && ctx.Err() != nil {
				return nil, "", shared.ErrTimeout("browser session acquisition cancelled")
			}
			return nil, "", shared.ErrServiceUnavailable("browser-pool capacity exhausted")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (p *browserPoolClient) acquireFromManager(ctx context.Context, sessionKey string) (*browser.Client, string, error) {
	acquireCtx, cancel := withAcquireTimeout(ctx, p.acquireTimeout)
	defer cancel()

	lease, err := p.manager.Acquire(acquireCtx, sessionKey)
	if err != nil {
		p.logger.Warn("pool-manager acquire failed", zap.Error(err), zap.String("session_key", sessionKey))
		return nil, "", shared.ErrServiceUnavailable("browser-pool manager")
	}

	instanceID := strings.TrimSpace(lease.InstanceID)
	if instanceID == "" {
		instanceID = strings.TrimSpace(lease.Endpoint)
	}
	endpoint := strings.TrimSpace(lease.Endpoint)
	if endpoint == "" {
		return nil, "", shared.ErrServiceUnavailable("browser-pool manager")
	}

	inst := p.ensureDynamicInstance(instanceID, endpoint)
	return inst.client, inst.id, nil
}

var errPoolExhausted = fmt.Errorf("browser pool exhausted")

func (p *browserPoolClient) tryAcquire(ctx context.Context, sessionKey string) (*browser.Client, string, error) {
	now := time.Now()

	p.mu.Lock()
	evicted := p.evictExpiredLocked(now)
	if binding, ok := p.sessions[sessionKey]; ok {
		binding.lastUsed = now
		if inst, exists := p.instances[binding.instanceID]; exists {
			client := inst.client
			instanceID := inst.id
			p.mu.Unlock()
			p.invokeEvictHook(evicted)
			return client, instanceID, nil
		}
		delete(p.sessions, sessionKey)
	}

	inst := p.selectInstanceLocked()
	if inst == nil {
		p.mu.Unlock()
		p.invokeEvictHook(evicted)
		return nil, "", errPoolExhausted
	}

	inst.activeSessions++
	p.sessions[sessionKey] = &sessionBinding{instanceID: inst.id, lastUsed: now}
	client := inst.client
	instanceID := inst.id
	p.mu.Unlock()

	p.invokeEvictHook(evicted)
	return client, instanceID, nil
}

func (p *browserPoolClient) selectInstanceLocked() *poolInstance {
	if len(p.instanceOrder) == 0 {
		return nil
	}

	if p.selectionPolicy == "round_robin" {
		for i := 0; i < len(p.instanceOrder); i++ {
			idx := (p.roundRobinCursor + i) % len(p.instanceOrder)
			inst := p.instances[p.instanceOrder[idx]]
			if inst == nil {
				continue
			}
			if p.maxSessionsPerInst > 0 && inst.activeSessions >= p.maxSessionsPerInst {
				continue
			}
			p.roundRobinCursor = (idx + 1) % len(p.instanceOrder)
			return inst
		}
		return nil
	}

	var selected *poolInstance
	for _, id := range p.instanceOrder {
		inst := p.instances[id]
		if inst == nil {
			continue
		}
		if p.maxSessionsPerInst > 0 && inst.activeSessions >= p.maxSessionsPerInst {
			continue
		}
		if selected == nil || inst.activeSessions < selected.activeSessions {
			selected = inst
		}
	}
	return selected
}

func (p *browserPoolClient) ensureDynamicInstance(instanceID, endpoint string) *poolInstance {
	p.mu.Lock()
	defer p.mu.Unlock()

	if instanceID == "" {
		instanceID = endpoint
	}

	if existing, ok := p.instances[instanceID]; ok {
		if existing.endpoint == endpoint {
			return existing
		}
		// Endpoint moved for this instance identifier, recreate client.
		instance := newPoolInstance(instanceID, endpoint, p.baseConfig, p.logger)
		_ = existing.client.Disconnect()
		existing.endpoint = instance.endpoint
		existing.client = instance.client
		return existing
	}

	instance := newPoolInstance(instanceID, endpoint, p.baseConfig, p.logger)
	p.instances[instanceID] = instance
	p.instanceOrder = append(p.instanceOrder, instanceID)
	return instance
}

func withAcquireTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline || timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func normalizeSessionKey(sessionKey string) string {
	trimmed := strings.TrimSpace(sessionKey)
	if trimmed == "" {
		return shared.DefaultSessionKey
	}
	return trimmed
}

func (p *browserPoolClient) clientForContext(ctx context.Context) (*browser.Client, string, error) {
	sessionKey := shared.SessionKeyFromContext(ctx)
	client, _, err := p.Acquire(ctx, sessionKey)
	if err != nil {
		return nil, sessionKey, err
	}
	return client, sessionKey, nil
}

func (p *browserPoolClient) Reconnect(ctx context.Context) error {
	client, sessionKey, err := p.clientForContext(ctx)
	if err != nil {
		return err
	}
	// Always tear down first: a CDP session can look "connected" while the
	// remote target is already dead (e.g. page load error Shutdown).
	if err := client.Disconnect(); err != nil {
		return err
	}
	connectCtx := shared.WithSessionKey(ctx, sessionKey)
	if connectCtx == nil {
		connectCtx = context.Background()
	}
	return client.Connect(connectCtx)
}

func (p *browserPoolClient) Connect(ctx context.Context) error {
	client, sessionKey, err := p.clientForContext(ctx)
	if err != nil {
		return err
	}
	connectCtx := shared.WithSessionKey(ctx, sessionKey)
	if connectCtx == nil {
		connectCtx = context.Background()
	}
	return client.Connect(connectCtx)
}

func (p *browserPoolClient) Disconnect() error {
	p.stopJanitor()

	p.mu.Lock()
	instances := make([]*poolInstance, 0, len(p.instances))
	for _, id := range p.instanceOrder {
		if inst, ok := p.instances[id]; ok {
			instances = append(instances, inst)
			inst.activeSessions = 0
		}
	}
	p.sessions = make(map[string]*sessionBinding)
	p.waiters = 0
	p.mu.Unlock()

	var firstErr error
	for _, inst := range instances {
		if err := inst.client.Disconnect(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (p *browserPoolClient) IsConnected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range p.instanceOrder {
		if inst, ok := p.instances[id]; ok && inst.client.IsConnected() {
			return true
		}
	}
	return false
}

func (p *browserPoolClient) PoolStatus() poolStatus {
	if p.manager != nil {
		return p.poolStatusFromManager()
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	status := poolStatus{
		Mode:             p.mode,
		SelectionPolicy:  p.selectionPolicy,
		TotalInstances:   len(p.instanceOrder),
		ActiveSessions:   len(p.sessions),
		Waiters:          p.waiters,
		MaxQueueDepth:    p.maxQueueDepth,
		AcquireTimeoutMS: p.acquireTimeout.Milliseconds(),
		Instances:        make([]poolInstanceStatus, 0, len(p.instanceOrder)),
	}

	for _, id := range p.instanceOrder {
		inst := p.instances[id]
		if inst == nil {
			continue
		}
		connected := inst.client.IsConnected()
		if connected {
			status.ConnectedCount++
		}
		status.Instances = append(status.Instances, poolInstanceStatus{
			ID:             id,
			Endpoint:       inst.endpoint,
			ActiveSessions: inst.activeSessions,
			Connected:      connected,
		})
	}

	sort.Slice(status.Instances, func(i, j int) bool {
		return status.Instances[i].ID < status.Instances[j].ID
	})

	return status
}

func (p *browserPoolClient) poolStatusFromManager() poolStatus {
	status := poolStatus{
		Mode:             p.mode,
		SelectionPolicy:  p.selectionPolicy,
		MaxQueueDepth:    p.maxQueueDepth,
		AcquireTimeoutMS: p.acquireTimeout.Milliseconds(),
		Instances:        []poolInstanceStatus{},
	}

	managerStatus, err := p.manager.Status(context.Background())
	if err != nil {
		p.logger.Warn("pool-manager status unavailable", zap.Error(err))
		return status
	}

	status.Mode = managerStatus.Mode
	status.SelectionPolicy = managerStatus.SelectionPolicy
	status.TotalInstances = managerStatus.TotalInstances
	status.ActiveSessions = managerStatus.ActiveSessions
	status.Instances = append(status.Instances, managerStatus.Instances...)
	for _, inst := range status.Instances {
		if inst.Connected {
			status.ConnectedCount++
		}
	}
	sort.Slice(status.Instances, func(i, j int) bool {
		return status.Instances[i].ID < status.Instances[j].ID
	})
	return status
}

func (p *browserPoolClient) withClient(ctx context.Context, action func(*browser.Client) error) error {
	client, _, err := p.clientForContext(ctx)
	if err != nil {
		return err
	}
	return action(client)
}

func withClientResult[T any](ctx context.Context, p *browserPoolClient, action func(*browser.Client) (T, error)) (T, error) {
	var zero T
	client, _, err := p.clientForContext(ctx)
	if err != nil {
		return zero, err
	}
	return action(client)
}

func (p *browserPoolClient) Navigate(ctx context.Context, req shared.NavigateRequest) (*shared.NavigateResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.NavigateResult, error) {
		return client.Navigate(ctx, req)
	})
}

func (p *browserPoolClient) GoBack(ctx context.Context) error {
	return p.withClient(ctx, func(client *browser.Client) error {
		return client.GoBack(ctx)
	})
}

func (p *browserPoolClient) GoForward(ctx context.Context) error {
	return p.withClient(ctx, func(client *browser.Client) error {
		return client.GoForward(ctx)
	})
}

func (p *browserPoolClient) Reload(ctx context.Context) error {
	return p.withClient(ctx, func(client *browser.Client) error {
		return client.Reload(ctx)
	})
}

func (p *browserPoolClient) GetPageInfo(ctx context.Context) (*shared.PageInfo, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.PageInfo, error) {
		return client.GetPageInfo(ctx)
	})
}

func (p *browserPoolClient) GetTitle(ctx context.Context) (string, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (string, error) {
		return client.GetTitle(ctx)
	})
}

func (p *browserPoolClient) GetURL(ctx context.Context) (string, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (string, error) {
		return client.GetURL(ctx)
	})
}

func (p *browserPoolClient) Click(ctx context.Context, req shared.ClickRequest) (*shared.ClickResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.ClickResult, error) {
		return client.Click(ctx, req)
	})
}

func (p *browserPoolClient) Fill(ctx context.Context, req shared.FillRequest) (*shared.FillResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.FillResult, error) {
		return client.Fill(ctx, req)
	})
}

func (p *browserPoolClient) SelectOption(ctx context.Context, req shared.SelectOptionRequest) (*shared.SelectOptionResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.SelectOptionResult, error) {
		return client.SelectOption(ctx, req)
	})
}

func (p *browserPoolClient) Scroll(ctx context.Context, req shared.ScrollRequest) (*shared.ScrollResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.ScrollResult, error) {
		return client.Scroll(ctx, req)
	})
}

func (p *browserPoolClient) GetText(ctx context.Context, req shared.GetTextRequest) (*shared.GetTextResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.GetTextResult, error) {
		return client.GetText(ctx, req)
	})
}

func (p *browserPoolClient) GetHTML(ctx context.Context, req shared.GetHTMLRequest) (*shared.GetHTMLResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.GetHTMLResult, error) {
		return client.GetHTML(ctx, req)
	})
}

func (p *browserPoolClient) GetAttribute(ctx context.Context, req shared.GetAttributeRequest) (*shared.GetAttributeResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.GetAttributeResult, error) {
		return client.GetAttribute(ctx, req)
	})
}

func (p *browserPoolClient) Screenshot(ctx context.Context, req shared.ScreenshotRequest) (*shared.ScreenshotResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.ScreenshotResult, error) {
		return client.Screenshot(ctx, req)
	})
}

func (p *browserPoolClient) Evaluate(ctx context.Context, req shared.EvaluateRequest) (*shared.EvaluateResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.EvaluateResult, error) {
		return client.Evaluate(ctx, req)
	})
}

func (p *browserPoolClient) WaitForSelector(ctx context.Context, req shared.WaitForSelectorRequest) (*shared.WaitForSelectorResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.WaitForSelectorResult, error) {
		return client.WaitForSelector(ctx, req)
	})
}

func (p *browserPoolClient) WaitForNavigation(ctx context.Context, timeout int) error {
	return p.withClient(ctx, func(client *browser.Client) error {
		return client.WaitForNavigation(ctx, timeout)
	})
}

func (p *browserPoolClient) Snapshot(ctx context.Context, req shared.SnapshotRequest) (*shared.SnapshotResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.SnapshotResult, error) {
		return client.Snapshot(ctx, req)
	})
}

func (p *browserPoolClient) GetMarkdown(ctx context.Context, req shared.GetMarkdownRequest) (*shared.GetMarkdownResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.GetMarkdownResult, error) {
		return client.GetMarkdown(ctx, req)
	})
}

func (p *browserPoolClient) Hover(ctx context.Context, req shared.HoverRequest) (*shared.HoverResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.HoverResult, error) {
		return client.Hover(ctx, req)
	})
}

func (p *browserPoolClient) PressKey(ctx context.Context, req shared.PressKeyRequest) (*shared.PressKeyResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.PressKeyResult, error) {
		return client.PressKey(ctx, req)
	})
}

func (p *browserPoolClient) DragDrop(ctx context.Context, req shared.DragDropRequest) (*shared.DragDropResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.DragDropResult, error) {
		return client.DragDrop(ctx, req)
	})
}

func (p *browserPoolClient) Assert(ctx context.Context, req shared.AssertRequest) (*shared.AssertResult, error) {
	return withClientResult(ctx, p, func(client *browser.Client) (*shared.AssertResult, error) {
		return client.Assert(ctx, req)
	})
}

func (p *browserPoolClient) ListFrames(ctx context.Context) ([]*browser.BrowserFrameInfo, error) {
	var frames []*browser.BrowserFrameInfo
	err := p.withClient(ctx, func(client *browser.Client) error {
		var e error
		frames, e = client.ListFrames(ctx)
		return e
	})
	return frames, err
}

func (p *browserPoolClient) SwitchFrame(ctx context.Context, selector string) error {
	return p.withClient(ctx, func(client *browser.Client) error {
		return client.SwitchFrame(ctx, selector)
	})
}

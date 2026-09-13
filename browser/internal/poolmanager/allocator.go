package poolmanager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

var (
	errPoolCapacityExhausted = errors.New("capacity_exhausted")
)

type InstanceStatus struct {
	ID             string    `json:"id"`
	Endpoint       string    `json:"endpoint"`
	Ready          bool      `json:"ready"`
	Connected      bool      `json:"connected"`
	ActiveSessions int       `json:"active_sessions"`
	Quarantined    bool      `json:"quarantined"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	LastSeenAt     time.Time `json:"last_seen_at,omitempty"`
}

type PoolStatus struct {
	PoolID                 string           `json:"pool_id"`
	Provider               string           `json:"provider"`
	Mode                   string           `json:"mode"`
	SelectionPolicy        string           `json:"selection_policy"`
	TotalInstances         int              `json:"total_instances"`
	ReadyInstances         int              `json:"ready_instances"`
	DesiredInstances       int              `json:"desired_instances"`
	ActiveSessions         int              `json:"active_sessions"`
	QueuedSessions         int              `json:"queued_sessions"`
	MaxSessionsPerInstance int              `json:"max_sessions_per_instance"`
	ScaleUpCooldownSeconds int64            `json:"scale_up_cooldown_s"`
	ScaleDownCooldownS     int64            `json:"scale_down_cooldown_s"`
	IdleTTLSeconds         int64            `json:"idle_ttl_s"`
	QueueTimeoutSeconds    int64            `json:"queue_timeout_s"`
	LastScaleActionAt      time.Time        `json:"last_scale_action_at,omitempty"`
	LastScaleReason        string           `json:"last_scale_reason,omitempty"`
	ReconcileStatus        string           `json:"reconcile_status"`
	ReconcileError         string           `json:"reconcile_error,omitempty"`
	Instances              []InstanceStatus `json:"instances"`
}

type Lease struct {
	PoolID          string `json:"pool_id"`
	SessionKey      string `json:"session_key"`
	InstanceID      string `json:"instance_id"`
	Endpoint        string `json:"endpoint"`
	LeaseTTLSeconds int64  `json:"lease_ttl_seconds,omitempty"`
}

type PoolSpec struct {
	PoolID                 string         `json:"pool_id"`
	Provider               string         `json:"provider"`
	Mode                   string         `json:"mode"`
	SelectionPolicy        string         `json:"selection_policy"`
	MinWarmInstances       int            `json:"min_warm_instances"`
	MaxInstances           int            `json:"max_instances"`
	MaxSessionsPerInstance int            `json:"max_sessions_per_instance"`
	ScaleUpCooldown        time.Duration  `json:"scale_up_cooldown"`
	ScaleDownCooldown      time.Duration  `json:"scale_down_cooldown"`
	IdleTTL                time.Duration  `json:"idle_ttl"`
	StartupTimeout         time.Duration  `json:"startup_timeout"`
	QueueTimeout           time.Duration  `json:"queue_timeout"`
	ProviderConfig         map[string]any `json:"provider_config"`
}

func (s *PoolSpec) Normalize() error {
	if s == nil {
		return fmt.Errorf("pool spec is required")
	}
	s.PoolID = normalizePoolID(s.PoolID)
	s.Provider = strings.ToLower(strings.TrimSpace(s.Provider))
	if s.Provider == "" {
		s.Provider = "docker"
	}
	s.Mode = strings.ToLower(strings.TrimSpace(s.Mode))
	if s.Mode == "" {
		s.Mode = "static"
	}
	s.SelectionPolicy = normalizeSelectionPolicy(s.SelectionPolicy)
	if s.ProviderConfig == nil {
		s.ProviderConfig = map[string]any{}
	}

	switch s.Provider {
	case "static", "docker", "k8s", "koyeb":
	default:
		return fmt.Errorf("pool provider must be one of: static, docker, k8s, koyeb")
	}
	switch s.Mode {
	case "static", "dynamic":
	default:
		return fmt.Errorf("pool mode must be static or dynamic")
	}

	if s.MinWarmInstances < 0 {
		return fmt.Errorf("pool min_warm_instances must be >= 0")
	}
	if s.MaxInstances <= 0 {
		s.MaxInstances = 1
	}
	if s.MaxSessionsPerInstance <= 0 {
		s.MaxSessionsPerInstance = 5
	}
	if s.ScaleUpCooldown <= 0 {
		s.ScaleUpCooldown = 5 * time.Second
	}
	if s.ScaleDownCooldown <= 0 {
		s.ScaleDownCooldown = 60 * time.Second
	}
	if s.IdleTTL <= 0 {
		s.IdleTTL = 300 * time.Second
	}
	if s.StartupTimeout <= 0 {
		s.StartupTimeout = 90 * time.Second
	}
	if s.QueueTimeout <= 0 {
		s.QueueTimeout = 20 * time.Second
	}
	if s.MaxInstances < s.MinWarmInstances {
		return fmt.Errorf("pool max_instances must be >= min_warm_instances")
	}

	return nil
}

type ProviderInstance struct {
	ID        string
	Endpoint  string
	Ready     bool
	CreatedAt time.Time
	LastSeen  time.Time
}

type Provider interface {
	Kind() string
	Reconcile(ctx context.Context, poolID string, desired int, spec PoolSpec) error
	ListInstances(ctx context.Context, poolID string, spec PoolSpec) ([]ProviderInstance, error)
	DeletePool(ctx context.Context, poolID string, spec PoolSpec) error
}

type pool struct {
	id                 string
	spec               PoolSpec
	provider           Provider
	instances          map[string]*managedInstance
	sessions           map[string]*sessionBinding
	waiters            int
	desiredInstances   int
	lastScaleActionAt  time.Time
	lastScaleReason    string
	lastScaleUpAt      time.Time
	lastScaleDownAt    time.Time
	reconcileStatus    string
	reconcileError     string
	lastReconcileAt    time.Time
	lastReconcileStart time.Time
	mu                 sync.Mutex
}

type managedInstance struct {
	ID             string
	Endpoint       string
	Ready          bool
	Quarantined    bool
	ActiveSessions int
	CreatedAt      time.Time
	LastSeenAt     time.Time
}

type sessionBinding struct {
	InstanceID string
	LastUsed   time.Time
}

type Manager struct {
	logger            *zap.Logger
	reconcileInterval time.Duration
	providers         map[string]Provider
	pools             map[string]*pool
	stopCh            chan struct{}
	doneCh            chan struct{}
	mu                sync.RWMutex
}

func NewManager(cfg *ServiceConfig, logger *zap.Logger) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("service config is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	m := &Manager{
		logger:            logger.Named("pool-manager"),
		reconcileInterval: cfg.ReconcileInterval,
		providers: map[string]Provider{
			"static": newStaticProvider(),
			"docker": newDockerProvider(cfg.DockerDefaults, logger),
			"k8s":    newK8sProvider(cfg.K8sDefaults, logger),
			"koyeb":  newKoyebProvider(cfg.KoyebDefaults, logger),
		},
		pools:  make(map[string]*pool),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}

	go m.reconcileLoop()

	if cfg.DefaultPool != nil {
		if _, err := m.ReconcilePool(context.Background(), *cfg.DefaultPool); err != nil {
			m.logger.Warn("failed to reconcile default pool", zap.Error(err), zap.String("pool_id", cfg.DefaultPool.PoolID))
		}
	}

	return m, nil
}

func (m *Manager) Shutdown(_ context.Context) {
	select {
	case <-m.stopCh:
		return
	default:
		close(m.stopCh)
	}
	<-m.doneCh
}

func (m *Manager) ReconcilePool(ctx context.Context, spec PoolSpec) (PoolStatus, error) {
	if err := spec.Normalize(); err != nil {
		return PoolStatus{}, err
	}
	provider, ok := m.providers[spec.Provider]
	if !ok {
		return PoolStatus{}, fmt.Errorf("unsupported pool provider %q", spec.Provider)
	}

	p := m.getOrCreatePool(spec.PoolID)
	p.mu.Lock()
	p.spec = spec
	p.provider = provider
	if p.instances == nil {
		p.instances = make(map[string]*managedInstance)
	}
	if p.sessions == nil {
		p.sessions = make(map[string]*sessionBinding)
	}
	if p.reconcileStatus == "" {
		p.reconcileStatus = "pending"
	}
	p.mu.Unlock()

	if err := m.reconcilePoolNow(ctx, p, "manual_reconcile"); err != nil {
		return p.statusSnapshot(), err
	}
	return p.statusSnapshot(), nil
}

func (m *Manager) AllocateSession(ctx context.Context, poolID, sessionKey string, queueTimeout time.Duration) (*Lease, error) {
	p, err := m.poolByID(poolID)
	if err != nil {
		return nil, err
	}
	sessionKey = normalizeSessionKey(sessionKey)
	if queueTimeout <= 0 {
		p.mu.Lock()
		queueTimeout = p.spec.QueueTimeout
		p.mu.Unlock()
	}
	if queueTimeout <= 0 {
		queueTimeout = 20 * time.Second
	}

	acquireCtx := ctx
	if acquireCtx == nil {
		acquireCtx = context.Background()
	}
	if _, hasDeadline := acquireCtx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		acquireCtx, cancel = context.WithTimeout(acquireCtx, queueTimeout)
		defer cancel()
	}

	waiting := false
	lastReconcile := time.Time{}
	for {
		lease, acquireErr := p.tryAcquire(sessionKey)
		if acquireErr == nil {
			if waiting {
				p.decrementWaiters()
			}
			return lease, nil
		}
		if !errors.Is(acquireErr, errPoolCapacityExhausted) {
			if waiting {
				p.decrementWaiters()
			}
			return nil, acquireErr
		}

		if !waiting {
			p.incrementWaiters()
			waiting = true
		}

		now := time.Now()
		if now.Sub(lastReconcile) >= 300*time.Millisecond {
			_ = m.reconcilePoolNow(context.Background(), p, "capacity_exhausted")
			lastReconcile = now
		}

		select {
		case <-acquireCtx.Done():
			p.decrementWaiters()
			return nil, fmt.Errorf("%w: timeout waiting for available instance", errPoolCapacityExhausted)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (m *Manager) ReleaseSession(poolID, sessionKey string) error {
	p, err := m.poolByID(poolID)
	if err != nil {
		return err
	}
	p.release(sessionKey)
	return nil
}

func (m *Manager) PoolStatus(poolID string) (PoolStatus, error) {
	p, err := m.poolByID(poolID)
	if err != nil {
		return PoolStatus{}, err
	}
	return p.statusSnapshot(), nil
}

func (m *Manager) DeletePool(ctx context.Context, poolID string) error {
	p, err := m.poolByID(poolID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	spec := p.spec
	provider := p.provider
	p.mu.Unlock()
	if provider != nil {
		if err := provider.DeletePool(ctx, poolID, spec); err != nil {
			return err
		}
	}

	m.mu.Lock()
	delete(m.pools, poolID)
	m.mu.Unlock()
	return nil
}

func (m *Manager) reconcileLoop() {
	defer close(m.doneCh)
	interval := m.reconcileInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.reconcileAll()
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) reconcileAll() {
	m.mu.RLock()
	pools := make([]*pool, 0, len(m.pools))
	for _, p := range m.pools {
		pools = append(pools, p)
	}
	m.mu.RUnlock()

	for _, p := range pools {
		_ = m.reconcilePoolNow(context.Background(), p, "periodic")
	}
}

func (m *Manager) reconcilePoolNow(ctx context.Context, p *pool, reason string) error {
	if p == nil {
		return fmt.Errorf("pool is nil")
	}

	p.mu.Lock()
	spec := p.spec
	provider := p.provider
	startedAt := time.Now()
	p.lastReconcileStart = startedAt
	p.reconcileStatus = "running"
	p.mu.Unlock()

	if provider == nil {
		p.markReconcileError(fmt.Errorf("pool provider is not configured"))
		return fmt.Errorf("pool provider is not configured")
	}

	instances, err := provider.ListInstances(ctx, p.id, spec)
	if err != nil {
		p.markReconcileError(err)
		return err
	}

	now := time.Now()
	p.mu.Lock()
	p.syncInstancesLocked(instances)
	p.evictIdleSessionsLocked(now)
	p.dropStaleSessionsLocked()
	readyCount := p.readyInstancesLocked()
	currentCount := len(p.instances)
	activeSessions := len(p.sessions)
	queuedSessions := p.waiters
	maxSess := maxInt(1, spec.MaxSessionsPerInstance)
	freeCapacity := readyCount*maxSess - activeSessions

	desired := currentCount
	if spec.Mode == "dynamic" {
		desired = int(math.Ceil(float64(activeSessions+queuedSessions) / float64(maxSess)))
		if desired < spec.MinWarmInstances {
			desired = spec.MinWarmInstances
		}
		if desired > spec.MaxInstances {
			desired = spec.MaxInstances
		}
		if desired < 0 {
			desired = 0
		}
		if freeCapacity <= 0 && queuedSessions > 0 && desired < spec.MaxInstances {
			desired++
		}
	}
	if spec.Mode == "static" {
		desired = currentCount
	}

	if desired > currentCount && now.Sub(p.lastScaleUpAt) < spec.ScaleUpCooldown {
		desired = currentCount
	}
	if desired < currentCount && now.Sub(p.lastScaleDownAt) < spec.ScaleDownCooldown {
		desired = currentCount
	}
	if desired < 0 {
		desired = 0
	}
	p.desiredInstances = desired
	p.mu.Unlock()

	if desired != currentCount {
		if err := provider.Reconcile(ctx, p.id, desired, spec); err != nil {
			p.markReconcileError(err)
			return err
		}

		instances, err = provider.ListInstances(ctx, p.id, spec)
		if err != nil {
			p.markReconcileError(err)
			return err
		}

		p.mu.Lock()
		p.syncInstancesLocked(instances)
		if desired > currentCount {
			p.lastScaleUpAt = time.Now()
		} else {
			p.lastScaleDownAt = time.Now()
		}
		p.lastScaleActionAt = time.Now()
		p.lastScaleReason = reason
		p.mu.Unlock()
	}

	p.mu.Lock()
	p.lastReconcileAt = time.Now()
	p.reconcileStatus = "ok"
	p.reconcileError = ""
	p.mu.Unlock()
	return nil
}

func (m *Manager) getOrCreatePool(poolID string) *pool {
	poolID = normalizePoolID(poolID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.pools[poolID]; ok {
		return existing
	}
	p := &pool{
		id:              poolID,
		instances:       make(map[string]*managedInstance),
		sessions:        make(map[string]*sessionBinding),
		reconcileStatus: "pending",
		lastScaleReason: "initial",
	}
	m.pools[poolID] = p
	return p
}

func (m *Manager) poolByID(poolID string) (*pool, error) {
	poolID = normalizePoolID(poolID)
	m.mu.RLock()
	p := m.pools[poolID]
	m.mu.RUnlock()
	if p == nil {
		return nil, fmt.Errorf("pool %q not found", poolID)
	}
	return p, nil
}

func (p *pool) tryAcquire(sessionKey string) (*Lease, error) {
	now := time.Now()
	sessionKey = normalizeSessionKey(sessionKey)

	p.mu.Lock()
	defer p.mu.Unlock()

	p.evictIdleSessionsLocked(now)
	p.dropStaleSessionsLocked()

	if binding, ok := p.sessions[sessionKey]; ok {
		binding.LastUsed = now
		if inst, exists := p.instances[binding.InstanceID]; exists && inst.Ready && !inst.Quarantined {
			return &Lease{
				PoolID:          p.id,
				SessionKey:      sessionKey,
				InstanceID:      inst.ID,
				Endpoint:        inst.Endpoint,
				LeaseTTLSeconds: int64(p.spec.IdleTTL.Seconds()),
			}, nil
		}
		delete(p.sessions, sessionKey)
	}

	inst := p.selectInstanceLocked()
	if inst == nil {
		return nil, errPoolCapacityExhausted
	}

	inst.ActiveSessions++
	p.sessions[sessionKey] = &sessionBinding{InstanceID: inst.ID, LastUsed: now}
	return &Lease{
		PoolID:          p.id,
		SessionKey:      sessionKey,
		InstanceID:      inst.ID,
		Endpoint:        inst.Endpoint,
		LeaseTTLSeconds: int64(p.spec.IdleTTL.Seconds()),
	}, nil
}

func (p *pool) release(sessionKey string) {
	sessionKey = normalizeSessionKey(sessionKey)
	p.mu.Lock()
	defer p.mu.Unlock()
	binding, ok := p.sessions[sessionKey]
	if !ok {
		return
	}
	if inst, exists := p.instances[binding.InstanceID]; exists && inst.ActiveSessions > 0 {
		inst.ActiveSessions--
	}
	delete(p.sessions, sessionKey)
}

func (p *pool) incrementWaiters() {
	p.mu.Lock()
	p.waiters++
	p.mu.Unlock()
}

func (p *pool) decrementWaiters() {
	p.mu.Lock()
	if p.waiters > 0 {
		p.waiters--
	}
	p.mu.Unlock()
}

func (p *pool) statusSnapshot() PoolStatus {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.evictIdleSessionsLocked(time.Now())
	p.dropStaleSessionsLocked()

	status := PoolStatus{
		PoolID:                 p.id,
		Provider:               p.spec.Provider,
		Mode:                   p.spec.Mode,
		SelectionPolicy:        p.spec.SelectionPolicy,
		TotalInstances:         len(p.instances),
		DesiredInstances:       p.desiredInstances,
		ActiveSessions:         len(p.sessions),
		QueuedSessions:         p.waiters,
		MaxSessionsPerInstance: p.spec.MaxSessionsPerInstance,
		ScaleUpCooldownSeconds: int64(p.spec.ScaleUpCooldown.Seconds()),
		ScaleDownCooldownS:     int64(p.spec.ScaleDownCooldown.Seconds()),
		IdleTTLSeconds:         int64(p.spec.IdleTTL.Seconds()),
		QueueTimeoutSeconds:    int64(p.spec.QueueTimeout.Seconds()),
		LastScaleActionAt:      p.lastScaleActionAt,
		LastScaleReason:        p.lastScaleReason,
		ReconcileStatus:        p.reconcileStatus,
		ReconcileError:         p.reconcileError,
		Instances:              make([]InstanceStatus, 0, len(p.instances)),
	}

	for _, inst := range p.instances {
		if inst.Ready {
			status.ReadyInstances++
		}
		status.Instances = append(status.Instances, InstanceStatus{
			ID:             inst.ID,
			Endpoint:       inst.Endpoint,
			Ready:          inst.Ready,
			Connected:      inst.Ready,
			ActiveSessions: inst.ActiveSessions,
			Quarantined:    inst.Quarantined,
			CreatedAt:      inst.CreatedAt,
			LastSeenAt:     inst.LastSeenAt,
		})
	}

	sort.Slice(status.Instances, func(i, j int) bool {
		return status.Instances[i].ID < status.Instances[j].ID
	})
	return status
}

func (p *pool) markReconcileError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastReconcileAt = time.Now()
	p.reconcileStatus = "error"
	if err != nil {
		p.reconcileError = err.Error()
	} else {
		p.reconcileError = "unknown error"
	}
}

func (p *pool) syncInstancesLocked(providerInstances []ProviderInstance) {
	next := make(map[string]*managedInstance, len(providerInstances))
	for _, item := range providerInstances {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		existing := p.instances[id]
		if existing == nil {
			existing = &managedInstance{ID: id}
		}
		existing.Endpoint = strings.TrimSpace(item.Endpoint)
		existing.Ready = item.Ready
		existing.LastSeenAt = item.LastSeen
		if existing.LastSeenAt.IsZero() {
			existing.LastSeenAt = time.Now()
		}
		if !item.CreatedAt.IsZero() {
			existing.CreatedAt = item.CreatedAt
		}
		if existing.CreatedAt.IsZero() {
			existing.CreatedAt = time.Now()
		}
		next[id] = existing
	}

	for _, inst := range next {
		inst.ActiveSessions = 0
	}
	for _, binding := range p.sessions {
		if inst, ok := next[binding.InstanceID]; ok {
			inst.ActiveSessions++
		}
	}

	p.instances = next
}

func (p *pool) dropStaleSessionsLocked() {
	for sessionKey, binding := range p.sessions {
		inst, ok := p.instances[binding.InstanceID]
		if !ok || inst == nil || inst.Endpoint == "" {
			delete(p.sessions, sessionKey)
			continue
		}
		if !inst.Ready {
			delete(p.sessions, sessionKey)
		}
	}
}

func (p *pool) evictIdleSessionsLocked(now time.Time) {
	if p.spec.IdleTTL <= 0 {
		return
	}
	for sessionKey, binding := range p.sessions {
		if now.Sub(binding.LastUsed) < p.spec.IdleTTL {
			continue
		}
		if inst, ok := p.instances[binding.InstanceID]; ok && inst.ActiveSessions > 0 {
			inst.ActiveSessions--
		}
		delete(p.sessions, sessionKey)
	}
}

func (p *pool) readyInstancesLocked() int {
	count := 0
	for _, inst := range p.instances {
		if inst.Ready && !inst.Quarantined {
			count++
		}
	}
	return count
}

func (p *pool) selectInstanceLocked() *managedInstance {
	if len(p.instances) == 0 {
		return nil
	}
	maxSessions := maxInt(1, p.spec.MaxSessionsPerInstance)
	selection := p.spec.SelectionPolicy

	instanceIDs := make([]string, 0, len(p.instances))
	for id := range p.instances {
		instanceIDs = append(instanceIDs, id)
	}
	sort.Strings(instanceIDs)

	if selection == "round_robin" {
		for _, id := range instanceIDs {
			inst := p.instances[id]
			if inst == nil || !inst.Ready || inst.Quarantined || strings.TrimSpace(inst.Endpoint) == "" {
				continue
			}
			if inst.ActiveSessions >= maxSessions {
				continue
			}
			return inst
		}
		return nil
	}

	var selected *managedInstance
	for _, id := range instanceIDs {
		inst := p.instances[id]
		if inst == nil || !inst.Ready || inst.Quarantined || strings.TrimSpace(inst.Endpoint) == "" {
			continue
		}
		if inst.ActiveSessions >= maxSessions {
			continue
		}
		if selected == nil || inst.ActiveSessions < selected.ActiveSessions {
			selected = inst
		}
	}
	return selected
}

func normalizePoolID(poolID string) string {
	trimmed := strings.TrimSpace(poolID)
	if trimmed == "" {
		return "default"
	}
	return trimmed
}

func normalizeSessionKey(sessionKey string) string {
	trimmed := strings.TrimSpace(sessionKey)
	if trimmed == "" {
		return "default"
	}
	return trimmed
}

func normalizeSelectionPolicy(policy string) string {
	v := strings.ToLower(strings.TrimSpace(policy))
	if v == "round_robin" {
		return "round_robin"
	}
	return "least_loaded"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

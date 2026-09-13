package healthcheck

import (
	"context"
	"sync"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/identity"
	"go.uber.org/zap"
)

// State represents the current health state.
type State string

const (
	StateHealthy   State = "healthy"
	StateUnhealthy State = "unhealthy"
	StateRenewing  State = "renewing"
	StateUnknown   State = "unknown"
)

// Checker performs periodic credential validation.
type Checker struct {
	identityMgr *identity.Manager
	interval    time.Duration
	logger      *zap.Logger
	state       State
	lastCheck   time.Time
	mu          sync.RWMutex
	onUnhealthy func()
}

// NewChecker creates a new health checker.
func NewChecker(mgr *identity.Manager, interval time.Duration, logger *zap.Logger) *Checker {
	return &Checker{
		identityMgr: mgr,
		interval:    interval,
		logger:      logger.Named("healthcheck"),
		state:       StateUnknown,
	}
}

// SetOnUnhealthy sets a callback to be invoked when credentials become invalid.
func (c *Checker) SetOnUnhealthy(fn func()) {
	c.mu.Lock()
	c.onUnhealthy = fn
	c.mu.Unlock()
}

// Start begins the periodic health check loop.
func (c *Checker) Start(ctx context.Context) {
	c.logger.Info("Starting health checker",
		zap.Duration("interval", c.interval),
	)

	// Perform initial check
	c.check(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Health checker stopped")
			return
		case <-ticker.C:
			c.check(ctx)
		}
	}
}

// check performs a single health check.
func (c *Checker) check(ctx context.Context) {
	c.logger.Debug("Performing health check")

	resp, err := c.identityMgr.Validate(ctx)
	if err != nil {
		c.logger.Error("Health check failed", zap.Error(err))
		c.setState(StateUnhealthy)
		c.triggerUnhealthy()
		return
	}

	c.mu.Lock()
	c.lastCheck = time.Now()
	c.mu.Unlock()

	if !resp.Valid {
		c.logger.Warn("Credentials are no longer valid")
		c.setState(StateUnhealthy)
		c.triggerUnhealthy()
		return
	}

	if resp.RenewRequired {
		c.logger.Info("Credential renewal required, initiating...")
		c.setState(StateRenewing)

		if err := c.identityMgr.Renew(ctx); err != nil {
			c.logger.Error("Credential renewal failed", zap.Error(err))
			c.setState(StateUnhealthy)
			return
		}

		c.logger.Info("Credentials renewed successfully")
	}

	c.setState(StateHealthy)
}

// setState updates the current state.
func (c *Checker) setState(state State) {
	c.mu.Lock()
	c.state = state
	c.mu.Unlock()
}

// triggerUnhealthy invokes the unhealthy callback if set.
func (c *Checker) triggerUnhealthy() {
	c.mu.RLock()
	fn := c.onUnhealthy
	c.mu.RUnlock()

	if fn != nil {
		fn()
	}
}

// State returns the current health state.
func (c *Checker) State() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// LastCheck returns the time of the last health check.
func (c *Checker) LastCheck() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastCheck
}

// IsHealthy returns true if the last check was successful.
func (c *Checker) IsHealthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state == StateHealthy
}

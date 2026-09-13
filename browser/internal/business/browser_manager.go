package business

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/browser"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

type snapshotState struct {
	currentSnapshotID  string
	currentSnapshotURL string
	refElements        map[string]shared.SnapshotElement
	snapshotElements   []shared.SnapshotElement
}

type capturedImage struct {
	data      []byte
	width     int
	height    int
	timestamp time.Time
}

type captureState struct {
	captures   map[string]*capturedImage
	totalBytes int64
}

type frameState struct {
	currentFrameSelector string
	currentFrameURL      string
	currentFrameTitle    string
}

// BrowserManager manages browser lifecycle and operations
type BrowserManager struct {
	client  *browserPoolClient
	config  *config.BrowserConfig
	logger  *zap.Logger
	baseCtx context.Context
	running bool
	mu      sync.RWMutex
	// opMu serializes CDP-backed tool operations so concurrent sessions cannot
	// overlap chromedp.Run calls on a shared browser target.
	opMu sync.Mutex

	refsMu         sync.RWMutex
	snapshotBySess map[string]*snapshotState

	capturesMu     sync.RWMutex
	capturesBySess map[string]*captureState

	networkMu     sync.RWMutex
	networkBySess map[string]*browser.NetworkState

	framesMu     sync.RWMutex
	frameBySess  map[string]*frameState

	// Test hooks (left nil in production).
	ensureConnectedFn func(context.Context) error
	reconnectFn       func(context.Context) error
}

// NewBrowserManager creates a new browser manager
func NewBrowserManager(cfg *config.BrowserConfig, logger *zap.Logger) *BrowserManager {
	manager := &BrowserManager{
		config:         cfg,
		logger:         logger.Named("browser-manager"),
		snapshotBySess: make(map[string]*snapshotState),
		capturesBySess: make(map[string]*captureState),
		networkBySess:  make(map[string]*browser.NetworkState),
		frameBySess:    make(map[string]*frameState),
	}

	client, err := newBrowserPoolClient(cfg, logger)
	if err != nil {
		manager.logger.Fatal("failed to create browser pool", zap.Error(err))
	}
	client.SetSessionEvictedHook(manager.clearSnapshotRefs)
	manager.client = client

	return manager
}

func (m *BrowserManager) isRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

func (m *BrowserManager) sessionKey(ctx context.Context) string {
	return shared.SessionKeyFromContext(ctx)
}

func (m *BrowserManager) ensureSessionContext(ctx context.Context) context.Context {
	return shared.WithSessionKey(ctx, m.sessionKey(ctx))
}

func (m *BrowserManager) sessionState(sessionKey string) *snapshotState {
	if m.snapshotBySess == nil {
		m.snapshotBySess = make(map[string]*snapshotState)
	}
	state, ok := m.snapshotBySess[sessionKey]
	if ok {
		return state
	}
	state = &snapshotState{
		refElements: make(map[string]shared.SnapshotElement),
	}
	m.snapshotBySess[sessionKey] = state
	return state
}

// Start starts the browser manager (lazy connection - doesn't require browser at startup)
func (m *BrowserManager) Start(ctx context.Context) error {
	if m.isRunning() {
		return nil
	}

	m.mu.Lock()
	m.running = true
	// Keep a long-lived context for (re)connect operations.
	m.baseCtx = ctx
	m.mu.Unlock()

	// Keep a long-lived context for (re)connect operations.
	m.logger.Info("starting browser manager")

	// Try to connect but don't fail if browser is not available
	connectCtx := shared.WithSessionKey(ctx, shared.DefaultSessionKey)
	if err := m.client.Connect(connectCtx); err != nil {
		m.logger.Warn("browser not available at startup (will retry on first tool call)",
			zap.String("pool_mode", normalizePoolMode(m.config.PoolMode)),
			zap.Error(err))
	}

	m.logger.Info("browser manager started")
	return nil
}

// Stop stops the browser manager
func (m *BrowserManager) Stop() error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return nil
	}
	m.running = false
	m.mu.Unlock()

	m.logger.Info("stopping browser manager")

	if err := m.client.Disconnect(); err != nil {
		m.logger.Error("failed to disconnect browser", zap.Error(err))
	}
	m.clearAllSnapshotRefs()

	m.logger.Info("browser manager stopped")
	return nil
}

// Name returns the manager name
func (m *BrowserManager) Name() string {
	return "BrowserManager"
}

// Health checks the health of the browser manager
func (m *BrowserManager) Health(ctx context.Context) error {
	if !m.isRunning() {
		return fmt.Errorf("browser manager not running")
	}

	status := m.client.PoolStatus()
	if status.ConnectedCount == 0 {
		return fmt.Errorf("browser not connected")
	}

	return nil
}

// GetClient returns the browser client
func (m *BrowserManager) GetClient() shared.BrowserClient {
	return m.client
}

// Navigate navigates to a URL
func (m *BrowserManager) Navigate(ctx context.Context, req shared.NavigateRequest) (*shared.NavigateResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "navigate", func() (*shared.NavigateResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		m.logger.Debug("navigating", zap.String("url", req.URL))
		result, err := m.client.Navigate(m.ensureSessionContext(ctx), req)
		if err == nil {
			m.clearSnapshotRefs(sessionKey)
		}
		return result, err
	})
}

// Click clicks on an element
func (m *BrowserManager) Click(ctx context.Context, req shared.ClickRequest) (*shared.ClickResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "click", func() (*shared.ClickResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
		if err != nil {
			return nil, err
		}
		req.Selector = selector

		m.logger.Debug("clicking", zap.String("selector", req.Selector))
		return m.client.Click(m.ensureSessionContext(ctx), req)
	})
}

// Fill fills in a form field
func (m *BrowserManager) Fill(ctx context.Context, req shared.FillRequest) (*shared.FillResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "fill", func() (*shared.FillResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
		if err != nil {
			return nil, err
		}
		req.Selector = selector

		m.logger.Debug("filling", zap.String("selector", req.Selector))
		return m.client.Fill(m.ensureSessionContext(ctx), req)
	})
}

// GetText extracts text from an element
func (m *BrowserManager) GetText(ctx context.Context, req shared.GetTextRequest) (*shared.GetTextResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "get_text", func() (*shared.GetTextResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
		if err != nil {
			return nil, err
		}
		req.Selector = selector

		m.logger.Debug("getting text", zap.String("selector", req.Selector))
		return m.client.GetText(m.ensureSessionContext(ctx), req)
	})
}

// GetHTML extracts HTML from an element or page
func (m *BrowserManager) GetHTML(ctx context.Context, req shared.GetHTMLRequest) (*shared.GetHTMLResult, error) {
	return executeWithRecoveryResult(ctx, m, "get_html", func() (*shared.GetHTMLResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		m.logger.Debug("getting HTML", zap.String("selector", req.Selector))
		return m.client.GetHTML(m.ensureSessionContext(ctx), req)
	})
}

// Screenshot takes a screenshot
func (m *BrowserManager) Screenshot(ctx context.Context, req shared.ScreenshotRequest) (*shared.ScreenshotResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "screenshot", func() (*shared.ScreenshotResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		if req.Ref != "" || strings.HasPrefix(req.Selector, "@") {
			selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
			if err != nil {
				return nil, err
			}
			req.Selector = selector
		}

		m.logger.Debug("taking screenshot")
		return m.client.Screenshot(m.ensureSessionContext(ctx), req)
	})
}

// Evaluate evaluates JavaScript
func (m *BrowserManager) Evaluate(ctx context.Context, req shared.EvaluateRequest) (*shared.EvaluateResult, error) {
	return executeWithRecoveryResult(ctx, m, "evaluate", func() (*shared.EvaluateResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		m.logger.Debug("evaluating script")
		return m.client.Evaluate(m.ensureSessionContext(ctx), req)
	})
}

// WaitForSelector waits for a selector
func (m *BrowserManager) WaitForSelector(ctx context.Context, req shared.WaitForSelectorRequest) (*shared.WaitForSelectorResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "wait_for_selector", func() (*shared.WaitForSelectorResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
		if err != nil {
			return nil, err
		}
		req.Selector = selector

		m.logger.Debug("waiting for selector", zap.String("selector", req.Selector))
		return m.client.WaitForSelector(m.ensureSessionContext(ctx), req)
	})
}

// GetPageInfo returns current page information
func (m *BrowserManager) GetPageInfo(ctx context.Context) (*shared.PageInfo, error) {
	return executeWithRecoveryResult(ctx, m, "get_page_info", func() (*shared.PageInfo, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		return m.client.GetPageInfo(m.ensureSessionContext(ctx))
	})
}

// GoBack navigates back
func (m *BrowserManager) GoBack(ctx context.Context) error {
	sessionKey := m.sessionKey(ctx)
	return m.executeWithRecovery(ctx, "go_back", func() error {
		if !m.isRunning() {
			return shared.ErrServiceUnavailable("browser-manager")
		}

		m.logger.Debug("navigating back")
		err := m.client.GoBack(m.ensureSessionContext(ctx))
		if err == nil {
			m.clearSnapshotRefs(sessionKey)
		}
		return err
	})
}

// GoForward navigates forward
func (m *BrowserManager) GoForward(ctx context.Context) error {
	sessionKey := m.sessionKey(ctx)
	return m.executeWithRecovery(ctx, "go_forward", func() error {
		if !m.isRunning() {
			return shared.ErrServiceUnavailable("browser-manager")
		}

		m.logger.Debug("navigating forward")
		err := m.client.GoForward(m.ensureSessionContext(ctx))
		if err == nil {
			m.clearSnapshotRefs(sessionKey)
		}
		return err
	})
}

// Reload reloads the page
func (m *BrowserManager) Reload(ctx context.Context) error {
	sessionKey := m.sessionKey(ctx)
	return m.executeWithRecovery(ctx, "reload", func() error {
		if !m.isRunning() {
			return shared.ErrServiceUnavailable("browser-manager")
		}

		m.logger.Debug("reloading page")
		err := m.client.Reload(m.ensureSessionContext(ctx))
		if err == nil {
			m.clearSnapshotRefs(sessionKey)
		}
		return err
	})
}

// SelectOption selects an option
func (m *BrowserManager) SelectOption(ctx context.Context, req shared.SelectOptionRequest) (*shared.SelectOptionResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "select_option", func() (*shared.SelectOptionResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
		if err != nil {
			return nil, err
		}
		req.Selector = selector

		m.logger.Debug("selecting option", zap.String("selector", req.Selector))
		return m.client.SelectOption(m.ensureSessionContext(ctx), req)
	})
}

// Scroll scrolls the page or element
func (m *BrowserManager) Scroll(ctx context.Context, req shared.ScrollRequest) (*shared.ScrollResult, error) {
	return executeWithRecoveryResult(ctx, m, "scroll", func() (*shared.ScrollResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		m.logger.Debug("scrolling")
		return m.client.Scroll(m.ensureSessionContext(ctx), req)
	})
}

// GetAttribute gets an attribute from an element
func (m *BrowserManager) GetAttribute(ctx context.Context, req shared.GetAttributeRequest) (*shared.GetAttributeResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "get_attribute", func() (*shared.GetAttributeResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
		if err != nil {
			return nil, err
		}
		req.Selector = selector

		m.logger.Debug("getting attribute", zap.String("selector", req.Selector), zap.String("attribute", req.Attribute))
		return m.client.GetAttribute(m.ensureSessionContext(ctx), req)
	})
}

// GetMarkdown extracts page content as Markdown
func (m *BrowserManager) GetMarkdown(ctx context.Context, req shared.GetMarkdownRequest) (*shared.GetMarkdownResult, error) {
	return executeWithRecoveryResult(ctx, m, "get_markdown", func() (*shared.GetMarkdownResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		m.logger.Debug("getting markdown", zap.String("strategy", req.Strategy))
		return m.client.GetMarkdown(m.ensureSessionContext(ctx), req)
	})
}

// Snapshot captures the current page interactive elements and assigns compact refs.
func (m *BrowserManager) Snapshot(ctx context.Context, req shared.SnapshotRequest) (*shared.SnapshotResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "snapshot", func() (*shared.SnapshotResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		result, err := m.client.Snapshot(m.ensureSessionContext(ctx), req)
		if err != nil {
			return nil, err
		}

		m.storeSnapshotRefs(sessionKey, result)
		return result, nil
	})
}

// Find searches semantic elements from the current snapshot.
func (m *BrowserManager) Find(ctx context.Context, req shared.FindRequest) (*shared.FindResult, error) {
	if strings.TrimSpace(req.By) == "" {
		return nil, shared.ErrValidation("find.by is required")
	}
	if strings.TrimSpace(req.Value) == "" {
		return nil, shared.ErrValidation("find.value is required")
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 10
	}

	sessionKey := m.sessionKey(ctx)
	elements := m.getSnapshotElements(sessionKey)
	if len(elements) == 0 {
		// Lazy snapshot fallback so find can work even if no explicit snapshot call happened yet.
		if _, err := m.Snapshot(ctx, shared.SnapshotRequest{Format: "compact", MaxElements: 200}); err != nil {
			return nil, err
		}
		elements = m.getSnapshotElements(sessionKey)
	}

	by := strings.ToLower(strings.TrimSpace(req.By))
	needle := strings.TrimSpace(req.Value)
	nameNeedle := strings.TrimSpace(req.Name)

	matches := make([]shared.FindMatch, 0, len(elements))
	for _, el := range elements {
		score := matchScore(by, needle, nameNeedle, req.Exact, el)
		if score <= 0 {
			continue
		}
		matches = append(matches, shared.FindMatch{
			Ref:      el.Ref,
			Score:    score,
			Role:     el.Role,
			Name:     el.Name,
			Selector: el.Selector,
		})
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].Ref < matches[j].Ref
		}
		return matches[i].Score > matches[j].Score
	})

	if len(matches) > req.MaxResults {
		matches = matches[:req.MaxResults]
	}

	return &shared.FindResult{
		By:      by,
		Value:   needle,
		Name:    nameNeedle,
		Count:   len(matches),
		Matches: matches,
	}, nil
}

// GetStatus returns the current status of the browser connection.
func (m *BrowserManager) GetStatus(ctx context.Context) *shared.BrowserStatus {
	sessionKey := m.sessionKey(ctx)
	pool := m.client.PoolStatus()

	m.refsMu.RLock()
	state, ok := m.snapshotBySess[sessionKey]
	m.refsMu.RUnlock()

	status := &shared.BrowserStatus{
		ManagerRunning:   m.isRunning(),
		BrowserConnected: pool.ConnectedCount > 0,
		CDPEndpoint:      m.config.CDPEndpoint,
		PoolMode:         pool.Mode,
		PoolSize:         pool.TotalInstances,
		ActiveSessions:   pool.ActiveSessions,
		SessionKey:       sessionKey,
	}
	if ok && state != nil {
		status.SnapshotID = state.currentSnapshotID
		status.SnapshotURL = state.currentSnapshotURL
		status.RefCount = len(state.refElements)
	}
	return status
}

// Reconnect attempts to reconnect to the browser
func (m *BrowserManager) Reconnect(ctx context.Context) error {
	if !m.isRunning() {
		return shared.ErrServiceUnavailable("browser-manager")
	}

	m.logger.Info("attempting to reconnect to browser")

	sessionKey := m.sessionKey(ctx)
	connectCtx := m.baseCtx
	if connectCtx == nil {
		connectCtx = ctx
	}
	connectCtx = shared.WithSessionKey(connectCtx, sessionKey)

	if err := m.client.Reconnect(connectCtx); err != nil {
		m.logger.Error("failed to reconnect to browser", zap.Error(err))
		return fmt.Errorf("failed to reconnect: %w", err)
	}
	m.clearSnapshotRefs(sessionKey)

	m.logger.Info("successfully reconnected to browser")
	return nil
}

// EnsureConnected ensures the browser is connected, attempting to connect if not
func (m *BrowserManager) EnsureConnected(ctx context.Context) error {
	if !m.isRunning() {
		return shared.ErrServiceUnavailable("browser-manager")
	}

	m.logger.Debug("ensuring browser connection")
	sessionKey := m.sessionKey(ctx)
	connectCtx := m.baseCtx
	if connectCtx == nil {
		connectCtx = ctx
	}
	connectCtx = shared.WithSessionKey(connectCtx, sessionKey)

	if err := m.client.Connect(connectCtx); err != nil {
		return fmt.Errorf("browser not available: %w", err)
	}

	return nil
}

func (m *BrowserManager) operationMaxAttempts() int {
	if !m.config.AutoReconnect {
		return 1
	}
	attempts := m.config.ReconnectRetries + 1
	if attempts < 1 {
		return 1
	}
	return attempts
}

func (m *BrowserManager) ensureConnected(ctx context.Context) error {
	if m.ensureConnectedFn != nil {
		return m.ensureConnectedFn(ctx)
	}
	return m.EnsureConnected(ctx)
}

func (m *BrowserManager) reconnect(ctx context.Context) error {
	if m.reconnectFn != nil {
		return m.reconnectFn(ctx)
	}
	return m.Reconnect(ctx)
}

func (m *BrowserManager) isRecoverableBrowserError(err error) bool {
	return shared.IsTransientBrowserError(err)
}

func (m *BrowserManager) executeWithRecovery(ctx context.Context, action string, op func() error) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	maxAttempts := m.operationMaxAttempts()
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := m.ensureConnected(ctx); err != nil {
			return err
		}

		err := op()
		if err == nil {
			return nil
		}
		lastErr = err

		if attempt == maxAttempts || !m.isRecoverableBrowserError(err) {
			break
		}

		m.logger.Warn("browser action failed, reconnecting before retry",
			zap.String("action", action),
			zap.Int("attempt", attempt),
			zap.Int("max_attempts", maxAttempts),
			zap.Error(err))

		// Prefer the long-lived manager context so a cancelled request does not
		// abort reconnect after a transient CDP failure.
		reconnectCtx := m.baseCtx
		if reconnectCtx == nil {
			reconnectCtx = context.Background()
		}
		reconnectCtx = shared.WithSessionKey(reconnectCtx, m.sessionKey(ctx))

		if reconnectErr := m.reconnect(reconnectCtx); reconnectErr != nil {
			m.logger.Warn("browser reconnect failed",
				zap.String("action", action),
				zap.Error(reconnectErr))
			break
		}
	}

	return lastErr
}

func executeWithRecoveryResult[T any](ctx context.Context, m *BrowserManager, action string, op func() (T, error)) (T, error) {
	var (
		result T
		zero   T
	)

	err := m.executeWithRecovery(ctx, action, func() error {
		var opErr error
		result, opErr = op()
		return opErr
	})
	if err != nil {
		return zero, err
	}

	return result, nil
}

func (m *BrowserManager) storeSnapshotRefs(sessionKey string, snapshot *shared.SnapshotResult) {
	if snapshot == nil {
		return
	}
	sessionKey = normalizeSessionKey(sessionKey)

	refs := make(map[string]shared.SnapshotElement, len(snapshot.Elements))
	for _, el := range snapshot.Elements {
		refs[el.Ref] = el
	}

	m.refsMu.Lock()
	defer m.refsMu.Unlock()
	state := m.sessionState(sessionKey)
	state.currentSnapshotID = snapshot.SnapshotID
	state.currentSnapshotURL = snapshot.URL
	state.refElements = refs
	state.snapshotElements = append([]shared.SnapshotElement(nil), snapshot.Elements...)
}

func (m *BrowserManager) clearSnapshotRefs(sessionKey string) {
	sessionKey = normalizeSessionKey(sessionKey)

	m.refsMu.Lock()
	defer m.refsMu.Unlock()
	delete(m.snapshotBySess, sessionKey)

	m.capturesMu.Lock()
	defer m.capturesMu.Unlock()
	delete(m.capturesBySess, sessionKey)

	m.networkMu.Lock()
	defer m.networkMu.Unlock()
	state, ok := m.networkBySess[sessionKey]
	if ok && state != nil {
		// Cancel listener if active
		if state.ListenerCancel != nil {
			state.ListenerCancel()
		}
		// Wait for listener goroutine to exit
		if state.ListenerDone != nil {
			select {
			case <-state.ListenerDone:
			case <-time.After(100 * time.Millisecond):
				m.logger.Warn("timeout waiting for network listener to exit", zap.String("session_key", sessionKey))
			}
		}
	}
	delete(m.networkBySess, sessionKey)

	m.framesMu.Lock()
	defer m.framesMu.Unlock()
	delete(m.frameBySess, sessionKey)
}

func (m *BrowserManager) clearAllSnapshotRefs() {
	m.refsMu.Lock()
	defer m.refsMu.Unlock()
	m.snapshotBySess = make(map[string]*snapshotState)
}

func (m *BrowserManager) getSnapshotElements(sessionKey string) []shared.SnapshotElement {
	sessionKey = normalizeSessionKey(sessionKey)

	m.refsMu.RLock()
	defer m.refsMu.RUnlock()
	state, ok := m.snapshotBySess[sessionKey]
	if !ok || state == nil {
		return nil
	}
	return append([]shared.SnapshotElement(nil), state.snapshotElements...)
}

// Capture takes a named screenshot and stores it in session state
func (m *BrowserManager) Capture(ctx context.Context, req shared.CaptureRequest) (*shared.CaptureResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "capture", func() (*shared.CaptureResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		// Take screenshot
		screenshotReq := shared.ScreenshotRequest{
			Selector: req.Selector,
			Ref:      req.Ref,
			FullPage: req.FullPage,
			Format:   "png",
		}

		screenshotResult, err := m.client.Screenshot(m.ensureSessionContext(ctx), screenshotReq)
		if err != nil {
			return nil, err
		}

		// Decode base64 data to get PNG bytes
		imageBytes, err := decodePNG(screenshotResult.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to process screenshot: %w", err)
		}

		// Check per-session limits
		m.capturesMu.Lock()
		defer m.capturesMu.Unlock()

		state := m.captureState(sessionKey)
		captureSize := int64(len(imageBytes))

		// Check max captures per session
		if len(state.captures) >= m.config.CaptureMaxPerSess {
			return nil, fmt.Errorf("capture limit per session exceeded (%d max)", m.config.CaptureMaxPerSess)
		}

		// Check max bytes per session
		if state.totalBytes+captureSize > m.config.CaptureMaxBytes {
			return nil, fmt.Errorf("capture memory limit per session exceeded (%d MB max)", m.config.CaptureMaxBytes/(1024*1024))
		}

		// Store capture
		captureID := fmt.Sprintf("capture_%d_%s", time.Now().UnixNano(), req.Name)
		state.captures[captureID] = &capturedImage{
			data:      imageBytes,
			width:     screenshotResult.Width,
			height:    screenshotResult.Height,
			timestamp: time.Now(),
		}
		state.totalBytes += captureSize

		m.logger.Debug("capture stored", zap.String("capture_id", captureID), zap.Int("size_bytes", len(imageBytes)))

		return &shared.CaptureResult{
			CaptureID: captureID,
			Dimensions: shared.ImageDimensions{
				Width:  screenshotResult.Width,
				Height: screenshotResult.Height,
			},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}, nil
	})
}

// VisualDiff compares two captured screenshots and returns diff regions
func (m *BrowserManager) VisualDiff(ctx context.Context, req shared.VisualDiffRequest) (*shared.VisualDiffResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "visual_diff", func() (*shared.VisualDiffResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		m.capturesMu.RLock()
		state, ok := m.capturesBySess[sessionKey]
		m.capturesMu.RUnlock()

		if !ok || state == nil {
			return nil, shared.ErrValidation("no captures in session (use browser_capture first)")
		}

		// Retrieve before and after captures
		beforeCapture, ok := state.captures[req.Before]
		if !ok {
			return nil, fmt.Errorf("before capture not found: %s", req.Before)
		}

		afterCapture, ok := state.captures[req.After]
		if !ok {
			return nil, fmt.Errorf("after capture not found: %s", req.After)
		}

		// Default threshold
		threshold := req.Threshold
		if threshold <= 0 {
			threshold = 10
		}

		// Compute pixel diff
		diffResult, err := browser.ComputePixelDiff(beforeCapture.data, afterCapture.data, threshold)
		if err != nil {
			return nil, fmt.Errorf("diff computation failed: %w", err)
		}

		// Convert regions
		diffRegions := make([]shared.VisualDiffRegion, len(diffResult.Regions))
		for i, r := range diffResult.Regions {
			diffRegions[i] = shared.VisualDiffRegion{
				X:              r.X,
				Y:              r.Y,
				Width:          r.Width,
				Height:         r.Height,
				DiffPercentage: r.DiffPercentage,
			}
		}

		m.logger.Debug("visual diff completed",
			zap.String("before", req.Before),
			zap.String("after", req.After),
			zap.Float64("diff_percentage", diffResult.DiffPercentage),
			zap.Int("diff_pixels", diffResult.DiffPixelCount),
			zap.Int("region_count", len(diffRegions)))

		return &shared.VisualDiffResult{
			Match:          diffResult.Match,
			DiffPercentage: diffResult.DiffPercentage,
			DiffPixelCount: diffResult.DiffPixelCount,
			Dimensions: shared.ImageDimensions{
				Width:  diffResult.Width,
				Height: diffResult.Height,
			},
			Regions: diffRegions,
		}, nil
	})
}

func (m *BrowserManager) captureState(sessionKey string) *captureState {
	if m.capturesBySess == nil {
		m.capturesBySess = make(map[string]*captureState)
	}
	state, ok := m.capturesBySess[sessionKey]
	if ok && state != nil {
		return state
	}
	state = &captureState{
		captures: make(map[string]*capturedImage),
	}
	m.capturesBySess[sessionKey] = state
	return state
}

// decodePNG decodes base64 PNG data to bytes
func decodePNG(base64Data string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(base64Data)
}

func (m *BrowserManager) resolveSelector(sessionKey, selector, ref string) (string, error) {
	sessionKey = normalizeSessionKey(sessionKey)
	selector = strings.TrimSpace(selector)
	ref = strings.TrimSpace(ref)

	if ref == "" && strings.HasPrefix(selector, "@") {
		ref = selector
		selector = ""
	}

	if selector != "" {
		return selector, nil
	}
	if ref == "" {
		return "", shared.ErrValidation("either selector or ref is required")
	}

	m.refsMu.RLock()
	defer m.refsMu.RUnlock()
	state, ok := m.snapshotBySess[sessionKey]
	if !ok || state == nil {
		return "", shared.ErrValidationf("unknown ref %s (no active snapshot for session %s, call browser_snapshot first)", ref, sessionKey)
	}
	if el, ok := state.refElements[ref]; ok {
		return el.Selector, nil
	}

	if state.currentSnapshotID == "" {
		return "", shared.ErrValidationf("unknown ref %s (no active snapshot, call browser_snapshot first)", ref)
	}
	return "", shared.ErrValidationf("unknown ref %s for snapshot %s", ref, state.currentSnapshotID)
}

func matchScore(by, value, name string, exact bool, el shared.SnapshotElement) float64 {
	normalizedBy := strings.ToLower(strings.TrimSpace(by))
	needle := strings.ToLower(strings.TrimSpace(value))
	nameNeedle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return 0
	}

	role := strings.ToLower(el.Role)
	primary := map[string]string{
		"role":        role,
		"text":        strings.ToLower(el.Text),
		"label":       strings.ToLower(el.Name),
		"placeholder": strings.ToLower(el.Placeholder),
		"testid":      strings.ToLower(el.TestID),
		"title":       strings.ToLower(el.Title),
		"alt":         strings.ToLower(el.Alt),
	}

	candidate, ok := primary[normalizedBy]
	if !ok {
		return 0
	}

	mainScore := basicScore(candidate, needle, exact)
	if mainScore == 0 {
		return 0
	}

	// For role-based search, support an optional name refinement.
	if normalizedBy == "role" && nameNeedle != "" {
		nameScore := basicScore(strings.ToLower(el.Name), nameNeedle, exact)
		if nameScore == 0 {
			nameScore = basicScore(strings.ToLower(el.Text), nameNeedle, exact)
		}
		if nameScore == 0 {
			return 0
		}
		return 0.7*mainScore + 0.3*nameScore
	}

	return mainScore
}

func basicScore(candidate, needle string, exact bool) float64 {
	candidate = strings.TrimSpace(candidate)
	needle = strings.TrimSpace(needle)
	if candidate == "" || needle == "" {
		return 0
	}

	if exact {
		if candidate == needle {
			return 1
		}
		return 0
	}

	if candidate == needle {
		return 1
	}
	if strings.HasPrefix(candidate, needle) {
		return 0.92
	}
	if strings.Contains(candidate, needle) {
		return 0.82
	}
	return 0
}

// NetworkCaptureStart starts network capture for a session
func (m *BrowserManager) NetworkCaptureStart(ctx context.Context) (string, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "network_capture_start", func() (string, error) {
		if !m.isRunning() {
			return "", shared.ErrServiceUnavailable("browser-manager")
		}

		m.networkMu.Lock()
		defer m.networkMu.Unlock()

		// Get or create network state
		state, ok := m.networkBySess[sessionKey]
		if !ok {
			state = &browser.NetworkState{
				MaxEntries:       100,
				ResponseBodySize: m.config.NetworkMaxBodyBytes,
				Mocks:            make(map[string]*browser.MockRule),
			}
			m.networkBySess[sessionKey] = state
		}

		if state.Capturing {
			return "", shared.ErrValidation("network capture already active")
		}

		// Generate capture ID
		captureID := fmt.Sprintf("cap_%d", time.Now().UnixNano())
		state.Capturing = true
		state.CaptureID = captureID

		// Create listener context
		listenerCtx, cancel := context.WithCancel(ctx)
		state.ListenerCtx = listenerCtx
		state.ListenerCancel = cancel
		state.ListenerDone = make(chan struct{})

		// Start listener goroutine (skeleton for when browser is available)
		go m.networkListenerGoroutine(sessionKey, listenerCtx, state.ListenerDone)

		m.logger.Debug("started network capture", zap.String("session_key", sessionKey), zap.String("capture_id", captureID))
		return captureID, nil
	})
}

// networkListenerGoroutine handles network capture in the background
func (m *BrowserManager) networkListenerGoroutine(sessionKey string, ctx context.Context, done chan struct{}) {
	defer close(done)

	// Check context before locking
	select {
	case <-ctx.Done():
		return
	default:
	}

	m.networkMu.RLock()
	state, ok := m.networkBySess[sessionKey]
	m.networkMu.RUnlock()

	if !ok || state == nil {
		return
	}

	// Skeleton implementation: when CDP integration is available, wire up:
	// chromedp.ListenTarget(ctx, func(ev interface{}) { ... })

	// For now, just wait for context cancellation
	<-ctx.Done()
}

// NetworkCaptureStop stops network capture for a session
func (m *BrowserManager) NetworkCaptureStop(ctx context.Context) ([]interface{}, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "network_capture_stop", func() ([]interface{}, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		m.networkMu.Lock()
		defer m.networkMu.Unlock()

		state, ok := m.networkBySess[sessionKey]
		if !ok || state == nil {
			return nil, shared.ErrValidation("no network capture active")
		}

		if !state.Capturing {
			return nil, shared.ErrValidation("network capture not active")
		}

		state.Capturing = false

		// Cancel listener
		if state.ListenerCancel != nil {
			state.ListenerCancel()
		}

		// Get entries before clearing
		entries := state.GetEntries()
		result := make([]interface{}, len(entries))
		for i, entry := range entries {
			result[i] = entry
		}

		m.logger.Debug("stopped network capture", zap.String("session_key", sessionKey), zap.Int("entries", len(entries)))
		return result, nil
	})
}

// NetworkMock registers a mock rule for network interception
func (m *BrowserManager) NetworkMock(ctx context.Context, urlPattern string, method string, responseStatus int, responseHeaders map[string]interface{}, responseBody string, once bool) (string, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "network_mock", func() (string, error) {
		if !m.isRunning() {
			return "", shared.ErrServiceUnavailable("browser-manager")
		}

		m.networkMu.Lock()
		defer m.networkMu.Unlock()

		// Get or create network state
		state, ok := m.networkBySess[sessionKey]
		if !ok {
			state = &browser.NetworkState{
				MaxEntries:       100,
				ResponseBodySize: m.config.NetworkMaxBodyBytes,
				Mocks:            make(map[string]*browser.MockRule),
			}
			m.networkBySess[sessionKey] = state
		}

		// Convert headers from map[string]interface{} to map[string]string
		headers := make(map[string]string)
		if responseHeaders != nil {
			for k, v := range responseHeaders {
				if str, ok := v.(string); ok {
					headers[k] = str
				}
			}
		}

		// Generate mock ID
		mockID := fmt.Sprintf("mock_%d", time.Now().UnixNano())

		rule := &browser.MockRule{
			ID:              mockID,
			URLPattern:      urlPattern,
			Method:          method,
			ResponseStatus:  responseStatus,
			ResponseHeaders: headers,
			ResponseBody:    responseBody,
			Once:            once,
		}

		state.AddMock(rule)
		m.logger.Debug("registered mock rule", zap.String("session_key", sessionKey), zap.String("mock_id", mockID))
		return mockID, nil
	})
}

// NetworkMockClear clears all mock rules for a session
func (m *BrowserManager) NetworkMockClear(ctx context.Context) error {
	sessionKey := m.sessionKey(ctx)
	return m.executeWithRecovery(ctx, "network_mock_clear", func() error {
		if !m.isRunning() {
			return shared.ErrServiceUnavailable("browser-manager")
		}

		m.networkMu.Lock()
		defer m.networkMu.Unlock()

		state, ok := m.networkBySess[sessionKey]
		if !ok || state == nil {
			// Nothing to clear
			return nil
		}

		state.ClearMocks()
		m.logger.Debug("cleared all mock rules", zap.String("session_key", sessionKey))
		return nil
	})
}

// Hover hovers over an element
func (m *BrowserManager) Hover(ctx context.Context, req shared.HoverRequest) (*shared.HoverResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "hover", func() (*shared.HoverResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
		if err != nil {
			return nil, err
		}
		req.Selector = selector

		m.logger.Debug("hovering", zap.String("selector", req.Selector))
		return m.client.Hover(m.ensureSessionContext(ctx), req)
	})
}

// PressKey sends keyboard input
func (m *BrowserManager) PressKey(ctx context.Context, req shared.PressKeyRequest) (*shared.PressKeyResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "press_key", func() (*shared.PressKeyResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		// Resolve selector if ref provided
		if req.Selector == "" && req.Ref != "" {
			selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
			if err != nil {
				return nil, err
			}
			req.Selector = selector
		}

		m.logger.Debug("pressing key", zap.String("key", req.Key))
		return m.client.PressKey(m.ensureSessionContext(ctx), req)
	})
}

// DragDrop performs drag-and-drop between elements
func (m *BrowserManager) DragDrop(ctx context.Context, req shared.DragDropRequest) (*shared.DragDropResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "drag_drop", func() (*shared.DragDropResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		sourceSelector, err := m.resolveSelector(sessionKey, req.SourceSelector, req.SourceRef)
		if err != nil {
			return nil, fmt.Errorf("source: %w", err)
		}
		req.SourceSelector = sourceSelector

		targetSelector, err := m.resolveSelector(sessionKey, req.TargetSelector, req.TargetRef)
		if err != nil {
			return nil, fmt.Errorf("target: %w", err)
		}
		req.TargetSelector = targetSelector

		m.logger.Debug("drag_drop",
			zap.String("source", req.SourceSelector),
			zap.String("target", req.TargetSelector))
		return m.client.DragDrop(m.ensureSessionContext(ctx), req)
	})
}

// Assert performs a DOM assertion
func (m *BrowserManager) Assert(ctx context.Context, req shared.AssertRequest) (*shared.AssertResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "assert", func() (*shared.AssertResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		// Resolve selector for element-based assertions
		if req.Assertion != "url_contains" && req.Assertion != "title_contains" {
			if req.Selector == "" && req.Ref != "" {
				selector, err := m.resolveSelector(sessionKey, req.Selector, req.Ref)
				if err != nil {
					return nil, err
				}
				req.Selector = selector
			}
		}

		m.logger.Debug("assert",
			zap.String("assertion", req.Assertion),
			zap.String("selector", req.Selector))
		return m.client.Assert(m.ensureSessionContext(ctx), req)
	})
}

// ListFrames lists all frames on the current page
func (m *BrowserManager) ListFrames(ctx context.Context) (*shared.ListFramesResult, error) {
	return executeWithRecoveryResult(ctx, m, "list_frames", func() (*shared.ListFramesResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		frames, err := m.client.ListFrames(m.ensureSessionContext(ctx))
		if err != nil {
			return nil, err
		}

		result := &shared.ListFramesResult{
			Frames: make([]shared.FrameInfo, len(frames)),
		}
		for i, f := range frames {
			result.Frames[i] = shared.FrameInfo{
				Selector: f.Selector,
				Name:     f.Name,
				URL:      f.URL,
				Visible:  f.Visible,
			}
		}
		return result, nil
	})
}

// SwitchFrame switches frame context for a session
func (m *BrowserManager) SwitchFrame(ctx context.Context, req shared.SwitchFrameRequest) (*shared.SwitchFrameResult, error) {
	sessionKey := m.sessionKey(ctx)
	return executeWithRecoveryResult(ctx, m, "switch_frame", func() (*shared.SwitchFrameResult, error) {
		if !m.isRunning() {
			return nil, shared.ErrServiceUnavailable("browser-manager")
		}

		// No parameters = reset to top frame
		if req.Selector == "" && req.Ref == "" && req.Name == "" {
			m.framesMu.Lock()
			delete(m.frameBySess, sessionKey)
			m.framesMu.Unlock()

			return &shared.SwitchFrameResult{
				Frame: "top",
				URL:   "",
				Title: "",
			}, nil
		}

		// Resolve selector
		selector := req.Selector
		if selector == "" && req.Ref != "" {
			var err error
			selector, err = m.resolveSelector(sessionKey, "", req.Ref)
			if err != nil {
				return nil, err
			}
		}
		if selector == "" && req.Name != "" {
			selector = fmt.Sprintf("iframe[name='%s']", req.Name)
		}

		// Verify frame exists
		if err := m.client.SwitchFrame(m.ensureSessionContext(ctx), selector); err != nil {
			return nil, err
		}

		// Look up frame info
		frames, err := m.client.ListFrames(m.ensureSessionContext(ctx))
		if err != nil {
			// Frame exists but couldn't get tree — store minimal state
			m.framesMu.Lock()
			m.frameBySess[sessionKey] = &frameState{
				currentFrameSelector: selector,
			}
			m.framesMu.Unlock()

			return &shared.SwitchFrameResult{
				Frame: selector,
			}, nil
		}

		var frameURL, frameTitle string
		for _, f := range frames {
			if f.Selector == selector || f.Name == req.Name {
				frameURL = f.URL
				frameTitle = f.Name
				break
			}
		}

		// Update state
		m.framesMu.Lock()
		m.frameBySess[sessionKey] = &frameState{
			currentFrameSelector: selector,
			currentFrameURL:      frameURL,
			currentFrameTitle:    frameTitle,
		}
		m.framesMu.Unlock()

		return &shared.SwitchFrameResult{
			Frame: selector,
			URL:   frameURL,
			Title: frameTitle,
		}, nil
	})
}

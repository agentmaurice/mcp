package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// Client implements BrowserClient interface using chromedp
type Client struct {
	config      *config.BrowserConfig
	logger      *zap.Logger
	allocCtx    context.Context
	allocCancel context.CancelFunc
	ctx         context.Context
	cancel      context.CancelFunc
	connected   bool
	mu          sync.RWMutex
	// opMu serializes chromedp operations against this client.
	opMu sync.Mutex
}

type containsSelectorPattern struct {
	Base string `json:"base"`
	Text string `json:"text"`
}

// NewClient creates a new browser client
func NewClient(cfg *config.BrowserConfig, logger *zap.Logger) *Client {
	return &Client{
		config: cfg,
		logger: logger.Named("browser-client"),
	}
}

// Connect establishes connection to the browser via CDP.
// The browser session context is intentionally detached from request-scoped
// contexts to avoid premature cancellations in MCP/SSE transports.
func (c *Client) Connect(_ context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		// If the existing session is still healthy, keep it.
		if c.ctx != nil && c.ctx.Err() == nil {
			return nil
		}
		// Stale connected state: tear down and recreate the session.
		c.logger.Warn("browser session marked connected but context is cancelled, recreating session")
		c.disconnectLocked()
	}

	c.logger.Info("connecting to browser",
		zap.String("cdp_endpoint", c.config.CDPEndpoint))

	// Create allocator context for remote browser.
	// Use a long-lived parent context so the browser session is not tied to a
	// transient request context.
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), c.config.CDPEndpoint)
	c.allocCtx = allocCtx
	c.allocCancel = allocCancel

	// Create browser context with options
	opts := []chromedp.ContextOption{
		chromedp.WithLogf(func(format string, args ...interface{}) {
			c.logger.Debug(fmt.Sprintf(format, args...))
		}),
	}

	browserCtx, cancel := chromedp.NewContext(allocCtx, opts...)
	c.ctx = browserCtx
	c.cancel = cancel

	// Initialize the browser by running a simple task
	if err := chromedp.Run(c.ctx); err != nil {
		c.disconnectLocked()
		return shared.ErrInternal("failed to connect to browser", err)
	}

	// Set viewport size
	if err := chromedp.Run(c.ctx,
		chromedp.EmulateViewport(int64(c.config.WindowWidth), int64(c.config.WindowHeight)),
	); err != nil {
		c.logger.Warn("failed to set viewport", zap.Error(err))
	}

	c.connected = true
	c.logger.Info("browser connected successfully")
	return nil
}

// Disconnect closes the browser connection
func (c *Client) Disconnect() error {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil
	}

	c.logger.Info("disconnecting from browser")
	c.disconnectLocked()
	c.logger.Info("browser disconnected")
	return nil
}

// requireSessionLocked returns unavailable when the CDP session is missing or
// its context has already been cancelled. Caller must hold c.mu.
func (c *Client) requireSessionLocked() error {
	if c.connected && c.ctx != nil && c.ctx.Err() == nil {
		return nil
	}
	return shared.ErrServiceUnavailable("browser")
}

func (c *Client) disconnectLocked() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.allocCancel != nil {
		c.allocCancel()
	}
	c.ctx = nil
	c.cancel = nil
	c.allocCtx = nil
	c.allocCancel = nil
	c.connected = false
}

// IsConnected checks if browser is connected
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.connected {
		return false
	}
	// Consider the browser disconnected if its session context is already done.
	return c.ctx != nil && c.ctx.Err() == nil
}

func (c *Client) actionContext(reqCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	// Deliberately anchor action contexts to the long-lived browser context.
	// Tying execution to request-scoped contexts caused premature cancellations
	// with some MCP/SSE clients ("context canceled" before CDP action completion).
	baseCtx := c.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	if timeout > 0 {
		return context.WithTimeout(baseCtx, timeout)
	}
	return context.WithCancel(baseCtx)
}

func (c *Client) isRetryableError(err error) bool {
	return shared.IsTransientBrowserError(err)
}

func (c *Client) isUnsupportedMethodError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknownmethod") || strings.Contains(msg, "-31998")
}

func (c *Client) runWithRetry(reqCtx context.Context, timeout time.Duration, action string, fn func(context.Context) error) error {
	const maxAttempts = 2
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		opCtx, cancel := c.actionContext(reqCtx, timeout)
		err := fn(opCtx)
		cancel()
		if err == nil {
			if attempt > 1 {
				c.logger.Info("browser action succeeded after retry",
					zap.String("action", action),
					zap.Int("attempt", attempt))
			}
			return nil
		}

		lastErr = err
		if attempt == maxAttempts || !c.isRetryableError(err) {
			break
		}

		c.logger.Warn("transient browser error, retrying",
			zap.String("action", action),
			zap.Int("attempt", attempt),
			zap.Error(err))
		time.Sleep(100 * time.Millisecond)
	}

	return lastErr
}

// Navigate navigates to a URL
func (c *Client) Navigate(ctx context.Context, req shared.NavigateRequest) (*shared.NavigateResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	timeout := c.config.NavigationTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Millisecond
	}

	targetURL := req.URL
	if resolved, ok := maybeResolveHTMLRedirect(ctx, req.URL); ok {
		c.logger.Info("following html meta-refresh before CDP navigate",
			zap.String("from", req.URL),
			zap.String("to", resolved))
		targetURL = resolved
	}

	c.logger.Debug("navigating to URL", zap.String("url", targetURL))

	var title string
	if err := c.runWithRetry(ctx, timeout, "navigate", func(opCtx context.Context) error {
		// Lightpanda often fails chromedp.Navigate's load lifecycle wait on
		// JS-heavy sites ("page load error Shutdown" / missing execution
		// context). Drive CDP page.Navigate directly, then wait for readiness.
		if err := c.navigateAndWait(opCtx, targetURL, &title); err != nil {
			return err
		}
		// Some sites still serve an empty stub in-browser; follow one meta hop.
		return c.followInPageMetaRefresh(opCtx, &title)
	}); err != nil {
		return nil, shared.ErrBrowserAction("navigate", err)
	}

	var currentURL string
	if err := c.runWithRetry(ctx, timeout, "get_url", func(opCtx context.Context) error {
		return chromedp.Run(opCtx, chromedp.Location(&currentURL))
	}); err != nil {
		currentURL = targetURL
	}

	return &shared.NavigateResult{
		URL:   currentURL,
		Title: title,
	}, nil
}

func (c *Client) navigateAndWait(opCtx context.Context, targetURL string, title *string) error {
	if err := chromedp.Run(opCtx, chromedp.ActionFunc(func(navCtx context.Context) error {
		_, _, _, err := page.Navigate(targetURL).Do(navCtx)
		return err
	})); err != nil {
		return err
	}

	waitErr := chromedp.Run(opCtx, chromedp.WaitReady("body", chromedp.ByQuery))
	if waitErr == nil {
		return chromedp.Run(opCtx, chromedp.Title(title))
	}

	// If readiness wait fails but navigation already resolved a URL, keep going.
	var loc string
	if locErr := chromedp.Run(opCtx, chromedp.Location(&loc)); locErr == nil &&
		strings.TrimSpace(loc) != "" && loc != "about:blank" {
		_ = chromedp.Run(opCtx, chromedp.Title(title))
		return nil
	}
	return waitErr
}

func (c *Client) followInPageMetaRefresh(opCtx context.Context, title *string) error {
	var info struct {
		TextLen int    `json:"textLen"`
		HTML    string `json:"html"`
		URL     string `json:"url"`
	}
	if err := chromedp.Run(opCtx, chromedp.Evaluate(`({
		textLen: ((document.body && document.body.innerText) || '').trim().length,
		html: (document.documentElement && document.documentElement.outerHTML || '').slice(0, 8192),
		url: location.href
	})`, &info)); err != nil {
		// Page may still be usable; don't fail navigate on probe errors.
		return nil
	}
	if info.TextLen >= 40 {
		return nil
	}
	target := parseMetaRefreshTarget(info.HTML)
	if target == "" {
		return nil
	}
	base := info.URL
	if strings.TrimSpace(base) == "" {
		base = "about:blank"
	}
	resolved, err := url.Parse(base)
	if err != nil {
		return nil
	}
	next, err := resolved.Parse(target)
	if err != nil || next.String() == "" || next.String() == base {
		return nil
	}
	c.logger.Info("following in-page meta-refresh after CDP navigate",
		zap.String("from", base),
		zap.String("to", next.String()))
	return c.navigateAndWait(opCtx, next.String(), title)
}

// GoBack navigates back in history
func (c *Client) GoBack(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return err
	}

	timeout := c.config.DefaultTimeout
	err := c.runWithRetry(ctx, timeout, "go_back", func(opCtx context.Context) error {
		return chromedp.Run(opCtx, chromedp.NavigateBack())
	})
	if err == nil {
		return nil
	}
	if !c.isUnsupportedMethodError(err) {
		return err
	}

	c.logger.Warn("NavigateBack unsupported by current CDP backend, using history.back fallback")
	return c.runWithRetry(ctx, timeout, "go_back_fallback", func(opCtx context.Context) error {
		return chromedp.Run(opCtx, chromedp.Evaluate(`history.back();`, nil))
	})
}

// GoForward navigates forward in history
func (c *Client) GoForward(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return err
	}

	timeout := c.config.DefaultTimeout
	err := c.runWithRetry(ctx, timeout, "go_forward", func(opCtx context.Context) error {
		return chromedp.Run(opCtx, chromedp.NavigateForward())
	})
	if err == nil {
		return nil
	}
	if !c.isUnsupportedMethodError(err) {
		return err
	}

	c.logger.Warn("NavigateForward unsupported by current CDP backend, using history.forward fallback")
	return c.runWithRetry(ctx, timeout, "go_forward_fallback", func(opCtx context.Context) error {
		return chromedp.Run(opCtx, chromedp.Evaluate(`history.forward();`, nil))
	})
}

// Reload reloads the current page
func (c *Client) Reload(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return err
	}

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	return chromedp.Run(opCtx, chromedp.Reload())
}

// GetPageInfo returns information about the current page
func (c *Client) GetPageInfo(ctx context.Context) (*shared.PageInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	var url, title string
	if err := chromedp.Run(opCtx,
		chromedp.Location(&url),
		chromedp.Title(&title),
	); err != nil {
		c.logger.Warn("primary get_page_info failed, trying fallback script", zap.Error(err))

		// Fallback for pages where Location/Title can fail due to transient
		// execution-context state. Keep this non-fatal so tool callers can
		// continue with navigate/reconnect flows.
		const fallbackScript = `(function() {
			const href = (typeof window !== 'undefined' && window.location && typeof window.location.href === 'string')
				? window.location.href
				: '';
			const pageTitle = (typeof document !== 'undefined' && typeof document.title === 'string')
				? document.title
				: '';
			return { url: href, title: pageTitle };
		})()`

		var fallback map[string]interface{}
		if err2 := chromedp.Run(opCtx, chromedp.Evaluate(fallbackScript, &fallback)); err2 != nil {
			c.logger.Warn("fallback get_page_info failed, returning empty page info", zap.Error(err2))
		} else {
			url = getString(fallback, "url")
			title = getString(fallback, "title")
		}
	}

	return &shared.PageInfo{
		URL:    url,
		Title:  title,
		Width:  c.config.WindowWidth,
		Height: c.config.WindowHeight,
	}, nil
}

// GetTitle returns the current page title
func (c *Client) GetTitle(ctx context.Context) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return "", err
	}

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	var title string
	if err := chromedp.Run(opCtx, chromedp.Title(&title)); err != nil {
		return "", shared.ErrBrowserAction("get_title", err)
	}
	return title, nil
}

// GetURL returns the current page URL
func (c *Client) GetURL(ctx context.Context) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return "", err
	}

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	var url string
	if err := chromedp.Run(opCtx, chromedp.Location(&url)); err != nil {
		return "", shared.ErrBrowserAction("get_url", err)
	}
	return url, nil
}

// Click clicks on an element
func (c *Client) Click(ctx context.Context, req shared.ClickRequest) (*shared.ClickResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	timeout := c.config.DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Millisecond
	}

	c.logger.Debug("clicking element", zap.String("selector", req.Selector))

	patterns, hasContains, parseErr := parseContainsSelectorPatterns(req.Selector)
	if parseErr != nil {
		return nil, shared.ErrValidation(parseErr.Error())
	}
	if hasContains {
		if err := c.runWithRetry(ctx, timeout, "click_contains", func(opCtx context.Context) error {
			return c.clickWithContainsPatterns(opCtx, req.Selector, patterns)
		}); err != nil {
			return nil, shared.ErrBrowserAction("click", err)
		}
		return &shared.ClickResult{
			Success: true,
			Message: fmt.Sprintf("clicked element by contains selector: %s", req.Selector),
		}, nil
	}

	if err := c.runWithRetry(ctx, timeout, "click", func(opCtx context.Context) error {
		return chromedp.Run(opCtx,
			chromedp.WaitVisible(req.Selector),
			chromedp.Click(req.Selector),
		)
	}); err != nil {
		return nil, shared.ErrBrowserAction("click", err)
	}

	return &shared.ClickResult{
		Success: true,
		Message: fmt.Sprintf("clicked element: %s", req.Selector),
	}, nil
}

func (c *Client) clickWithContainsPatterns(opCtx context.Context, selector string, patterns []containsSelectorPattern) error {
	if len(patterns) == 0 {
		return shared.ErrValidation("contains selector requires at least one pattern")
	}

	patternsJSON, err := json.Marshal(patterns)
	if err != nil {
		return shared.ErrInternal("failed to encode contains selector patterns", err)
	}

	script := fmt.Sprintf(`(function() {
  const patterns = %s;
  const isVisible = (el) => {
    if (!el) return false;
    const style = window.getComputedStyle(el);
    if (style.display === "none" || style.visibility === "hidden" || style.pointerEvents === "none") return false;
    const rect = el.getBoundingClientRect();
    return (rect.width > 0 || rect.height > 0 || el.getClientRects().length > 0);
  };

  for (const pattern of patterns) {
    const baseSelector = (pattern.base && pattern.base.trim()) ? pattern.base : "*";
    let elements = [];
    try {
      elements = document.querySelectorAll(baseSelector);
    } catch (e) {
      continue;
    }
    const needle = (pattern.text || "").toLowerCase();
    for (const element of elements) {
      const text = (element.innerText || element.textContent || "").toLowerCase();
      if (needle && !text.includes(needle)) continue;
      if (!isVisible(element)) continue;
      element.scrollIntoView({block: "center", inline: "center"});
      element.click();
      return {clicked: true, selector: baseSelector, text: pattern.text || ""};
    }
  }
  return {clicked: false};
})()`, string(patternsJSON))

	var result struct {
		Clicked  bool   `json:"clicked"`
		Selector string `json:"selector"`
		Text     string `json:"text"`
	}
	if err := chromedp.Run(opCtx, chromedp.Evaluate(script, &result)); err != nil {
		return err
	}
	if !result.Clicked {
		return shared.ErrElementNotFound(selector)
	}
	return nil
}

func parseContainsSelectorPatterns(selector string) ([]containsSelectorPattern, bool, error) {
	segments := strings.Split(selector, ",")
	patterns := make([]containsSelectorPattern, 0, len(segments))
	hasContains := false

	for _, segment := range segments {
		trimmed := strings.TrimSpace(segment)
		if trimmed == "" {
			continue
		}

		lowered := strings.ToLower(trimmed)
		idx := strings.Index(lowered, ":contains(")
		if idx == -1 {
			patterns = append(patterns, containsSelectorPattern{
				Base: trimmed,
				Text: "",
			})
			continue
		}

		hasContains = true
		pattern, err := parseContainsSelectorSegment(trimmed, idx)
		if err != nil {
			return nil, true, err
		}
		patterns = append(patterns, pattern)
	}

	if !hasContains {
		return nil, false, nil
	}
	if len(patterns) == 0 {
		return nil, true, shared.ErrValidation("contains selector requires at least one selector segment")
	}

	return patterns, true, nil
}

func parseContainsSelectorSegment(selector string, containsIndex int) (containsSelectorPattern, error) {
	const containsToken = ":contains("
	if containsIndex < 0 || containsIndex+len(containsToken) > len(selector) {
		return containsSelectorPattern{}, shared.ErrValidationf("invalid contains selector: %s", selector)
	}

	base := strings.TrimSpace(selector[:containsIndex])
	if base == "" {
		base = "*"
	}

	remainder := strings.TrimSpace(selector[containsIndex+len(containsToken):])
	if !strings.HasSuffix(remainder, ")") {
		return containsSelectorPattern{}, shared.ErrValidationf("invalid contains selector (missing ')'): %s", selector)
	}

	valueExpr := strings.TrimSpace(remainder[:len(remainder)-1])
	if len(valueExpr) < 2 {
		return containsSelectorPattern{}, shared.ErrValidationf("invalid contains selector text: %s", selector)
	}

	quote := valueExpr[0]
	if (quote != '\'' && quote != '"') || valueExpr[len(valueExpr)-1] != quote {
		return containsSelectorPattern{}, shared.ErrValidationf("contains selector text must be quoted: %s", selector)
	}

	text := valueExpr[1 : len(valueExpr)-1]
	text = unescapeContainsText(text, quote)
	if strings.TrimSpace(text) == "" {
		return containsSelectorPattern{}, shared.ErrValidationf("contains selector text cannot be empty: %s", selector)
	}

	return containsSelectorPattern{
		Base: base,
		Text: text,
	}, nil
}

func unescapeContainsText(text string, quote byte) string {
	text = strings.ReplaceAll(text, `\\`, `\`)
	if quote == '\'' {
		text = strings.ReplaceAll(text, `\'`, `'`)
	} else if quote == '"' {
		text = strings.ReplaceAll(text, `\"`, `"`)
	}
	return text
}

// Fill fills in a form field
func (c *Client) Fill(ctx context.Context, req shared.FillRequest) (*shared.FillResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	timeout := c.config.DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Millisecond
	}

	c.logger.Debug("filling element",
		zap.String("selector", req.Selector),
		zap.Bool("clear", req.Clear))

	var actions []chromedp.Action
	actions = append(actions, chromedp.WaitVisible(req.Selector))

	if req.Clear {
		actions = append(actions, chromedp.Clear(req.Selector))
	}

	actions = append(actions, chromedp.SendKeys(req.Selector, req.Value))

	if err := c.runWithRetry(ctx, timeout, "fill", func(opCtx context.Context) error {
		return chromedp.Run(opCtx, actions...)
	}); err != nil {
		return nil, shared.ErrBrowserAction("fill", err)
	}

	return &shared.FillResult{
		Success: true,
		Message: fmt.Sprintf("filled element: %s", req.Selector),
	}, nil
}

// SelectOption selects option(s) in a select element
func (c *Client) SelectOption(ctx context.Context, req shared.SelectOptionRequest) (*shared.SelectOptionResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	timeout := c.config.DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Millisecond
	}

	c.logger.Debug("selecting option",
		zap.String("selector", req.Selector),
		zap.Strings("values", req.Values))

	if len(req.Values) == 0 {
		return nil, shared.ErrValidation("select_option requires at least one value")
	}

	if err := c.runWithRetry(ctx, timeout, "select_option", func(opCtx context.Context) error {
		return chromedp.Run(opCtx,
			chromedp.WaitVisible(req.Selector),
			chromedp.SetValue(req.Selector, req.Values[0]),
		)
	}); err != nil {
		return nil, shared.ErrBrowserAction("select_option", err)
	}

	return &shared.SelectOptionResult{
		Selected: req.Values,
		Success:  true,
	}, nil
}

// Scroll scrolls the page or element
func (c *Client) Scroll(ctx context.Context, req shared.ScrollRequest) (*shared.ScrollResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	c.logger.Debug("scrolling", zap.Int("x", req.X), zap.Int("y", req.Y))

	var script string
	if req.Selector != "" {
		script = fmt.Sprintf(`document.querySelector('%s').scrollTo(%d, %d)`, req.Selector, req.X, req.Y)
	} else {
		script = fmt.Sprintf(`window.scrollTo(%d, %d)`, req.X, req.Y)
	}

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	if err := chromedp.Run(opCtx, chromedp.Evaluate(script, nil)); err != nil {
		return nil, shared.ErrBrowserAction("scroll", err)
	}

	return &shared.ScrollResult{
		Success: true,
		Message: "scrolled successfully",
	}, nil
}

// GetText gets the text content of an element
func (c *Client) GetText(ctx context.Context, req shared.GetTextRequest) (*shared.GetTextResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	timeout := c.config.DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Millisecond
	}

	c.logger.Debug("getting text", zap.String("selector", req.Selector))

	var text string
	if err := c.runWithRetry(ctx, timeout, "get_text", func(opCtx context.Context) error {
		return chromedp.Run(opCtx,
			chromedp.WaitVisible(req.Selector),
			chromedp.Text(req.Selector, &text),
		)
	}); err != nil {
		return nil, shared.ErrBrowserAction("get_text", err)
	}

	return &shared.GetTextResult{
		Text:     text,
		Selector: req.Selector,
	}, nil
}

// GetHTML gets the HTML content of an element or the full page
func (c *Client) GetHTML(ctx context.Context, req shared.GetHTMLRequest) (*shared.GetHTMLResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	c.logger.Debug("getting HTML",
		zap.String("selector", req.Selector),
		zap.Bool("outer", req.Outer))

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	var html string
	var err error

	if req.Selector == "" {
		// Get full page HTML
		err = chromedp.Run(opCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			node, err := dom.GetDocument().Do(ctx)
			if err != nil {
				return err
			}
			html, err = dom.GetOuterHTML().WithNodeID(node.NodeID).Do(ctx)
			return err
		}))
	} else if req.Outer {
		err = chromedp.Run(opCtx, chromedp.OuterHTML(req.Selector, &html))
	} else {
		err = chromedp.Run(opCtx, chromedp.InnerHTML(req.Selector, &html))
	}

	if err != nil {
		return nil, shared.ErrBrowserAction("get_html", err)
	}

	return &shared.GetHTMLResult{
		HTML:     html,
		Selector: req.Selector,
	}, nil
}

// GetAttribute gets an attribute value from an element
func (c *Client) GetAttribute(ctx context.Context, req shared.GetAttributeRequest) (*shared.GetAttributeResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	timeout := c.config.DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Millisecond
	}

	c.logger.Debug("getting attribute",
		zap.String("selector", req.Selector),
		zap.String("attribute", req.Attribute))

	var value string
	var exists bool
	if err := c.runWithRetry(ctx, timeout, "get_attribute", func(opCtx context.Context) error {
		return chromedp.Run(opCtx,
			chromedp.WaitVisible(req.Selector),
			chromedp.AttributeValue(req.Selector, req.Attribute, &value, &exists),
		)
	}); err != nil {
		return nil, shared.ErrBrowserAction("get_attribute", err)
	}

	return &shared.GetAttributeResult{
		Value:    value,
		Exists:   exists,
		Selector: req.Selector,
	}, nil
}

// Screenshot takes a screenshot
func (c *Client) Screenshot(ctx context.Context, req shared.ScreenshotRequest) (*shared.ScreenshotResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	c.logger.Debug("taking screenshot",
		zap.String("selector", req.Selector),
		zap.Bool("full_page", req.FullPage))

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	format := req.Format
	if format == "" {
		format = c.config.ScreenshotFormat
	}

	quality := req.Quality
	if quality == 0 {
		quality = c.config.ScreenshotQuality
	}

	var buf []byte
	var action chromedp.Action

	if req.Selector != "" {
		action = chromedp.Screenshot(req.Selector, &buf)
	} else if req.FullPage {
		action = chromedp.FullScreenshot(&buf, quality)
	} else {
		action = chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			if format == "jpeg" {
				buf, err = page.CaptureScreenshot().
					WithFormat(page.CaptureScreenshotFormatJpeg).
					WithQuality(int64(quality)).
					Do(ctx)
			} else {
				buf, err = page.CaptureScreenshot().
					WithFormat(page.CaptureScreenshotFormatPng).
					Do(ctx)
			}
			return err
		})
	}

	if err := chromedp.Run(opCtx, action); err != nil {
		return nil, shared.ErrBrowserAction("screenshot", err)
	}

	return &shared.ScreenshotResult{
		Data:   base64.StdEncoding.EncodeToString(buf),
		Format: format,
	}, nil
}

// Evaluate evaluates JavaScript in the browser
func (c *Client) Evaluate(ctx context.Context, req shared.EvaluateRequest) (*shared.EvaluateResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	c.logger.Debug("evaluating script")

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	var result interface{}

	scripts := buildEvaluateScriptCandidates(req.Script)
	var lastErr error
	for i, script := range scripts {
		if err := chromedp.Run(opCtx, chromedp.Evaluate(script, &result)); err != nil {
			lastErr = err
			if i == 0 {
				c.logger.Debug("evaluate primary script failed, trying fallback wrappers", zap.Error(err))
			}
			continue
		}

		return &shared.EvaluateResult{
			Result: result,
		}, nil
	}

	return nil, shared.ErrBrowserAction("evaluate", lastErr)
}

func buildEvaluateScriptCandidates(script string) []string {
	trimmed := strings.TrimSpace(script)
	if trimmed == "" {
		return []string{script}
	}

	statementWrapper := fmt.Sprintf("(function(){\n%s\n})()", trimmed)
	expressionWrapper := fmt.Sprintf("(function(){ return (%s); })()", trimmed)

	candidates := []string{trimmed}
	if strings.Contains(trimmed, "return") {
		candidates = append(candidates, statementWrapper, expressionWrapper)
	} else {
		candidates = append(candidates, expressionWrapper, statementWrapper)
	}

	return dedupeStrings(candidates)
}

func dedupeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// WaitForSelector waits for a selector to appear
func (c *Client) WaitForSelector(ctx context.Context, req shared.WaitForSelectorRequest) (*shared.WaitForSelectorResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	timeout := c.config.DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Millisecond
	}

	opCtx, cancel := c.actionContext(ctx, timeout)
	defer cancel()

	c.logger.Debug("waiting for selector",
		zap.String("selector", req.Selector),
		zap.String("state", req.State))

	var action chromedp.Action
	switch req.State {
	case "hidden":
		action = chromedp.WaitNotPresent(req.Selector)
	case "detached":
		action = chromedp.WaitNotPresent(req.Selector)
	default: // visible or attached
		action = chromedp.WaitVisible(req.Selector)
	}

	if err := chromedp.Run(opCtx, action); err != nil {
		return &shared.WaitForSelectorResult{
			Found:   false,
			Message: err.Error(),
		}, nil
	}

	return &shared.WaitForSelectorResult{
		Found:   true,
		Message: "selector found",
	}, nil
}

// WaitForNavigation waits for navigation to complete
func (c *Client) WaitForNavigation(ctx context.Context, timeout int) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return err
	}

	var timeoutDuration time.Duration
	if timeout > 0 {
		timeoutDuration = time.Duration(timeout) * time.Millisecond
	}

	opCtx, cancel := c.actionContext(ctx, timeoutDuration)
	defer cancel()

	return chromedp.Run(opCtx, chromedp.WaitReady("body"))
}

// GetMarkdown extracts page content as Markdown
func (c *Client) GetMarkdown(ctx context.Context, req shared.GetMarkdownRequest) (*shared.GetMarkdownResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	c.logger.Debug("extracting markdown",
		zap.String("strategy", req.Strategy),
		zap.Bool("includeMetadata", req.IncludeMetadata),
		zap.Bool("includeLinks", req.IncludeLinks),
		zap.Bool("includeTables", req.IncludeTables),
		zap.Int("maxLength", req.MaxLength))

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	// Set defaults
	strategy := req.Strategy
	if strategy == "" {
		strategy = "auto"
	}
	includeMetadata := true
	if req.IncludeMetadata == false {
		includeMetadata = req.IncludeMetadata
	}
	includeLinks := true
	if req.IncludeLinks == false {
		includeLinks = req.IncludeLinks
	}
	includeTables := true
	if req.IncludeTables == false {
		includeTables = req.IncludeTables
	}

	// Build options object for JavaScript
	options := map[string]interface{}{
		"strategy":        strategy,
		"includeMetadata": includeMetadata,
		"includeLinks":    includeLinks,
		"includeTables":   includeTables,
		"maxLength":       req.MaxLength,
	}

	// Execute the markdown extraction script
	script := fmt.Sprintf("(%s)(%s)", markdownExtractionScript, toJSON(options))

	var result map[string]interface{}
	if err := chromedp.Run(opCtx, chromedp.Evaluate(script, &result)); err != nil {
		return nil, shared.ErrBrowserAction("get_markdown", err)
	}

	// Parse the result
	markdown, _ := result["markdown"].(string)
	warnings := []string{}
	if w, ok := result["warnings"].([]interface{}); ok {
		for _, item := range w {
			if s, ok := item.(string); ok {
				warnings = append(warnings, s)
			}
		}
	}

	// Parse metadata if present
	var metadata *shared.MarkdownMetadata
	if m, ok := result["metadata"].(map[string]interface{}); ok && m != nil {
		metadata = &shared.MarkdownMetadata{
			Title:              getString(m, "title"),
			URL:                getString(m, "url"),
			Language:           getString(m, "language"),
			ExtractionStrategy: getString(m, "extraction_strategy"),
			Timestamp:          getString(m, "timestamp"),
		}
	}

	return &shared.GetMarkdownResult{
		Markdown: markdown,
		Metadata: metadata,
		Warnings: warnings,
	}, nil
}

// toJSON converts a map to JSON string for use in JavaScript
func toJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// getString safely gets a string from a map
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// Hover hovers over an element
func (c *Client) Hover(ctx context.Context, req shared.HoverRequest) (*shared.HoverResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	timeout := c.config.DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Millisecond
	}

	opCtx, cancel := c.actionContext(ctx, timeout)
	defer cancel()

	if err := Hover(opCtx, req.Selector); err != nil {
		return nil, shared.ErrBrowserAction("hover", err)
	}

	return &shared.HoverResult{
		Hovered:  true,
		Selector: req.Selector,
	}, nil
}

// PressKey sends keyboard input
func (c *Client) PressKey(ctx context.Context, req shared.PressKeyRequest) (*shared.PressKeyResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	// Focus element if selector provided
	if req.Selector != "" {
		if err := FocusElement(opCtx, req.Selector); err != nil {
			return nil, shared.ErrBrowserAction("focus", err)
		}
	}

	repeat := req.Repeat
	if repeat == 0 {
		repeat = 1
	}

	if err := PressKey(opCtx, req.Key, repeat); err != nil {
		return nil, shared.ErrBrowserAction("press_key", err)
	}

	return &shared.PressKeyResult{
		Key:        req.Key,
		Dispatched: true,
	}, nil
}

// DragDrop performs drag-and-drop between two elements
func (c *Client) DragDrop(ctx context.Context, req shared.DragDropRequest) (*shared.DragDropResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	if err := DragDrop(opCtx, req.SourceSelector, req.TargetSelector); err != nil {
		return nil, shared.ErrBrowserAction("drag_drop", err)
	}

	return &shared.DragDropResult{
		Success: true,
		Source:  req.SourceSelector,
		Target:  req.TargetSelector,
	}, nil
}

// Assert performs a DOM assertion
func (c *Client) Assert(ctx context.Context, req shared.AssertRequest) (*shared.AssertResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	result := AssertDOM(opCtx, AssertionType(req.Assertion), req.Selector, req.Attribute, req.Expected)

	return &shared.AssertResult{
		Pass:      result.Pass,
		Assertion: result.Assertion,
		Actual:    result.Actual,
		Expected:  result.Expected,
		Message:   result.Message,
	}, nil
}

// ListFrames returns all frames on the current page
func (c *Client) ListFrames(ctx context.Context) ([]*BrowserFrameInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return nil, err
	}

	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	return GetFrameTree(opCtx)
}

// SwitchFrame is currently a no-op placeholder — frame context switching
// requires deeper integration with chromedp's target management.
// For now, the manager tracks frame state for informational purposes.
func (c *Client) SwitchFrame(ctx context.Context, selector string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.requireSessionLocked(); err != nil {
		return err
	}

	// Verify the frame exists
	opCtx, cancel := c.actionContext(ctx, c.config.DefaultTimeout)
	defer cancel()

	var exists bool
	chromedp.Run(opCtx, chromedp.Evaluate(fmt.Sprintf(`!!document.querySelector(%q)`, selector), &exists))
	if !exists {
		return fmt.Errorf("frame not found: %s", selector)
	}

	return nil
}

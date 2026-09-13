package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/config"
	bufferclient "github.com/agentmaurice/mcpchatui/mcp/browser/internal/integrations/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// ScreenshotTool handles screenshot requests
type ScreenshotTool struct {
	browserManager *business.BrowserManager
	bufferDefaults config.BufferConfig
	logger         *zap.Logger
}

type screenshotBufferRuntimeOptions struct {
	Enabled            bool
	URL                string
	AuthToken          string
	Namespace          string
	Summary            string
	SoftThresholdBytes int
	HardThresholdBytes int
	MaxUploadBytes     int
	TTLSeconds         int
	NetworkRetries     int
	Timeout            time.Duration
}

// NewScreenshotTool creates a new screenshot tool
func NewScreenshotTool(browserManager *business.BrowserManager, bufferCfg *config.BufferConfig, logger *zap.Logger) *ScreenshotTool {
	defaults := config.BufferConfig{
		Enabled:            false,
		Namespace:          "browser",
		SoftThresholdBytes: 500000,
		HardThresholdBytes: 1000000,
		MaxUploadBytes:     20000000,
		TTLSeconds:         600,
		NetworkRetries:     1,
		Timeout:            10 * time.Second,
	}
	if bufferCfg != nil {
		defaults = *bufferCfg
	}

	return &ScreenshotTool{
		browserManager: browserManager,
		bufferDefaults: defaults,
		logger:         logger.Named("tool-screenshot"),
	}
}

// Definition returns the tool definition
func (t *ScreenshotTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_screenshot",
		Description: "Take a screenshot of the current page or a specific element. Returns base64 encoded image data.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for a specific element to screenshot. Optional if ref is provided.",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Optional if selector is provided.",
				},
				"full_page": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to capture the full scrollable page (default: false)",
				},
				"format": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"png", "jpeg"},
					"description": "Image format (default: png)",
				},
				"quality": map[string]interface{}{
					"type":        "integer",
					"description": "JPEG quality 0-100 (default: 80, only used for jpeg format)",
				},
				"buffer_options": map[string]interface{}{
					"type":        "object",
					"description": "Optional buffer upload options. If enabled=true and the screenshot exceeds soft_threshold_bytes, image payload is uploaded to buffer and replaced by a buffer reference.",
					"properties": map[string]interface{}{
						"enabled": map[string]interface{}{
							"type":        "boolean",
							"description": "Enable buffering for this screenshot call.",
						},
						"url": map[string]interface{}{
							"type":        "string",
							"description": "Buffer service base URL (e.g. http://localhost:3001).",
						},
						"auth_token": map[string]interface{}{
							"type":        "string",
							"description": "Bearer token used to authenticate on buffer service. Required when enabled=true.",
						},
						"namespace": map[string]interface{}{
							"type":        "string",
							"description": "Buffer namespace (default: browser).",
						},
						"summary": map[string]interface{}{
							"type":        "string",
							"description": "Optional summary saved with the buffered content.",
						},
						"soft_threshold_bytes": map[string]interface{}{
							"type":        "integer",
							"description": "Start buffering when payload size is above this threshold.",
						},
						"hard_threshold_bytes": map[string]interface{}{
							"type":        "integer",
							"description": "Warn when payload is above this threshold and still sent inline.",
						},
						"max_upload_bytes": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum allowed upload request payload.",
						},
						"ttl_seconds": map[string]interface{}{
							"type":        "integer",
							"description": "Buffer entry TTL in seconds.",
						},
						"network_retries": map[string]interface{}{
							"type":        "integer",
							"description": "Network retry count for buffer upload.",
						},
						"timeout_ms": map[string]interface{}{
							"type":        "integer",
							"description": "Buffer HTTP timeout in milliseconds.",
						},
					},
				},
			},
		},
	}
}

// Handler returns the tool handler function
func (t *ScreenshotTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		selector, _ := getStringArg(args, "selector", false)
		ref, _ := getStringArg(args, "ref", false)
		fullPage, _ := getBoolArg(args, "full_page", false)
		format, _ := getStringArg(args, "format", false)
		quality, _ := getIntArg(args, "quality", 0)

		bufferOptions, err := t.resolveBufferOptions(args)
		if err != nil {
			return createErrorResult(err), nil
		}

		t.logger.Debug("executing screenshot",
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.Bool("full_page", fullPage),
			zap.String("format", format),
			zap.Bool("buffer_enabled", bufferOptions.Enabled))

		result, err := t.browserManager.Screenshot(ctx, shared.ScreenshotRequest{
			Selector: selector,
			Ref:      ref,
			FullPage: fullPage,
			Format:   format,
			Quality:  quality,
		})
		if err != nil {
			t.logger.Error("screenshot failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		// Return as image content
		mimeType := "image/png"
		if result.Format == "jpeg" {
			mimeType = "image/jpeg"
		}

		inlineResult := createImageResult(result.Data, mimeType)
		inlinePayloadBytes := mcpResultByteSize(inlineResult)
		if !bufferOptions.Enabled || inlinePayloadBytes <= bufferOptions.SoftThresholdBytes {
			if bufferOptions.Enabled && inlinePayloadBytes > bufferOptions.HardThresholdBytes {
				t.logger.Warn("screenshot payload above hard threshold and sent inline",
					zap.Int("payload_bytes", inlinePayloadBytes),
					zap.Int("hard_threshold_bytes", bufferOptions.HardThresholdBytes),
				)
			}
			return inlineResult, nil
		}

		uploadClient := bufferclient.NewClient(bufferclient.Options{
			BaseURL:        bufferOptions.URL,
			AuthToken:      bufferOptions.AuthToken,
			MaxUploadBytes: bufferOptions.MaxUploadBytes,
			NetworkRetries: bufferOptions.NetworkRetries,
			Timeout:        bufferOptions.Timeout,
		}, t.logger)

		refResult, err := uploadClient.Upload(ctx, bufferclient.UploadRequest{
			Namespace:  bufferOptions.Namespace,
			ToolName:   "browser_screenshot",
			MimeType:   mimeType,
			Text:       result.Data,
			Summary:    summarizeBufferedScreenshot(bufferOptions.Summary, inlinePayloadBytes),
			TTLSeconds: bufferOptions.TTLSeconds,
			Metadata: map[string]any{
				"content_type":  "image",
				"payload_bytes": inlinePayloadBytes,
				"width":         result.Width,
				"height":        result.Height,
				"format":        result.Format,
			},
		})
		if err != nil {
			var uploadErr *bufferclient.UploadError
			if errors.As(err, &uploadErr) {
				if uploadErr != nil && uploadErr.Reason == bufferclient.FailureTooLarge {
					return createBufferedTruncatedResult("browser_screenshot", inlinePayloadBytes, mimeType, len(result.Data)), nil
				}

				t.logger.Warn("buffer upload failed, falling back to inline screenshot",
					zap.String("reason", string(uploadErr.Reason)),
					zap.Int("status_code", uploadErr.StatusCode),
					zap.String("message", uploadErr.Message),
				)
				return inlineResult, nil
			}

			t.logger.Warn("buffer upload failed with unknown error, falling back to inline screenshot", zap.Error(err))
			return inlineResult, nil
		}

		return createBufferedReferenceResult("browser_screenshot", refResult, inlinePayloadBytes), nil
	}
}

func (t *ScreenshotTool) resolveBufferOptions(args map[string]interface{}) (screenshotBufferRuntimeOptions, error) {
	defaults := t.bufferDefaults
	opts := screenshotBufferRuntimeOptions{
		Enabled:            defaults.Enabled,
		URL:                strings.TrimSpace(defaults.URL),
		Namespace:          strings.TrimSpace(defaults.Namespace),
		SoftThresholdBytes: defaults.SoftThresholdBytes,
		HardThresholdBytes: defaults.HardThresholdBytes,
		MaxUploadBytes:     defaults.MaxUploadBytes,
		TTLSeconds:         defaults.TTLSeconds,
		NetworkRetries:     defaults.NetworkRetries,
		Timeout:            defaults.Timeout,
	}

	if opts.Namespace == "" {
		opts.Namespace = "browser"
	}
	if opts.SoftThresholdBytes <= 0 {
		opts.SoftThresholdBytes = 500000
	}
	if opts.HardThresholdBytes < opts.SoftThresholdBytes {
		opts.HardThresholdBytes = opts.SoftThresholdBytes
	}
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = 20000000
	}
	if opts.TTLSeconds <= 0 {
		opts.TTLSeconds = 600
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.NetworkRetries < 0 {
		opts.NetworkRetries = 0
	}

	bufferArgs, err := getObjectArg(args, "buffer_options", false)
	if err != nil {
		return opts, err
	}
	if bufferArgs == nil {
		// Buffer auth is expected from call options, so buffering is opt-in per request.
		opts.Enabled = false
		return opts, nil
	}

	if enabled, err := getBoolArg(bufferArgs, "enabled", opts.Enabled); err == nil {
		opts.Enabled = enabled
	}
	if url, err := getStringArg(bufferArgs, "url", false); err == nil && strings.TrimSpace(url) != "" {
		opts.URL = strings.TrimSpace(url)
	}
	if namespace, err := getStringArg(bufferArgs, "namespace", false); err == nil && strings.TrimSpace(namespace) != "" {
		opts.Namespace = strings.TrimSpace(namespace)
	}
	if summary, err := getStringArg(bufferArgs, "summary", false); err == nil {
		opts.Summary = strings.TrimSpace(summary)
	}
	if soft, err := getIntArg(bufferArgs, "soft_threshold_bytes", opts.SoftThresholdBytes); err == nil && soft > 0 {
		opts.SoftThresholdBytes = soft
	}
	if hard, err := getIntArg(bufferArgs, "hard_threshold_bytes", opts.HardThresholdBytes); err == nil && hard > 0 {
		opts.HardThresholdBytes = hard
	}
	if maxUpload, err := getIntArg(bufferArgs, "max_upload_bytes", opts.MaxUploadBytes); err == nil && maxUpload > 0 {
		opts.MaxUploadBytes = maxUpload
	}
	if ttl, err := getIntArg(bufferArgs, "ttl_seconds", opts.TTLSeconds); err == nil && ttl > 0 {
		opts.TTLSeconds = ttl
	}
	if retries, err := getIntArg(bufferArgs, "network_retries", opts.NetworkRetries); err == nil && retries >= 0 {
		opts.NetworkRetries = retries
	}
	if timeoutMS, err := getIntArg(bufferArgs, "timeout_ms", 0); err == nil && timeoutMS > 0 {
		opts.Timeout = time.Duration(timeoutMS) * time.Millisecond
	}

	// Buffer auth must be provided in tool options when buffering is enabled.
	if opts.Enabled {
		authToken, err := getStringArg(bufferArgs, "auth_token", true)
		if err != nil {
			return opts, fmt.Errorf("buffer_options.auth_token is required when buffer_options.enabled=true")
		}
		opts.AuthToken = strings.TrimSpace(authToken)
		if opts.AuthToken == "" {
			return opts, fmt.Errorf("buffer_options.auth_token cannot be empty when buffering is enabled")
		}
		if opts.URL == "" {
			return opts, fmt.Errorf("buffer_options.url is required when buffering is enabled")
		}
	}

	if opts.HardThresholdBytes < opts.SoftThresholdBytes {
		opts.HardThresholdBytes = opts.SoftThresholdBytes
	}

	return opts, nil
}

func mcpResultByteSize(result *mcp.CallToolResult) int {
	if result == nil {
		return 0
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func summarizeBufferedScreenshot(summary string, payloadBytes int) string {
	if strings.TrimSpace(summary) != "" {
		return strings.TrimSpace(summary)
	}
	kib := payloadBytes / 1024
	if kib <= 0 {
		kib = 1
	}
	return fmt.Sprintf("browser_screenshot output (%d KB)", kib)
}

func createBufferedReferenceResult(toolName string, reference *bufferclient.BufferReference, payloadBytes int) *mcp.CallToolResult {
	summary := summarizeBufferedScreenshot(reference.Summary, payloadBytes)
	text := fmt.Sprintf("[Buffered] %s stored in buffer %s", summary, reference.Key)

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(text),
		},
		StructuredContent: map[string]any{
			"status": "succeeded",
			"outputs": []map[string]any{
				{
					"tool_name":  toolName,
					"ref":        "buffer://" + reference.Key,
					"mime_type":  reference.MimeType,
					"summary":    summary,
					"size_bytes": reference.Size,
					"expires_in": reference.ExpiresIn,
				},
			},
		},
	}
}

func createBufferedTruncatedResult(toolName string, payloadBytes int, mimeType string, base64Bytes int) *mcp.CallToolResult {
	kib := payloadBytes / 1024
	if kib <= 0 {
		kib = 1
	}

	text := fmt.Sprintf(
		"[Truncated] Buffer upload rejected (413) for %s. Original payload: %d KB. image omitted: %s, base64_bytes=%d",
		toolName,
		kib,
		mimeType,
		base64Bytes,
	)
	return mcp.NewToolResultText(text)
}

// ScrollTool handles scroll requests
type ScrollTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewScrollTool creates a new scroll tool
func NewScrollTool(browserManager *business.BrowserManager, logger *zap.Logger) *ScrollTool {
	return &ScrollTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-scroll"),
	}
}

// Definition returns the tool definition
func (t *ScrollTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_scroll",
		Description: "Scroll the page or a specific element to a position",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for element to scroll within. If empty, scrolls the window.",
				},
				"x": map[string]interface{}{
					"type":        "integer",
					"description": "Horizontal scroll position in pixels (default: 0)",
				},
				"y": map[string]interface{}{
					"type":        "integer",
					"description": "Vertical scroll position in pixels (default: 0)",
				},
				"behavior": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"smooth", "instant"},
					"description": "Scroll behavior (default: instant)",
				},
			},
		},
	}
}

// Handler returns the tool handler function
func (t *ScrollTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		selector, _ := getStringArg(args, "selector", false)
		x, _ := getIntArg(args, "x", 0)
		y, _ := getIntArg(args, "y", 0)
		behavior, _ := getStringArg(args, "behavior", false)

		t.logger.Debug("executing scroll",
			zap.String("selector", selector),
			zap.Int("x", x),
			zap.Int("y", y))

		result, err := t.browserManager.Scroll(ctx, shared.ScrollRequest{
			Selector: selector,
			X:        x,
			Y:        y,
			Behavior: behavior,
		})
		if err != nil {
			t.logger.Error("scroll failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

// EvaluateTool handles JavaScript evaluation requests
type EvaluateTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewEvaluateTool creates a new evaluate tool
func NewEvaluateTool(browserManager *business.BrowserManager, logger *zap.Logger) *EvaluateTool {
	return &EvaluateTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-evaluate"),
	}
}

// Definition returns the tool definition
func (t *EvaluateTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_evaluate",
		Description: "Execute JavaScript code in the browser page context. Returns the result of the expression.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"script": map[string]interface{}{
					"type":        "string",
					"description": "JavaScript code to execute. Supports expressions and statement blocks; top-level 'return' is also accepted.",
				},
			},
			Required: []string{"script"},
		},
	}
}

// Handler returns the tool handler function
func (t *EvaluateTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		script, err := getStringArg(args, "script", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		t.logger.Debug("executing evaluate")

		result, err := t.browserManager.Evaluate(ctx, shared.EvaluateRequest{
			Script: script,
		})
		if err != nil {
			t.logger.Error("evaluate failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		// Format the result
		if result.Result == nil {
			return createTextResult(map[string]interface{}{
				"result": nil,
			})
		}

		return createTextResult(map[string]interface{}{
			"result": fmt.Sprintf("%v", result.Result),
		})
	}
}

// WaitForSelectorTool handles wait for selector requests
type WaitForSelectorTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewWaitForSelectorTool creates a new wait for selector tool
func NewWaitForSelectorTool(browserManager *business.BrowserManager, logger *zap.Logger) *WaitForSelectorTool {
	return &WaitForSelectorTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-wait-for-selector"),
	}
}

// Definition returns the tool definition
func (t *WaitForSelectorTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_wait_for_selector",
		Description: "Wait for an element matching the selector to appear or disappear from the page",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector to wait for. Optional if ref is provided.",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Optional if selector is provided.",
				},
				"state": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"visible", "hidden", "attached", "detached"},
					"description": "State to wait for (default: visible)",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in milliseconds (default: 30000)",
				},
			},
		},
	}
}

// Handler returns the tool handler function
func (t *WaitForSelectorTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		selector, _ := getStringArg(args, "selector", false)
		ref, _ := getStringArg(args, "ref", false)
		if selector == "" && ref == "" {
			return createErrorResult(shared.ErrValidation("either selector or ref is required")), nil
		}

		state, _ := getStringArg(args, "state", false)
		timeout, _ := getIntArg(args, "timeout", 0)

		t.logger.Debug("executing wait for selector",
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.String("state", state))

		result, err := t.browserManager.WaitForSelector(ctx, shared.WaitForSelectorRequest{
			Selector: selector,
			Ref:      ref,
			State:    state,
			Timeout:  timeout,
		})
		if err != nil {
			t.logger.Error("wait for selector failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

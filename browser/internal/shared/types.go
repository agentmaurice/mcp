package shared

// NavigateRequest represents a navigation request
type NavigateRequest struct {
	URL     string `json:"url"`
	Timeout int    `json:"timeout,omitempty"` // in milliseconds
}

// NavigateResult represents the result of navigation
type NavigateResult struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Status int    `json:"status,omitempty"`
}

// ClickRequest represents a click action request
type ClickRequest struct {
	Selector string `json:"selector"`
	Ref      string `json:"ref,omitempty"`     // element reference from snapshot (e.g. @e1)
	Timeout  int    `json:"timeout,omitempty"` // in milliseconds
}

// ClickResult represents the result of a click action
type ClickResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// FillRequest represents a form fill request
type FillRequest struct {
	Selector string `json:"selector"`
	Ref      string `json:"ref,omitempty"` // element reference from snapshot (e.g. @e1)
	Value    string `json:"value"`
	Clear    bool   `json:"clear,omitempty"` // clear before typing
	Timeout  int    `json:"timeout,omitempty"`
}

// FillResult represents the result of a fill action
type FillResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// GetTextRequest represents a text extraction request
type GetTextRequest struct {
	Selector string `json:"selector"`
	Ref      string `json:"ref,omitempty"` // element reference from snapshot (e.g. @e1)
	Timeout  int    `json:"timeout,omitempty"`
}

// GetTextResult represents the result of text extraction
type GetTextResult struct {
	Text     string `json:"text"`
	Selector string `json:"selector"`
}

// ScreenshotRequest represents a screenshot request
type ScreenshotRequest struct {
	Selector string `json:"selector,omitempty"` // if empty, full page
	Ref      string `json:"ref,omitempty"`      // element reference from snapshot (e.g. @e1)
	Format   string `json:"format,omitempty"`   // png or jpeg
	Quality  int    `json:"quality,omitempty"`  // jpeg quality 0-100
	FullPage bool   `json:"full_page,omitempty"`
}

// ScreenshotResult represents the result of a screenshot
type ScreenshotResult struct {
	Data   string `json:"data"` // base64 encoded
	Format string `json:"format"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// GetHTMLRequest represents an HTML extraction request
type GetHTMLRequest struct {
	Selector string `json:"selector,omitempty"` // if empty, full page
	Outer    bool   `json:"outer,omitempty"`    // outer or inner HTML
}

// GetHTMLResult represents the result of HTML extraction
type GetHTMLResult struct {
	HTML     string `json:"html"`
	Selector string `json:"selector,omitempty"`
}

// WaitForSelectorRequest represents a wait for selector request
type WaitForSelectorRequest struct {
	Selector string `json:"selector"`
	Ref      string `json:"ref,omitempty"`   // element reference from snapshot (e.g. @e1)
	State    string `json:"state,omitempty"` // visible, hidden, attached, detached
	Timeout  int    `json:"timeout,omitempty"`
}

// WaitForSelectorResult represents the result of waiting
type WaitForSelectorResult struct {
	Found   bool   `json:"found"`
	Message string `json:"message,omitempty"`
}

// EvaluateRequest represents a JavaScript evaluation request
type EvaluateRequest struct {
	Script string `json:"script"`
}

// EvaluateResult represents the result of JS evaluation
type EvaluateResult struct {
	Result interface{} `json:"result"`
}

// SelectOptionRequest represents a select option request
type SelectOptionRequest struct {
	Selector string   `json:"selector"`
	Ref      string   `json:"ref,omitempty"` // element reference from snapshot (e.g. @e1)
	Values   []string `json:"values"`        // values to select
	Timeout  int      `json:"timeout,omitempty"`
}

// SelectOptionResult represents the result of select option
type SelectOptionResult struct {
	Selected []string `json:"selected"`
	Success  bool     `json:"success"`
}

// GetAttributeRequest represents an attribute extraction request
type GetAttributeRequest struct {
	Selector  string `json:"selector"`
	Ref       string `json:"ref,omitempty"` // element reference from snapshot (e.g. @e1)
	Attribute string `json:"attribute"`
	Timeout   int    `json:"timeout,omitempty"`
}

// GetAttributeResult represents the result of attribute extraction
type GetAttributeResult struct {
	Value    string `json:"value"`
	Exists   bool   `json:"exists"`
	Selector string `json:"selector"`
}

// ScrollRequest represents a scroll request
type ScrollRequest struct {
	Selector string `json:"selector,omitempty"` // if empty, scroll window
	X        int    `json:"x,omitempty"`
	Y        int    `json:"y,omitempty"`
	Behavior string `json:"behavior,omitempty"` // smooth or instant
}

// ScrollResult represents the result of scrolling
type ScrollResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// SnapshotRequest captures a compact semantic snapshot of interactive elements.
type SnapshotRequest struct {
	Format        string `json:"format,omitempty"`         // compact or json (default: compact)
	MaxElements   int    `json:"max_elements,omitempty"`   // optional cap, default 200
	IncludeHidden bool   `json:"include_hidden,omitempty"` // include hidden elements
}

// SnapshotElement is one interactive element exposed to the LLM with a compact ref.
type SnapshotElement struct {
	Ref         string `json:"ref"`
	Selector    string `json:"selector"`
	Role        string `json:"role,omitempty"`
	Name        string `json:"name,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	TestID      string `json:"test_id,omitempty"`
	Title       string `json:"title,omitempty"`
	Alt         string `json:"alt,omitempty"`
}

// SnapshotResult is the result of a page snapshot.
type SnapshotResult struct {
	SnapshotID string            `json:"snapshot_id"`
	URL        string            `json:"url"`
	Title      string            `json:"title"`
	Format     string            `json:"format"`
	Total      int               `json:"total"`
	Truncated  bool              `json:"truncated"`
	Elements   []SnapshotElement `json:"elements"`
}

// FindRequest searches semantic elements in the current snapshot.
type FindRequest struct {
	By         string `json:"by"`                    // role, text, label, placeholder, testid, title, alt
	Value      string `json:"value"`                 // query value (or role when by=role)
	Name       string `json:"name,omitempty"`        // optional name query for by=role
	Exact      bool   `json:"exact,omitempty"`       // exact or contains match
	MaxResults int    `json:"max_results,omitempty"` // default 10
}

// FindMatch is a semantic match in the snapshot.
type FindMatch struct {
	Ref      string  `json:"ref"`
	Score    float64 `json:"score"`
	Role     string  `json:"role,omitempty"`
	Name     string  `json:"name,omitempty"`
	Selector string  `json:"selector,omitempty"`
}

// FindResult is the result of a semantic find operation.
type FindResult struct {
	By      string      `json:"by"`
	Value   string      `json:"value"`
	Name    string      `json:"name,omitempty"`
	Count   int         `json:"count"`
	Matches []FindMatch `json:"matches"`
}

// PageInfo represents information about the current page
type PageInfo struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// GetMarkdownRequest represents a markdown extraction request
type GetMarkdownRequest struct {
	Strategy        string `json:"strategy,omitempty"`         // auto, article, dom, accessibility
	IncludeMetadata bool   `json:"include_metadata,omitempty"` // default: true
	IncludeLinks    bool   `json:"include_links,omitempty"`    // default: true
	IncludeTables   bool   `json:"include_tables,omitempty"`   // default: true
	MaxLength       int    `json:"max_length,omitempty"`       // optional truncation
}

// GetMarkdownResult represents the result of markdown extraction
type GetMarkdownResult struct {
	Markdown string            `json:"markdown"`
	Metadata *MarkdownMetadata `json:"metadata,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
}

// MarkdownMetadata contains page metadata for markdown extraction
type MarkdownMetadata struct {
	Title              string `json:"title"`
	URL                string `json:"url"`
	Language           string `json:"language,omitempty"`
	ExtractionStrategy string `json:"extraction_strategy"`
	Timestamp          string `json:"timestamp"`
}

// ============= Visual Diffing =============

// CaptureRequest represents a named screenshot capture request
type CaptureRequest struct {
	Name     string `json:"name"`
	Selector string `json:"selector,omitempty"`
	Ref      string `json:"ref,omitempty"`
	FullPage bool   `json:"full_page,omitempty"`
}

// CaptureResult represents the result of a capture
type CaptureResult struct {
	CaptureID  string         `json:"capture_id"`
	Dimensions ImageDimensions `json:"dimensions"`
	Timestamp  string         `json:"timestamp"`
}

// ImageDimensions represents width/height of an image
type ImageDimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// VisualDiffRequest represents a visual diff request
type VisualDiffRequest struct {
	Before    string  `json:"before"`
	After     string  `json:"after"`
	Selector  string  `json:"selector,omitempty"`
	Ref       string  `json:"ref,omitempty"`
	Threshold float64 `json:"threshold,omitempty"`
	Format    string  `json:"format,omitempty"`
}

// VisualDiffRegion represents a bounding box of changed pixels
type VisualDiffRegion struct {
	X              int     `json:"x"`
	Y              int     `json:"y"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	DiffPercentage float64 `json:"diff_percentage"`
}

// VisualDiffResult represents the result of a visual diff
type VisualDiffResult struct {
	Match          bool              `json:"match"`
	DiffPercentage float64           `json:"diff_percentage"`
	DiffPixelCount int               `json:"diff_pixel_count"`
	Dimensions     ImageDimensions   `json:"dimensions"`
	Regions        []VisualDiffRegion `json:"regions,omitempty"`
}

// ============= Network Interception =============

// NetworkCaptureStartRequest represents a request to start network capture
type NetworkCaptureStartRequest struct {
	FilterURL    string `json:"filter_url,omitempty"`
	FilterMethod string `json:"filter_method,omitempty"`
	IncludeBody  bool   `json:"include_body,omitempty"`
	MaxEntries   int    `json:"max_entries,omitempty"`
}

// NetworkCaptureStartResult represents the result of starting capture
type NetworkCaptureStartResult struct {
	CaptureID string `json:"capture_id"`
	Status    string `json:"status"`
}

// NetworkCaptureStopRequest represents a request to stop capture
type NetworkCaptureStopRequest struct {
	Format string `json:"format,omitempty"`
}

// NetworkEntry represents a single captured network request
type NetworkEntry struct {
	URL                   string            `json:"url"`
	Method                string            `json:"method"`
	Status                int               `json:"status"`
	ContentType           string            `json:"content_type"`
	DurationMs            int               `json:"duration_ms"`
	SizeBytes             int               `json:"size_bytes"`
	RequestHeaders        map[string]string `json:"request_headers,omitempty"`
	ResponseHeaders       map[string]string `json:"response_headers,omitempty"`
	RequestBody           string            `json:"request_body,omitempty"`
	ResponseBody          string            `json:"response_body,omitempty"`
	ResponseBodyTruncated bool              `json:"response_body_truncated,omitempty"`
}

// NetworkStats represents aggregate network capture statistics
type NetworkStats struct {
	TotalRequests int            `json:"total_requests"`
	ByMethod      map[string]int `json:"by_method"`
	ByStatus      map[string]int `json:"by_status"`
	AvgDurationMs int            `json:"avg_duration_ms"`
}

// NetworkCaptureStopResult represents the result of stopping capture
type NetworkCaptureStopResult struct {
	Entries []NetworkEntry `json:"entries"`
	Stats   NetworkStats   `json:"stats"`
}

// NetworkMockRequest represents a request to install a network mock
type NetworkMockRequest struct {
	URLPattern      string            `json:"url_pattern"`
	Method          string            `json:"method,omitempty"`
	ResponseStatus  int               `json:"response_status,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody    string            `json:"response_body,omitempty"`
	ContentType     string            `json:"content_type,omitempty"`
	Once            bool              `json:"once,omitempty"`
}

// NetworkMockResult represents the result of installing a mock
type NetworkMockResult struct {
	MockID     string `json:"mock_id"`
	URLPattern string `json:"url_pattern"`
	Active     bool   `json:"active"`
}

// NetworkMockClearRequest represents a request to clear mocks
type NetworkMockClearRequest struct {
	MockID string `json:"mock_id,omitempty"`
}

// NetworkMockClearResult represents the result of clearing mocks
type NetworkMockClearResult struct {
	Cleared   []string `json:"cleared"`
	Remaining int      `json:"remaining"`
}

// ============= Interactions =============

// HoverRequest represents a hover action request
type HoverRequest struct {
	Selector string `json:"selector,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Timeout  int    `json:"timeout,omitempty"`
}

// HoverResult represents the result of a hover action
type HoverResult struct {
	Hovered  bool   `json:"hovered"`
	Selector string `json:"selector"`
}

// DragDropRequest represents a drag-and-drop action request
type DragDropRequest struct {
	SourceSelector string `json:"source_selector,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	TargetSelector string `json:"target_selector,omitempty"`
	TargetRef      string `json:"target_ref,omitempty"`
	Timeout        int    `json:"timeout,omitempty"`
}

// DragDropResult represents the result of a drag-and-drop action
type DragDropResult struct {
	Success bool   `json:"success"`
	Source  string `json:"source"`
	Target  string `json:"target"`
}

// PressKeyRequest represents a key press request
type PressKeyRequest struct {
	Key      string `json:"key"`
	Selector string `json:"selector,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Repeat   int    `json:"repeat,omitempty"`
}

// PressKeyResult represents the result of a key press
type PressKeyResult struct {
	Key        string `json:"key"`
	Dispatched bool   `json:"dispatched"`
}

// AssertRequest represents a DOM assertion request
type AssertRequest struct {
	Assertion string `json:"assertion"`
	Selector  string `json:"selector,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Expected  string `json:"expected,omitempty"`
	Attribute string `json:"attribute,omitempty"`
	Timeout   int    `json:"timeout,omitempty"`
}

// AssertResult represents the result of a DOM assertion
type AssertResult struct {
	Pass      bool   `json:"pass"`
	Assertion string `json:"assertion"`
	Actual    string `json:"actual,omitempty"`
	Expected  string `json:"expected,omitempty"`
	Message   string `json:"message"`
}

// ============= Frames =============

// SwitchFrameRequest represents a request to switch frame context
type SwitchFrameRequest struct {
	Selector string `json:"selector,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Name     string `json:"name,omitempty"`
}

// SwitchFrameResult represents the result of switching frame
type SwitchFrameResult struct {
	Frame string `json:"frame"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// FrameInfo represents information about a single frame
type FrameInfo struct {
	Selector string `json:"selector"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Visible  bool   `json:"visible"`
}

// ListFramesResult represents the result of listing frames
type ListFramesResult struct {
	Frames []FrameInfo `json:"frames"`
}

// BrowserStatus represents the current status of the browser connection
type BrowserStatus struct {
	ManagerRunning   bool   `json:"manager_running"`
	BrowserConnected bool   `json:"browser_connected"`
	CDPEndpoint      string `json:"cdp_endpoint"`
	SnapshotID       string `json:"snapshot_id,omitempty"`
	SnapshotURL      string `json:"snapshot_url,omitempty"`
	RefCount         int    `json:"ref_count,omitempty"`
	PoolMode         string `json:"pool_mode,omitempty"`
	PoolSize         int    `json:"pool_size,omitempty"`
	ActiveSessions   int    `json:"active_sessions,omitempty"`
	SessionKey       string `json:"session_key,omitempty"`
}

package shared

import "context"

// Manager interface for lifecycle management
type Manager interface {
	Start(ctx context.Context) error
	Stop() error
	Name() string
	Health(ctx context.Context) error
}

// BrowserClient interface for browser operations
type BrowserClient interface {
	// Connect establishes connection to the browser
	Connect(ctx context.Context) error

	// Disconnect closes the browser connection
	Disconnect() error

	// IsConnected checks if browser is connected
	IsConnected() bool

	// Navigation
	Navigate(ctx context.Context, req NavigateRequest) (*NavigateResult, error)
	GoBack(ctx context.Context) error
	GoForward(ctx context.Context) error
	Reload(ctx context.Context) error

	// Page info
	GetPageInfo(ctx context.Context) (*PageInfo, error)
	GetTitle(ctx context.Context) (string, error)
	GetURL(ctx context.Context) (string, error)

	// Interactions
	Click(ctx context.Context, req ClickRequest) (*ClickResult, error)
	Fill(ctx context.Context, req FillRequest) (*FillResult, error)
	SelectOption(ctx context.Context, req SelectOptionRequest) (*SelectOptionResult, error)
	Scroll(ctx context.Context, req ScrollRequest) (*ScrollResult, error)

	// Content extraction
	GetText(ctx context.Context, req GetTextRequest) (*GetTextResult, error)
	GetHTML(ctx context.Context, req GetHTMLRequest) (*GetHTMLResult, error)
	GetAttribute(ctx context.Context, req GetAttributeRequest) (*GetAttributeResult, error)

	// Screenshots
	Screenshot(ctx context.Context, req ScreenshotRequest) (*ScreenshotResult, error)

	// JavaScript
	Evaluate(ctx context.Context, req EvaluateRequest) (*EvaluateResult, error)

	// Waiting
	WaitForSelector(ctx context.Context, req WaitForSelectorRequest) (*WaitForSelectorResult, error)
	WaitForNavigation(ctx context.Context, timeout int) error

	// Snapshotting
	Snapshot(ctx context.Context, req SnapshotRequest) (*SnapshotResult, error)

	// Markdown extraction
	GetMarkdown(ctx context.Context, req GetMarkdownRequest) (*GetMarkdownResult, error)

	// Advanced interactions
	Hover(ctx context.Context, req HoverRequest) (*HoverResult, error)
	PressKey(ctx context.Context, req PressKeyRequest) (*PressKeyResult, error)
	DragDrop(ctx context.Context, req DragDropRequest) (*DragDropResult, error)
	Assert(ctx context.Context, req AssertRequest) (*AssertResult, error)
}

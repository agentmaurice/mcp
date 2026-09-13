package browser

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// BrowserFrameInfo contains info about a single frame in the page
type BrowserFrameInfo struct {
	Selector string
	Name     string
	URL      string
	Visible  bool
}

// GetFrameTree returns all frames on the page
func GetFrameTree(ctx context.Context) ([]*BrowserFrameInfo, error) {
	var tree *page.FrameTree

	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		tree, err = page.GetFrameTree().Do(ctx)
		return err
	})); err != nil {
		return nil, fmt.Errorf("failed to get frame tree: %w", err)
	}

	var frames []*BrowserFrameInfo
	processFrame(ctx, tree, &frames)
	return frames, nil
}

func processFrame(ctx context.Context, ft *page.FrameTree, frames *[]*BrowserFrameInfo) {
	if ft == nil {
		return
	}

	// Skip top-level frame (only list child frames / iframes)
	if ft.Frame.ParentID != "" {
		selector := findIFrameSelector(ctx, ft.Frame.URL, string(ft.Frame.ID))
		visible := checkFrameVisible(ctx, selector)

		*frames = append(*frames, &BrowserFrameInfo{
			Selector: selector,
			Name:     string(ft.Frame.ID),
			URL:      ft.Frame.URL,
			Visible:  visible,
		})
	}

	// Process children
	for _, child := range ft.ChildFrames {
		processFrame(ctx, child, frames)
	}
}

func checkFrameVisible(ctx context.Context, selector string) bool {
	if selector == "" {
		return false
	}
	var visible bool
	chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`
	(function() {
		const el = document.querySelector(%q);
		if (!el) return false;
		const rect = el.getBoundingClientRect();
		const style = getComputedStyle(el);
		return rect.height > 0 && rect.width > 0 && style.display !== 'none';
	})()
	`, selector), &visible))
	return visible
}

func findIFrameSelector(ctx context.Context, frameURL, frameID string) string {
	// Try by frame ID first
	if frameID != "" {
		var found bool
		chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`!!document.querySelector('iframe[name=%q]') || !!document.querySelector('iframe#%s')`, frameID, frameID), &found))
		if found {
			return fmt.Sprintf("iframe[name='%s']", frameID)
		}
	}

	// Try by src attribute
	if frameURL != "" {
		var selector string
		chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`
		(function() {
			for (let iframe of document.querySelectorAll('iframe')) {
				if (iframe.src === %q || iframe.src.startsWith(%q)) {
					if (iframe.id) return '#' + iframe.id;
					if (iframe.name) return 'iframe[name="' + iframe.name + '"]';
					return 'iframe[src*="' + iframe.src.substring(0, 50) + '"]';
				}
			}
			return '';
		})()
		`, frameURL, frameURL), &selector))
		if selector != "" {
			return selector
		}
	}

	return ""
}

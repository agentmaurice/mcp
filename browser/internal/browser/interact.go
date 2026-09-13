package browser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// Hover moves mouse over an element identified by selector
func Hover(ctx context.Context, selector string) error {
	var x, y float64

	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			node, err := dom.GetDocument().Do(ctx)
			if err != nil {
				return err
			}

			nodeID, err := dom.QuerySelector(node.NodeID, selector).Do(ctx)
			if err != nil {
				return err
			}

			if nodeID == 0 {
				return fmt.Errorf("element not found: %s", selector)
			}

			box, err := dom.GetBoxModel().WithNodeID(nodeID).Do(ctx)
			if err != nil {
				return err
			}

			if box == nil {
				return fmt.Errorf("unable to get box model for %s", selector)
			}

			// Get center of element
			x = (box.Content[0] + box.Content[2]) / 2
			y = (box.Content[1] + box.Content[3]) / 2
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return input.DispatchMouseEvent(input.MouseMoved, x, y).Do(ctx)
		}),
	)

	return err
}

// PressKey sends keyboard input with optional modifiers
func PressKey(ctx context.Context, key string, repeat int) error {
	if repeat < 1 || repeat > 100 {
		return fmt.Errorf("repeat must be between 1 and 100")
	}

	modifiers := parseModifiers(key)
	mainKey := extractMainKey(key)

	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		for i := 0; i < repeat; i++ {
			// keyDown
			if err := input.DispatchKeyEvent(input.KeyDown).WithKey(mainKey).WithModifiers(input.Modifier(modifiers)).Do(ctx); err != nil {
				return err
			}

			// char event if printable
			if isPrintable(mainKey) {
				if err := input.DispatchKeyEvent(input.KeyChar).WithKey(mainKey).WithText(mainKey).WithModifiers(input.Modifier(modifiers)).Do(ctx); err != nil {
					return err
				}
			}

			// keyUp
			if err := input.DispatchKeyEvent(input.KeyUp).WithKey(mainKey).WithModifiers(input.Modifier(modifiers)).Do(ctx); err != nil {
				return err
			}
		}
		return nil
	}))
}

// FocusElement focuses an element by selector
func FocusElement(ctx context.Context, selector string) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		node, err := dom.GetDocument().Do(ctx)
		if err != nil {
			return err
		}
		nodeID, err := dom.QuerySelector(node.NodeID, selector).Do(ctx)
		if err != nil {
			return err
		}
		if nodeID == 0 {
			return fmt.Errorf("element not found: %s", selector)
		}
		return dom.Focus().WithNodeID(nodeID).Do(ctx)
	}))
}

func parseModifiers(key string) int64 {
	var mods int64
	if strings.Contains(key, "Alt") {
		mods |= 1 // Alt
	}
	if strings.Contains(key, "Control") || strings.Contains(key, "Ctrl") {
		mods |= 2 // Ctrl
	}
	if strings.Contains(key, "Meta") || strings.Contains(key, "Command") {
		mods |= 4 // Meta/Command
	}
	if strings.Contains(key, "Shift") {
		mods |= 8 // Shift
	}
	return mods
}

func extractMainKey(key string) string {
	parts := strings.Split(key, "+")
	return parts[len(parts)-1]
}

func isPrintable(key string) bool {
	if len(key) == 1 {
		c := key[0]
		return (c >= 32 && c <= 126) || (c >= 160 && c <= 255)
	}
	return false
}

// DragDrop performs drag-and-drop with CDP mouse events + HTML5 fallback
func DragDrop(ctx context.Context, sourceSelector, targetSelector string) error {
	// Get coordinates of source and target
	var sourceX, sourceY, targetX, targetY float64

	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			node, err := dom.GetDocument().Do(ctx)
			if err != nil {
				return err
			}
			nodeID, err := dom.QuerySelector(node.NodeID, sourceSelector).Do(ctx)
			if err != nil {
				return err
			}
			if nodeID == 0 {
				return fmt.Errorf("source element not found: %s", sourceSelector)
			}
			box, err := dom.GetBoxModel().WithNodeID(nodeID).Do(ctx)
			if err != nil {
				return err
			}
			sourceX = (box.Content[0] + box.Content[2]) / 2
			sourceY = (box.Content[1] + box.Content[3]) / 2
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			node, err := dom.GetDocument().Do(ctx)
			if err != nil {
				return err
			}
			nodeID, err := dom.QuerySelector(node.NodeID, targetSelector).Do(ctx)
			if err != nil {
				return err
			}
			if nodeID == 0 {
				return fmt.Errorf("target element not found: %s", targetSelector)
			}
			box, err := dom.GetBoxModel().WithNodeID(nodeID).Do(ctx)
			if err != nil {
				return err
			}
			targetX = (box.Content[0] + box.Content[2]) / 2
			targetY = (box.Content[1] + box.Content[3]) / 2
			return nil
		}),
	); err != nil {
		return err
	}

	// Strategy 1: CDP mouse events with intermediate steps
	if err := cdpDragDrop(ctx, sourceX, sourceY, targetX, targetY); err == nil {
		return nil
	}

	// Strategy 2: HTML5 Drag API fallback
	js := fmt.Sprintf(`
	(function() {
		const source = document.querySelector(%q);
		const target = document.querySelector(%q);
		if (!source || !target) return false;

		const dataTransfer = new DataTransfer();
		source.dispatchEvent(new DragEvent('dragstart', {bubbles: true, cancelable: true, dataTransfer: dataTransfer}));
		target.dispatchEvent(new DragEvent('dragover', {bubbles: true, cancelable: true, dataTransfer: dataTransfer}));
		target.dispatchEvent(new DragEvent('drop', {bubbles: true, cancelable: true, dataTransfer: dataTransfer}));
		source.dispatchEvent(new DragEvent('dragend', {bubbles: true, cancelable: true, dataTransfer: dataTransfer}));

		return true;
	})();
	`, sourceSelector, targetSelector)

	var result bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &result)); err != nil {
		return fmt.Errorf("drag_failed: %w", err)
	}

	if !result {
		return fmt.Errorf("drag_failed: HTML5 fallback returned false")
	}

	return nil
}

func cdpDragDrop(ctx context.Context, sx, sy, tx, ty float64) error {
	steps := 5
	return chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			return input.DispatchMouseEvent(input.MousePressed, sx, sy).
				WithButton(input.Left).
				WithClickCount(1).
				Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			for i := 1; i <= steps; i++ {
				x := sx + (tx-sx)*float64(i)/float64(steps)
				y := sy + (ty-sy)*float64(i)/float64(steps)
				if err := input.DispatchMouseEvent(input.MouseMoved, x, y).Do(ctx); err != nil {
					return err
				}
				time.Sleep(10 * time.Millisecond)
			}
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return input.DispatchMouseEvent(input.MouseReleased, tx, ty).
				WithButton(input.Left).
				WithClickCount(1).
				Do(ctx)
		}),
	)
}

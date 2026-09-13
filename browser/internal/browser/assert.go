package browser

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/chromedp"
)

// AssertionType defines the type of DOM assertion
type AssertionType string

const (
	AssertVisible         AssertionType = "visible"
	AssertHidden          AssertionType = "hidden"
	AssertTextContains    AssertionType = "text_contains"
	AssertTextEquals      AssertionType = "text_equals"
	AssertAttributeEquals AssertionType = "attribute_equals"
	AssertURLContains     AssertionType = "url_contains"
	AssertTitleContains   AssertionType = "title_contains"
	AssertElementCount    AssertionType = "element_count"
)

// DOMAssertResult represents the result of a DOM assertion
type DOMAssertResult struct {
	Pass      bool
	Assertion string
	Actual    string
	Expected  string
	Message   string
}

// AssertDOM performs a synchronous DOM assertion (no polling)
func AssertDOM(ctx context.Context, assertType AssertionType, selector, attribute, expected string) *DOMAssertResult {
	switch assertType {
	case AssertVisible:
		return assertVisible(ctx, selector)
	case AssertHidden:
		return assertHidden(ctx, selector)
	case AssertTextContains:
		return assertTextContains(ctx, selector, expected)
	case AssertTextEquals:
		return assertTextEquals(ctx, selector, expected)
	case AssertAttributeEquals:
		return assertAttributeEquals(ctx, selector, attribute, expected)
	case AssertURLContains:
		return assertURLContains(ctx, expected)
	case AssertTitleContains:
		return assertTitleContains(ctx, expected)
	case AssertElementCount:
		return assertElementCount(ctx, selector, expected)
	default:
		return &DOMAssertResult{
			Pass:    false,
			Message: fmt.Sprintf("unknown assertion type: %s", assertType),
		}
	}
}

func assertVisible(ctx context.Context, selector string) *DOMAssertResult {
	visible := false
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		node, err := dom.GetDocument().Do(ctx)
		if err != nil {
			return err
		}
		nodeID, err := dom.QuerySelector(node.NodeID, selector).Do(ctx)
		if err != nil {
			return err
		}
		if nodeID == 0 {
			return nil // not found = not visible
		}
		box, err := dom.GetBoxModel().WithNodeID(nodeID).Do(ctx)
		if err != nil {
			return nil // can't get box = not visible
		}
		visible = box != nil && box.Content[2] > box.Content[0] && box.Content[3] > box.Content[1]
		return nil
	}))
	if err != nil {
		visible = false
	}

	msg := "is not visible"
	if visible {
		msg = "is visible"
	}
	return &DOMAssertResult{
		Pass:      visible,
		Assertion: string(AssertVisible),
		Expected:  "element is visible",
		Message:   fmt.Sprintf("element %s", msg),
	}
}

func assertHidden(ctx context.Context, selector string) *DOMAssertResult {
	vis := assertVisible(ctx, selector)
	msg := "is visible"
	if !vis.Pass {
		msg = "is hidden"
	}
	return &DOMAssertResult{
		Pass:      !vis.Pass,
		Assertion: string(AssertHidden),
		Expected:  "element is hidden",
		Message:   fmt.Sprintf("element %s", msg),
	}
}

func assertTextContains(ctx context.Context, selector, expected string) *DOMAssertResult {
	var text string
	chromedp.Run(ctx, chromedp.Text(selector, &text, chromedp.ByQuery))

	pass := strings.Contains(text, expected)
	msg := "does not"
	if pass {
		msg = "does"
	}
	return &DOMAssertResult{
		Pass:      pass,
		Assertion: string(AssertTextContains),
		Actual:    text,
		Expected:  expected,
		Message:   fmt.Sprintf("text %s contain %q", msg, expected),
	}
}

func assertTextEquals(ctx context.Context, selector, expected string) *DOMAssertResult {
	var text string
	chromedp.Run(ctx, chromedp.Text(selector, &text, chromedp.ByQuery))

	pass := text == expected
	msg := "does not"
	if pass {
		msg = "does"
	}
	return &DOMAssertResult{
		Pass:      pass,
		Assertion: string(AssertTextEquals),
		Actual:    text,
		Expected:  expected,
		Message:   fmt.Sprintf("text %s equal %q", msg, expected),
	}
}

func assertAttributeEquals(ctx context.Context, selector, attribute, expected string) *DOMAssertResult {
	var actual string
	chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		node, err := dom.GetDocument().Do(ctx)
		if err != nil {
			return err
		}
		nodeID, err := dom.QuerySelector(node.NodeID, selector).Do(ctx)
		if err != nil {
			return err
		}
		if nodeID == 0 {
			return fmt.Errorf("element not found")
		}
		attrs, err := dom.GetAttributes(nodeID).Do(ctx)
		if err != nil {
			return err
		}
		for i := 0; i < len(attrs); i += 2 {
			if attrs[i] == attribute {
				actual = attrs[i+1]
				break
			}
		}
		return nil
	}))

	pass := actual == expected
	msg := "does not"
	if pass {
		msg = "does"
	}
	return &DOMAssertResult{
		Pass:      pass,
		Assertion: string(AssertAttributeEquals),
		Actual:    actual,
		Expected:  expected,
		Message:   fmt.Sprintf("attribute %q %s equal %q", attribute, msg, expected),
	}
}

func assertURLContains(ctx context.Context, expected string) *DOMAssertResult {
	var current string
	chromedp.Run(ctx, chromedp.Evaluate(`window.location.href`, &current))

	pass := strings.Contains(current, expected)
	msg := "does not"
	if pass {
		msg = "does"
	}
	return &DOMAssertResult{
		Pass:      pass,
		Assertion: string(AssertURLContains),
		Actual:    current,
		Expected:  expected,
		Message:   fmt.Sprintf("URL %s contain %q", msg, expected),
	}
}

func assertTitleContains(ctx context.Context, expected string) *DOMAssertResult {
	var title string
	chromedp.Run(ctx, chromedp.Evaluate(`document.title`, &title))

	pass := strings.Contains(title, expected)
	msg := "does not"
	if pass {
		msg = "does"
	}
	return &DOMAssertResult{
		Pass:      pass,
		Assertion: string(AssertTitleContains),
		Actual:    title,
		Expected:  expected,
		Message:   fmt.Sprintf("title %s contain %q", msg, expected),
	}
}

func assertElementCount(ctx context.Context, selector, expected string) *DOMAssertResult {
	expectedCount, _ := strconv.Atoi(expected)
	var actualCount int

	chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		node, err := dom.GetDocument().Do(ctx)
		if err != nil {
			return err
		}
		nodeIDs, err := dom.QuerySelectorAll(node.NodeID, selector).Do(ctx)
		if err != nil {
			return err
		}
		actualCount = len(nodeIDs)
		return nil
	}))

	pass := actualCount == expectedCount
	msg := "does not"
	if pass {
		msg = "does"
	}
	return &DOMAssertResult{
		Pass:      pass,
		Assertion: string(AssertElementCount),
		Actual:    strconv.Itoa(actualCount),
		Expected:  expected,
		Message:   fmt.Sprintf("element count %s equal %d", msg, expectedCount),
	}
}

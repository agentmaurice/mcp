package browser

import (
	"testing"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// TestCDPAPIsCompile validates that the CDP APIs we need are available
// This is a compilation check only — no browser execution needed
func TestCDPAPIsCompile(t *testing.T) {
	// Fetch API types exist
	_ = fetch.Enable()

	// Network API types exist
	_ = network.Enable()

	// Page API types exist
	_ = page.GetFrameTree()

	// ListenTarget exists
	listenTarget := chromedp.ListenTarget
	_ = listenTarget

	t.Log("All CDP APIs are available and compile successfully")
}

// TestEventTypesExist validates that event types we'll use are defined
func TestEventTypesExist(t *testing.T) {
	eventRequestWillBeSent := &network.EventRequestWillBeSent{}
	_ = eventRequestWillBeSent

	eventResponseReceived := &network.EventResponseReceived{}
	_ = eventResponseReceived

	eventLoadingFinished := &network.EventLoadingFinished{}
	_ = eventLoadingFinished

	eventRequestPaused := &fetch.EventRequestPaused{}
	_ = eventRequestPaused

	t.Log("All event types are defined and instantiable")
}

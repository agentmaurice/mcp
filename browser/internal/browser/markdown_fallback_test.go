package browser

import (
	"strings"
	"testing"
)

func TestMarkdownExtractionScriptHasNullSafeFallback(t *testing.T) {
	if !strings.Contains(markdownExtractionScript, "document.body && (document.body.innerText || document.body.textContent)") {
		t.Fatalf("expected markdown fallback to guard document.body access")
	}

	if !strings.Contains(markdownExtractionScript, "document.documentElement && (document.documentElement.innerText || document.documentElement.textContent)") {
		t.Fatalf("expected markdown fallback to guard document.documentElement access")
	}
}


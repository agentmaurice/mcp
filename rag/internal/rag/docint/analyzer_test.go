package docint

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestSplitContentSplitsLongWhitespaceText(t *testing.T) {
	a := NewAnalyzer(zap.NewNop())
	text := strings.TrimSpace(strings.Repeat("mot ", 1200))

	chunks := a.splitContent(text, 1000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	for i, chunk := range chunks {
		if len(chunk) > 1000 {
			t.Fatalf("chunk %d too large: %d", i, len(chunk))
		}
	}
}

func TestSplitContentSplitsSingleOversizedToken(t *testing.T) {
	a := NewAnalyzer(zap.NewNop())
	text := strings.Repeat("A", 2500)

	chunks := a.splitContent(text, 1000)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	for i, chunk := range chunks {
		if len(chunk) > 1000 {
			t.Fatalf("chunk %d too large: %d", i, len(chunk))
		}
	}
}

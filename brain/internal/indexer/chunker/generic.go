package chunker

import (
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
)

// chunkGeneric splits text into chunks of approximately maxTokens.
// Uses paragraph boundaries (double newline) where possible.
func chunkGeneric(text, filePath string, maxTokens int) []shared.ChunkInput {
	if maxTokens <= 0 {
		maxTokens = 500
	}

	lines := strings.Split(text, "\n")
	var chunks []shared.ChunkInput

	var current strings.Builder
	startLine := 1
	currentTokens := 0

	for i, line := range lines {
		lineNum := i + 1
		lineTokens := estimateTokens(line)

		// Check if adding this line would exceed the limit
		if currentTokens+lineTokens > maxTokens && current.Len() > 0 {
			content := strings.TrimSpace(current.String())
			if content != "" {
				chunks = append(chunks, shared.ChunkInput{
					Content:   content,
					Type:      "paragraph",
					StartLine: startLine,
					EndLine:   lineNum - 1,
					Metadata:  map[string]interface{}{},
				})
			}
			current.Reset()
			currentTokens = 0
			startLine = lineNum
		}

		current.WriteString(line)
		current.WriteString("\n")
		currentTokens += lineTokens
	}

	// Save remaining content
	if current.Len() > 0 {
		content := strings.TrimSpace(current.String())
		if content != "" {
			chunks = append(chunks, shared.ChunkInput{
				Content:   content,
				Type:      "paragraph",
				StartLine: startLine,
				EndLine:   len(lines),
				Metadata:  map[string]interface{}{},
			})
		}
	}

	return chunks
}

// estimateTokens gives a rough token count (words ≈ tokens * 0.75 for code).
func estimateTokens(text string) int {
	words := len(strings.Fields(text))
	// Rough heuristic: 1 word ≈ 1.3 tokens for code
	return int(float64(words) * 1.3)
}

package chunker

import (
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
)

// chunkMarkdown splits markdown content by headers.
func chunkMarkdown(text, filePath string) []shared.ChunkInput {
	lines := strings.Split(text, "\n")
	var chunks []shared.ChunkInput

	var currentChunk strings.Builder
	var currentTitle string
	var currentLevel int
	startLine := 1

	for i, line := range lines {
		lineNum := i + 1
		trimmed := strings.TrimSpace(line)

		// Detect headers
		if strings.HasPrefix(trimmed, "#") {
			// If we have accumulated content, save it
			if currentChunk.Len() > 0 {
				content := strings.TrimSpace(currentChunk.String())
				if content != "" {
					chunks = append(chunks, shared.ChunkInput{
						Content:    content,
						Type:       "section",
						SymbolName: currentTitle,
						StartLine:  startLine,
						EndLine:    lineNum - 1,
						Metadata: map[string]interface{}{
							"heading_level": currentLevel,
						},
					})
				}
				currentChunk.Reset()
			}

			// Parse header level
			level := 0
			for _, ch := range trimmed {
				if ch == '#' {
					level++
				} else {
					break
				}
			}
			currentTitle = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			currentLevel = level
			startLine = lineNum
		}

		currentChunk.WriteString(line)
		currentChunk.WriteString("\n")
	}

	// Save last chunk
	if currentChunk.Len() > 0 {
		content := strings.TrimSpace(currentChunk.String())
		if content != "" {
			chunks = append(chunks, shared.ChunkInput{
				Content:    content,
				Type:       "section",
				SymbolName: currentTitle,
				StartLine:  startLine,
				EndLine:    len(lines),
				Metadata: map[string]interface{}{
					"heading_level": currentLevel,
				},
			})
		}
	}

	// If no headers found, return whole file as one chunk
	if len(chunks) == 0 && strings.TrimSpace(text) != "" {
		chunks = append(chunks, shared.ChunkInput{
			Content:   text,
			Type:      "paragraph",
			StartLine: 1,
			EndLine:   len(lines),
			Metadata:  map[string]interface{}{},
		})
	}

	return chunks
}

package chunker

import (
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
)

// Chunk dispatches to the appropriate chunker based on language/type.
func Chunk(content []byte, lang, filePath string, maxTokens int) []shared.ChunkInput {
	if maxTokens <= 0 {
		maxTokens = 1000
	}

	text := string(content)
	if strings.TrimSpace(text) == "" {
		return nil
	}

	switch lang {
	case "go":
		chunks := chunkGoAST(content, filePath)
		if len(chunks) > 0 {
			return chunks
		}
		// Fallback to generic if AST parsing fails
		return chunkGeneric(text, filePath, maxTokens)
	default:
		// Check if it's markdown
		ext := strings.ToLower(filePath)
		if strings.HasSuffix(ext, ".md") || strings.HasSuffix(ext, ".mdx") || strings.HasSuffix(ext, ".rst") {
			return chunkMarkdown(text, filePath)
		}
		return chunkGeneric(text, filePath, maxTokens)
	}
}

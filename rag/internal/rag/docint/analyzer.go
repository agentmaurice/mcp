package docint

import (
	"context"
	"regexp"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"go.uber.org/zap"
)

// Analyzer implements document analysis and intelligent chunking
type Analyzer struct {
	logger *zap.Logger
}

// NewAnalyzer creates a new document analyzer
func NewAnalyzer(logger *zap.Logger) *Analyzer {
	return &Analyzer{
		logger: logger.Named("docint"),
	}
}

// Analyze analyzes a document and extracts sections
func (a *Analyzer) Analyze(ctx context.Context, text, title string) (*shared.AnalyzedDoc, error) {
	a.logger.Debug("analyzing document", zap.String("title", title), zap.Int("length", len(text)))

	sections := a.extractSections(text)

	return &shared.AnalyzedDoc{
		Title:    title,
		Sections: sections,
	}, nil
}

// extractSections extracts logical sections from text
func (a *Analyzer) extractSections(text string) []shared.Section {
	// Simple section extraction based on paragraphs
	// In production, this would use more sophisticated NLP
	paragraphs := strings.Split(text, "\n\n")

	var sections []shared.Section
	var currentSection shared.Section
	currentSection.Text = ""

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		// Check if this is a heading (simple heuristic)
		if isHeading(para) {
			// Save previous section if it has content
			if currentSection.Text != "" {
				sections = append(sections, currentSection)
			}
			// Start new section
			currentSection = shared.Section{
				Type:  "content",
				Text:  "",
				Level: 1,
			}
		} else {
			// Add to current section
			if currentSection.Text != "" {
				currentSection.Text += "\n\n"
			}
			currentSection.Text += para
		}
	}

	// Add final section
	if currentSection.Text != "" {
		sections = append(sections, currentSection)
	}

	// If no sections were found, create one with all content
	if len(sections) == 0 {
		sections = append(sections, shared.Section{
			Type:  "content",
			Text:  text,
			Level: 1,
		})
	}

	return sections
}

// isHeading determines if a line is likely a heading
func isHeading(line string) bool {
	// Simple heuristics for heading detection
	if len(line) > 100 {
		return false
	}
	if strings.HasPrefix(line, "#") {
		return true
	}
	// Check if all caps or title case
	if line == strings.ToUpper(line) && len(line) > 3 {
		return true
	}
	return false
}

// ChunkSections creates chunks from analyzed sections
func (a *Analyzer) ChunkSections(sections []shared.Section, maxChunkSize int) []shared.Chunk {
	var chunks []shared.Chunk

	for _, section := range sections {
		// If section is small enough, create one chunk
		if len(section.Text) <= maxChunkSize {
			chunks = append(chunks, shared.Chunk{
				Text: section.Text,
				Metadata: map[string]interface{}{
					"section_type": section.Type,
					"level":        section.Level,
				},
			})
			continue
		}

		// Split large sections into smaller chunks
		sectionChunks := a.splitContent(section.Text, maxChunkSize)
		for i, chunkText := range sectionChunks {
			chunks = append(chunks, shared.Chunk{
				Text: chunkText,
				Metadata: map[string]interface{}{
					"section_type": section.Type,
					"level":        section.Level,
					"chunk_index":  i,
				},
			})
		}
	}

	return chunks
}

// NormalizeText applies a basic normalization to make hashes/embeddings comparable.
func NormalizeText(text string) string {
	// strip HTML tags
	re := regexp.MustCompile(`<[^>]+>`)
	text = re.ReplaceAllString(text, " ")
	text = strings.ToLower(text)
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	// collapse whitespace
	ws := regexp.MustCompile(`\s+`)
	text = ws.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// splitContent splits content into chunks of maximum size
func (a *Analyzer) splitContent(content string, maxSize int) []string {
	if len(content) <= maxSize {
		return []string{content}
	}

	var chunks []string
	sentences := strings.Split(content, ". ")

	var currentChunk strings.Builder
	overlapSize := maxSize / 5 // ~20% overlap to preserve context
	if overlapSize < 50 {
		overlapSize = 50
	}

	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}

		// Add period back if not already present
		if !strings.HasSuffix(sentence, ".") && !strings.HasSuffix(sentence, "!") && !strings.HasSuffix(sentence, "?") {
			sentence += "."
		}

		// If a single sentence is longer than the chunk budget, split it with a
		// whitespace/newline-aware fallback so PDF-derived text without proper
		// sentence boundaries still gets chunked.
		if len(sentence) > maxSize {
			if currentChunk.Len() > 0 {
				chunkText := strings.TrimSpace(currentChunk.String())
				if chunkText != "" {
					chunks = append(chunks, chunkText)
				}
				currentChunk.Reset()
			}

			longChunks := splitOversizedText(sentence, maxSize, overlapSize)
			chunks = append(chunks, longChunks...)
			continue
		}

		// If adding this sentence exceeds max size, save current chunk
		if currentChunk.Len() > 0 && currentChunk.Len()+len(sentence)+1 > maxSize {
			chunkText := strings.TrimSpace(currentChunk.String())
			if chunkText != "" {
				chunks = append(chunks, chunkText)
			}
			currentChunk.Reset()

			// Seed next chunk with trailing overlap from previous chunk
			overlap := trailingWindow(chunkText, overlapSize)
			if overlap != "" {
				currentChunk.WriteString(overlap)
			}
		}

		// Add sentence to current chunk
		if currentChunk.Len() > 0 {
			currentChunk.WriteString(" ")
		}
		currentChunk.WriteString(sentence)
	}

	// Add final chunk
	if currentChunk.Len() > 0 {
		chunkText := strings.TrimSpace(currentChunk.String())
		if chunkText != "" {
			chunks = append(chunks, chunkText)
		}
	}

	return chunks
}

func splitOversizedText(text string, maxSize int, overlapSize int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= maxSize {
		return []string{text}
	}

	var chunks []string
	start := 0
	for start < len(text) {
		end := start + maxSize
		if end >= len(text) {
			chunk := strings.TrimSpace(text[start:])
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			break
		}

		splitAt := findSplitBoundary(text, start, end)
		if splitAt <= start {
			splitAt = end
		}

		chunk := strings.TrimSpace(text[start:splitAt])
		if chunk != "" {
			chunks = append(chunks, chunk)
		}

		nextStart := splitAt - overlapSize
		if nextStart <= start {
			nextStart = splitAt
		}
		start = nextStart
	}

	return chunks
}

func findSplitBoundary(text string, start int, end int) int {
	if end > len(text) {
		end = len(text)
	}
	window := text[start:end]
	if window == "" {
		return start
	}

	searchStart := len(window) / 2
	if idx := strings.LastIndexAny(window[searchStart:], "\n\r\t "); idx >= 0 {
		return start + searchStart + idx
	}
	if idx := strings.LastIndexAny(window, "\n\r\t "); idx >= 0 {
		return start + idx
	}
	return end
}

// trailingWindow returns the last N characters of text (trimmed) to use as overlap.
func trailingWindow(text string, size int) string {
	if size <= 0 || text == "" {
		return ""
	}
	if len(text) <= size {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(text[len(text)-size:])
}

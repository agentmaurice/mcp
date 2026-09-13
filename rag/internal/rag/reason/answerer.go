package reason

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/cache"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"go.uber.org/zap"
)

// Answerer implements LLM-based answer generation
type Answerer struct {
	llmClient shared.LLMClient
	cache     cache.QueryCache
	logger    *zap.Logger
}

// NewAnswerer creates a new answerer
func NewAnswerer(llmClient shared.LLMClient, queryCache cache.QueryCache, logger *zap.Logger) *Answerer {
	return &Answerer{
		llmClient: llmClient,
		cache:     queryCache,
		logger:    logger.Named("reason"),
	}
}

// GenerateAnswer generates an answer using retrieved chunks
func (a *Answerer) GenerateAnswer(ctx context.Context, query string, chunks []shared.Chunk, maxTokens int) (*shared.Answer, error) {
	a.logger.Debug("generating answer",
		zap.String("query", query),
		zap.Int("chunks", len(chunks)),
		zap.Int("maxTokens", maxTokens))

	if len(chunks) == 0 {
		return &shared.Answer{
			Text:      "Je n'ai pas trouvé d'information pertinente pour répondre à cette question.",
			Citations: []shared.Citation{},
		}, nil
	}

	// Keep context within a rough budget derived from maxTokens
	chunks = capChunksToBudget(chunks, maxTokens)

	// Extract chunk IDs for cache key
	chunkIDs := make([]string, len(chunks))
	for i, ch := range chunks {
		chunkIDs[i] = ch.ID
	}

	// Try answer cache
	cachedAnswer, answerCached := a.cache.GetAnswer(ctx, query, chunkIDs, maxTokens)
	if answerCached {
		a.logger.Debug("using cached answer")
		return cachedAnswer, nil
	}

	// Build context from chunks
	context := a.buildContext(chunks)

	// Build prompt
	prompt := a.buildPrompt(query, context)

	// Generate answer
	answerText, err := a.llmClient.GenerateAnswer(ctx, prompt, maxTokens)
	if err != nil {
		return nil, err
	}

	// Build citations
	citations := a.buildCitations(chunks)

	answer := &shared.Answer{
		Text:      answerText,
		Citations: citations,
	}

	// Cache the answer
	if err := a.cache.SetAnswer(ctx, query, chunkIDs, maxTokens, answer); err != nil {
		a.logger.Warn("failed to cache answer", zap.Error(err))
	}

	return answer, nil
}

// buildContext builds context string from chunks
func (a *Answerer) buildContext(chunks []shared.Chunk) string {
	var parts []string

	for i, chunk := range chunks {
		part := fmt.Sprintf("[%d] %s", i+1, chunk.Text)
		parts = append(parts, part)
	}

	return strings.Join(parts, "\n\n")
}

// buildPrompt builds the prompt for the LLM
func (a *Answerer) buildPrompt(query, context string) string {
	return fmt.Sprintf(`Tu es un assistant qui répond aux questions en te basant sur les informations fournies.

Contexte:
%s

Question: %s

Instructions:
- Réponds de manière précise et concise
- Utilise uniquement les informations du contexte
- Si tu ne trouves pas l'information, dis-le clairement
- Cite les sources en utilisant les numéros [1], [2], etc.

Réponse:`, context, query)
}

// buildCitations builds citations from chunks
func (a *Answerer) buildCitations(chunks []shared.Chunk) []shared.Citation {
	citations := make([]shared.Citation, len(chunks))

	for i, chunk := range chunks {
		// Create snippet from chunk text (truncate if too long)
		snippet := chunk.Text
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}

		citation := shared.Citation{
			ChunkID:    chunk.ID,
			DocumentID: chunk.DocumentID,
			Snippet:    snippet,
			Metadata:   chunk.Metadata,
		}

		citations[i] = citation
	}

	return citations
}

// capChunksToBudget trims chunks to fit roughly within the generation budget.
func capChunksToBudget(chunks []shared.Chunk, maxTokens int) []shared.Chunk {
	if maxTokens <= 0 {
		return chunks
	}
	// Approximate 1 token ~= 4 characters (rough heuristic)
	budgetChars := maxTokens * 4
	if budgetChars < 500 {
		budgetChars = 500
	}

	var kept []shared.Chunk
	used := 0
	for _, ch := range chunks {
		length := len(ch.Text)
		if used+length > budgetChars && len(kept) > 0 {
			break
		}
		kept = append(kept, ch)
		used += length
	}
	return kept
}

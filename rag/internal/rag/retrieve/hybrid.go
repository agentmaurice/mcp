package retrieve

import (
	"context"
	"sort"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/cache"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// Retriever implements hybrid retrieval (vector + full-text)
type Retriever struct {
	vectorRetriever VectorRetriever
	keyword         shared.KeywordSearcher
	llmClient       shared.LLMClient
	cache           cache.QueryCache
	logger          *zap.Logger
}

// NewRetriever creates a new hybrid retriever
func NewRetriever(vectorRetriever VectorRetriever, keyword shared.KeywordSearcher, llmClient shared.LLMClient, queryCache cache.QueryCache, logger *zap.Logger) *Retriever {
	return &Retriever{
		vectorRetriever: vectorRetriever,
		keyword:         keyword,
		llmClient:       llmClient,
		cache:           queryCache,
		logger:          logger.Named("retrieve"),
	}
}

// Retrieve performs hybrid retrieval using vector search
func (r *Retriever) Retrieve(ctx context.Context, query string, analysis *shared.QueryAnalysis, topK int, tenantID xid.ID, deploymentID xid.ID, metadataFilters map[string]string) ([]shared.Chunk, error) {
	r.logger.Debug("retrieving chunks",
		zap.String("query", query),
		zap.Int("topK", topK),
		zap.Strings("keywords", analysis.Keywords),
		zap.String("tenantID", tenantID.String()),
		zap.String("deploymentID", deploymentID.String()),
		zap.Any("metadataFilters", metadataFilters))

	// Try embedding cache first
	embedding, embeddingCached := r.cache.GetEmbedding(ctx, query)
	if !embeddingCached {
		// Generate query embedding
		var err error
		embedding, err = r.llmClient.GenerateEmbedding(ctx, query)
		if err != nil {
			r.logger.Warn("failed to generate query embedding, attempting keyword fallback", zap.Error(err))
			if r.keyword != nil {
				keywordChunks, keywordErr := r.keyword.SearchByKeyword(ctx, tenantID, query, topK*2)
				if keywordErr == nil && len(keywordChunks) > 0 {
					merged := r.mergeAndRank(query, analysis, nil, keywordChunks, topK)
					r.logger.Warn("using keyword-only fallback retrieval",
						zap.Int("chunks", len(merged)),
						zap.Error(err))
					return merged, nil
				}
				if keywordErr != nil {
					r.logger.Warn("keyword fallback retrieval failed", zap.Error(keywordErr))
				}
			}
			return nil, err
		}
		// Cache the embedding
		if err := r.cache.SetEmbedding(ctx, query, embedding); err != nil {
			r.logger.Warn("failed to cache embedding", zap.Error(err))
		}
		r.logger.Debug("generated query embedding", zap.Int("dims", len(embedding)))
	} else {
		r.logger.Debug("using cached embedding", zap.Int("dims", len(embedding)))
	}

	// Try search cache
	vectorChunks, searchCached := r.cache.GetSearchResults(ctx, embedding, topK, tenantID)
	if !searchCached {
		// Perform vector search using embedding
		var err error
		vectorChunks, err = r.vectorRetriever.Retrieve(ctx, deploymentID.String(), tenantID.String(), embedding, topK, metadataFilters)
		if err != nil {
			r.logger.Error("failed to search vector store", zap.Error(err))
			return nil, err
		}
		// Cache search results
		if err := r.cache.SetSearchResults(ctx, embedding, topK, tenantID, vectorChunks); err != nil {
			r.logger.Warn("failed to cache search results", zap.Error(err))
		}
	} else {
		r.logger.Debug("using cached search results", zap.Int("chunks", len(vectorChunks)))
	}

	// Optional lexical search fallback
	var keywordChunks []shared.Chunk
	if r.keyword != nil {
		var err error
		keywordChunks, err = r.keyword.SearchByKeyword(ctx, tenantID, query, topK*2)
		if err != nil {
			r.logger.Warn("keyword search failed", zap.Error(err))
		}
	}

	merged := r.mergeAndRank(query, analysis, vectorChunks, keywordChunks, topK)
	r.logger.Debug("retrieved chunks", zap.Int("count", len(merged)))

	return merged, nil
}

// RetrieveWithReranking performs retrieval with reranking
func (r *Retriever) RetrieveWithReranking(ctx context.Context, query string, analysis *shared.QueryAnalysis, topK int, tenantID xid.ID, deploymentID xid.ID, metadataFilters map[string]string) ([]shared.Chunk, error) {
	// Retrieve more candidates than needed
	candidates, err := r.Retrieve(ctx, query, analysis, topK*2, tenantID, deploymentID, metadataFilters)
	if err != nil {
		return nil, err
	}

	// In production, this would use a reranker model
	// For now, just return the top K
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}

	return candidates, nil
}

// mergeAndRank combines vector and keyword results with a simple hybrid rerank.
func (r *Retriever) mergeAndRank(query string, analysis *shared.QueryAnalysis, vectorChunks, keywordChunks []shared.Chunk, topK int) []shared.Chunk {
	type scored struct {
		chunk shared.Chunk
		score float64
	}

	scoreMap := make(map[string]*scored)

	// Vector scores (already provided by vector DB, higher is better)
	for _, ch := range vectorChunks {
		s := &scored{chunk: ch, score: ch.Score}
		scoreMap[ch.ID] = s
	}

	// Keyword scores (lightweight lexical hit count)
	if len(keywordChunks) > 0 && len(analysis.Keywords) > 0 {
		for _, ch := range keywordChunks {
			matchScore := lexicalScore(analysis.Keywords, ch.Text)
			if existing, ok := scoreMap[ch.ID]; ok {
				existing.score += matchScore
			} else {
				// Base score for lexical-only hits
				scoreMap[ch.ID] = &scored{chunk: ch, score: 0.2 + matchScore}
			}
		}
	}

	// Flatten and sort
	out := make([]*scored, 0, len(scoreMap))
	for _, s := range scoreMap {
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })

	// Truncate
	if len(out) > topK {
		out = out[:topK]
	}

	final := make([]shared.Chunk, len(out))
	for i, s := range out {
		final[i] = s.chunk
		// Store combined score for downstream prioritization
		final[i].Score = s.score
	}
	return final
}

// lexicalScore counts how many keywords appear in the text and normalizes.
func lexicalScore(keywords []string, text string) float64 {
	if len(keywords) == 0 || text == "" {
		return 0
	}
	lower := strings.ToLower(text)
	matches := 0
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(kw)) {
			matches++
		}
	}
	return float64(matches) / float64(len(keywords))
}

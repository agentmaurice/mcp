package cache

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/rs/xid"
)

// QueryCache defines the interface for RAG query caching
type QueryCache interface {
	// Embedding cache operations
	GetEmbedding(ctx context.Context, queryText string) ([]float64, bool)
	SetEmbedding(ctx context.Context, queryText string, embedding []float64) error

	// Search results cache operations
	GetSearchResults(ctx context.Context, embedding []float64, topK int, tenantID xid.ID) ([]shared.Chunk, bool)
	SetSearchResults(ctx context.Context, embedding []float64, topK int, tenantID xid.ID, chunks []shared.Chunk) error

	// Answer cache operations
	GetAnswer(ctx context.Context, queryText string, chunkIDs []string, maxTokens int) (*shared.Answer, bool)
	SetAnswer(ctx context.Context, queryText string, chunkIDs []string, maxTokens int, answer *shared.Answer) error

	// Invalidation
	InvalidateTenant(ctx context.Context, tenantID xid.ID) error
	InvalidateAll(ctx context.Context) error

	// Stats
	Stats() CacheStats

	// IsEnabled returns true if the cache is enabled
	IsEnabled() bool
}

// CacheStats contains cache statistics
type CacheStats struct {
	EmbeddingHits   int64 `json:"embedding_hits"`
	EmbeddingMisses int64 `json:"embedding_misses"`
	SearchHits      int64 `json:"search_hits"`
	SearchMisses    int64 `json:"search_misses"`
	AnswerHits      int64 `json:"answer_hits"`
	AnswerMisses    int64 `json:"answer_misses"`
}

// NoopCache is a cache that does nothing (used when caching is disabled)
type NoopCache struct{}

func NewNoopCache() *NoopCache {
	return &NoopCache{}
}

func (n *NoopCache) GetEmbedding(ctx context.Context, queryText string) ([]float64, bool) {
	return nil, false
}

func (n *NoopCache) SetEmbedding(ctx context.Context, queryText string, embedding []float64) error {
	return nil
}

func (n *NoopCache) GetSearchResults(ctx context.Context, embedding []float64, topK int, tenantID xid.ID) ([]shared.Chunk, bool) {
	return nil, false
}

func (n *NoopCache) SetSearchResults(ctx context.Context, embedding []float64, topK int, tenantID xid.ID, chunks []shared.Chunk) error {
	return nil
}

func (n *NoopCache) GetAnswer(ctx context.Context, queryText string, chunkIDs []string, maxTokens int) (*shared.Answer, bool) {
	return nil, false
}

func (n *NoopCache) SetAnswer(ctx context.Context, queryText string, chunkIDs []string, maxTokens int, answer *shared.Answer) error {
	return nil
}

func (n *NoopCache) InvalidateTenant(ctx context.Context, tenantID xid.ID) error {
	return nil
}

func (n *NoopCache) InvalidateAll(ctx context.Context) error {
	return nil
}

func (n *NoopCache) Stats() CacheStats {
	return CacheStats{}
}

func (n *NoopCache) IsEnabled() bool {
	return false
}

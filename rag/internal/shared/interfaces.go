package shared

import (
	"context"

	"github.com/rs/xid"
)

// VectorStore interface for vector database abstraction
type VectorStore interface {
	Upsert(ctx context.Context, chunks []Chunk) error
	UpsertDocEmbedding(ctx context.Context, doc Chunk) error
	Search(ctx context.Context, query string, topK int, tenantID xid.ID) ([]Chunk, error)
	SearchByVector(ctx context.Context, embedding []float64, topK int, tenantID xid.ID, metadataFilters map[string]string) ([]Chunk, error)
	SearchDocEmbedding(ctx context.Context, embedding []float64, topK int, tenantID xid.ID) ([]Chunk, error)
	Delete(ctx context.Context, chunkIDs []string) error
	DeleteByTenant(ctx context.Context, tenantID xid.ID) error
}

// KeywordSearcher interface for lexical search fallback
type KeywordSearcher interface {
	SearchByKeyword(ctx context.Context, tenantID xid.ID, query string, limit int) ([]Chunk, error)
}

// LLMClient interface for LLM provider abstraction
type LLMClient interface {
	GenerateEmbedding(ctx context.Context, text string) ([]float64, error)
	GenerateAnswer(ctx context.Context, prompt string, maxTokens int) (string, error)
}

// Manager interface for lifecycle management
type Manager interface {
	Start(ctx context.Context) error
	Stop() error
	Name() string
	Health(ctx context.Context) error
}

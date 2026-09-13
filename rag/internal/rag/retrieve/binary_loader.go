package retrieve

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
)

// DBBinaryEmbeddingLoader loads binary embeddings from the database.
type DBBinaryEmbeddingLoader struct {
	chunkRepo *repository.ChunkRepository
}

func NewDBBinaryEmbeddingLoader(chunkRepo *repository.ChunkRepository) *DBBinaryEmbeddingLoader {
	return &DBBinaryEmbeddingLoader{chunkRepo: chunkRepo}
}

func (l *DBBinaryEmbeddingLoader) LoadTenant(ctx context.Context, tenantID string) ([]BinaryEmbedding, error) {
	records, err := l.chunkRepo.ListBinaryEmbeddingsByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]BinaryEmbedding, 0, len(records))
	for _, rec := range records {
		out = append(out, BinaryEmbedding{ID: rec.ID, Bits: rec.Bits})
	}
	return out, nil
}

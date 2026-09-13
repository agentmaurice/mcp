package retrieve

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
)

// Int8EmbeddingStore provides access to int8 embeddings.
type Int8EmbeddingStore interface {
	GetMany(ctx context.Context, ids []string) (map[string][]int8, error)
}

// DBInt8EmbeddingStore loads int8 embeddings from the database.
type DBInt8EmbeddingStore struct {
	chunkRepo *repository.ChunkRepository
}

func NewDBInt8EmbeddingStore(chunkRepo *repository.ChunkRepository) *DBInt8EmbeddingStore {
	return &DBInt8EmbeddingStore{chunkRepo: chunkRepo}
}

func (s *DBInt8EmbeddingStore) GetMany(ctx context.Context, ids []string) (map[string][]int8, error) {
	if len(ids) == 0 {
		return map[string][]int8{}, nil
	}
	records, err := s.chunkRepo.GetEmbeddingsInt8ByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]int8, len(records))
	for id, data := range records {
		out[id] = bytesToInt8(data)
	}
	return out, nil
}

func bytesToInt8(data []byte) []int8 {
	if len(data) == 0 {
		return []int8{}
	}
	out := make([]int8, len(data))
	for i, b := range data {
		out[i] = int8(b)
	}
	return out
}

package retrieve

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// BinaryInt8Retriever implements binary search + int8 rescoring.
type BinaryInt8Retriever struct {
	quantizer      EmbeddingQuantizer
	index          BinaryIndex
	int8Store      Int8EmbeddingStore
	chunkRepo      *repository.ChunkRepository
	candidateCount int
	logger         *zap.Logger
}

// NewBinaryInt8Retriever creates a new binary/int8 retriever.
func NewBinaryInt8Retriever(
	quantizer EmbeddingQuantizer,
	index BinaryIndex,
	int8Store Int8EmbeddingStore,
	chunkRepo *repository.ChunkRepository,
	candidateCount int,
	logger *zap.Logger,
) *BinaryInt8Retriever {
	if logger == nil {
		logger = zap.NewNop()
	}
	if candidateCount <= 0 {
		candidateCount = 1000
	}
	return &BinaryInt8Retriever{
		quantizer:      quantizer,
		index:          index,
		int8Store:      int8Store,
		chunkRepo:      chunkRepo,
		candidateCount: candidateCount,
		logger:         logger.Named("binary-int8-retriever"),
	}
}

func (r *BinaryInt8Retriever) Name() string {
	return "binary_int8"
}

func (r *BinaryInt8Retriever) Retrieve(ctx context.Context, _ string, tenantID string, queryEmbedding []float64, k int, metadataFilters map[string]string) ([]shared.Chunk, error) {
	if r.quantizer == nil || r.index == nil || r.int8Store == nil || r.chunkRepo == nil {
		return nil, fmt.Errorf("binary retriever not fully configured")
	}
	if k <= 0 {
		return []shared.Chunk{}, nil
	}

	start := time.Now()

	if err := r.index.EnsureTenant(ctx, tenantID); err != nil {
		return nil, err
	}

	binaryQuery, err := r.quantizer.ToBinary(queryEmbedding)
	if err != nil {
		return nil, err
	}
	queryInt8, err := r.quantizer.ToInt8(queryEmbedding)
	if err != nil {
		return nil, err
	}

	candidates, err := r.index.Search(binaryQuery, tenantID, r.candidateCount)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return []shared.Chunk{}, nil
	}

	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.ID)
	}

	int8Map, err := r.int8Store.GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}

	type scored struct {
		id    string
		score int64
	}
	scoredList := make([]scored, 0, len(candidates))
	for _, cand := range candidates {
		vec, ok := int8Map[cand.ID]
		if !ok || len(vec) == 0 {
			continue
		}
		score := dotInt8(queryInt8, vec)
		scoredList = append(scoredList, scored{id: cand.ID, score: score})
	}

	if len(scoredList) == 0 {
		return []shared.Chunk{}, nil
	}

	sort.Slice(scoredList, func(i, j int) bool {
		if scoredList[i].score == scoredList[j].score {
			return scoredList[i].id < scoredList[j].id
		}
		return scoredList[i].score > scoredList[j].score
	})

	if len(scoredList) > k {
		scoredList = scoredList[:k]
	}

	tenantXID, err := xid.FromString(tenantID)
	if err != nil {
		return nil, err
	}

	topIDs := make([]string, 0, len(scoredList))
	for _, s := range scoredList {
		topIDs = append(topIDs, s.id)
	}

	chunks, err := r.chunkRepo.GetByIDsForTenant(ctx, topIDs, tenantXID)
	if err != nil {
		return nil, err
	}
	chunkMap := make(map[string]*shared.Chunk, len(chunks))
	for _, ch := range chunks {
		c := ch
		chunkMap[c.ID] = &c
	}

	out := make([]shared.Chunk, 0, len(scoredList))
	for _, s := range scoredList {
		chunk, ok := chunkMap[s.id]
		if !ok {
			continue
		}
		chunk.Score = float64(s.score)
		out = append(out, *chunk)
	}

	r.logger.Debug("binary retrieval completed",
		zap.String("tenant_id", tenantID),
		zap.Int("candidates", len(candidates)),
		zap.Int("results", len(out)),
		zap.Duration("duration", time.Since(start)))

	return out, nil
}

func dotInt8(a, b []int8) int64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var sum int64
	for i := 0; i < n; i++ {
		sum += int64(a[i]) * int64(b[i])
	}
	return sum
}

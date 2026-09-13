package retrieve

import (
	"context"
	"fmt"
	"math/bits"
	"sort"
	"sync"

	"go.uber.org/zap"
)

// BinaryCandidate represents a candidate from binary search.
type BinaryCandidate struct {
	ID       string
	Distance int
}

// BinaryEmbedding represents a binary embedding with its ID.
type BinaryEmbedding struct {
	ID   string
	Bits []byte
}

// BinaryEmbeddingLoader loads binary embeddings for a tenant.
type BinaryEmbeddingLoader interface {
	LoadTenant(ctx context.Context, tenantID string) ([]BinaryEmbedding, error)
}

// BinaryIndex provides binary embedding search.
type BinaryIndex interface {
	Add(id string, binaryEmbedding []byte, tenantID string)
	Search(binaryQuery []byte, tenantID string, limit int) ([]BinaryCandidate, error)
	EnsureTenant(ctx context.Context, tenantID string) error
}

type binaryEntry struct {
	id   string
	bits []byte
}

type tenantIndex struct {
	loaded  bool
	entries []binaryEntry
}

// InMemoryBinaryIndex is a simple in-memory binary index (linear scan).
type InMemoryBinaryIndex struct {
	mu       sync.RWMutex
	byTenant map[string]*tenantIndex
	loader   BinaryEmbeddingLoader
	logger   *zap.Logger
}

// NewInMemoryBinaryIndex creates a new binary index.
func NewInMemoryBinaryIndex(loader BinaryEmbeddingLoader, logger *zap.Logger) *InMemoryBinaryIndex {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &InMemoryBinaryIndex{
		byTenant: make(map[string]*tenantIndex),
		loader:   loader,
		logger:   logger.Named("binary-index"),
	}
}

// EnsureTenant loads embeddings for a tenant if not already loaded.
func (i *InMemoryBinaryIndex) EnsureTenant(ctx context.Context, tenantID string) error {
	i.mu.RLock()
	ti, ok := i.byTenant[tenantID]
	if ok && ti.loaded {
		i.mu.RUnlock()
		return nil
	}
	i.mu.RUnlock()

	if i.loader == nil {
		return fmt.Errorf("binary index loader is nil")
	}

	embeddings, err := i.loader.LoadTenant(ctx, tenantID)
	if err != nil {
		return err
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	existing, ok := i.byTenant[tenantID]
	if !ok {
		existing = &tenantIndex{}
		i.byTenant[tenantID] = existing
	}
	idSet := make(map[string]struct{}, len(existing.entries)+len(embeddings))
	for _, entry := range existing.entries {
		idSet[entry.id] = struct{}{}
	}
	for _, emb := range embeddings {
		if len(emb.Bits) == 0 || emb.ID == "" {
			continue
		}
		if _, found := idSet[emb.ID]; found {
			continue
		}
		cpy := make([]byte, len(emb.Bits))
		copy(cpy, emb.Bits)
		existing.entries = append(existing.entries, binaryEntry{id: emb.ID, bits: cpy})
		idSet[emb.ID] = struct{}{}
	}
	existing.loaded = true

	i.logger.Info("binary index loaded",
		zap.String("tenant_id", tenantID),
		zap.Int("count", len(existing.entries)))

	return nil
}

// Add inserts a binary embedding into the index.
func (i *InMemoryBinaryIndex) Add(id string, binaryEmbedding []byte, tenantID string) {
	if id == "" || len(binaryEmbedding) == 0 {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	ti, ok := i.byTenant[tenantID]
	if !ok {
		ti = &tenantIndex{loaded: false}
		i.byTenant[tenantID] = ti
	}
	cpy := make([]byte, len(binaryEmbedding))
	copy(cpy, binaryEmbedding)
	ti.entries = append(ti.entries, binaryEntry{id: id, bits: cpy})
}

// Search performs a linear scan with Hamming distance.
func (i *InMemoryBinaryIndex) Search(binaryQuery []byte, tenantID string, limit int) ([]BinaryCandidate, error) {
	if limit <= 0 {
		return []BinaryCandidate{}, nil
	}
	i.mu.RLock()
	ti, ok := i.byTenant[tenantID]
	i.mu.RUnlock()
	if !ok || !ti.loaded {
		return []BinaryCandidate{}, nil
	}

	candidates := make([]BinaryCandidate, 0, len(ti.entries))
	for _, entry := range ti.entries {
		dist := hammingDistance(binaryQuery, entry.bits)
		candidates = append(candidates, BinaryCandidate{ID: entry.id, Distance: dist})
	}

	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].Distance == candidates[b].Distance {
			return candidates[a].ID < candidates[b].ID
		}
		return candidates[a].Distance < candidates[b].Distance
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func hammingDistance(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	dist := 0
	for i := 0; i < n; i++ {
		dist += bits.OnesCount8(a[i] ^ b[i])
	}
	// If lengths differ, count remaining bits as distance.
	if len(a) > n {
		for i := n; i < len(a); i++ {
			dist += bits.OnesCount8(a[i])
		}
	}
	if len(b) > n {
		for i := n; i < len(b); i++ {
			dist += bits.OnesCount8(b[i])
		}
	}
	return dist
}

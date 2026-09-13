package cache

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/dgraph-io/ristretto/v2"
	"github.com/eko/gocache/lib/v4/cache"
	"github.com/eko/gocache/lib/v4/store"
	ristretto_store "github.com/eko/gocache/store/ristretto/v4"
	redis_store "github.com/eko/gocache/store/redis/v4"
	"github.com/redis/go-redis/v9"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// Manager implements QueryCache using gocache backends
type Manager struct {
	cfg     *config.CacheConfig
	enabled bool

	// Caches store serialized JSON bytes for compatibility across stores
	embeddingCache cache.CacheInterface[[]byte]
	searchCache    cache.CacheInterface[[]byte]
	answerCache    cache.CacheInterface[[]byte]

	// Stats
	stats CacheStats

	logger *zap.Logger
}

// NewManager creates a new cache manager
func NewManager(cfg *config.CacheConfig, logger *zap.Logger) (QueryCache, error) {
	if cfg == nil || !cfg.Enabled {
		logger.Info("cache disabled, using noop cache")
		return NewNoopCache(), nil
	}

	m := &Manager{
		cfg:     cfg,
		enabled: true,
		logger:  logger.Named("cache"),
	}

	var err error

	// Initialize embedding cache
	if cfg.Embedding.Enabled {
		ttl := time.Duration(cfg.Embedding.TTLSeconds) * time.Second
		m.embeddingCache, err = m.newStore("embedding", cfg.Embedding.MaxSize, ttl)
		if err != nil {
			return nil, err
		}
		m.logger.Info("embedding cache initialized",
			zap.Int("max_size", cfg.Embedding.MaxSize),
			zap.Duration("ttl", ttl),
			zap.Bool("sliding_ttl", cfg.Embedding.SlidingTTL))
	}

	// Initialize search cache
	if cfg.Search.Enabled {
		ttl := time.Duration(cfg.Search.TTLSeconds) * time.Second
		m.searchCache, err = m.newStore("search", cfg.Search.MaxSize, ttl)
		if err != nil {
			return nil, err
		}
		m.logger.Info("search cache initialized",
			zap.Int("max_size", cfg.Search.MaxSize),
			zap.Duration("ttl", ttl),
			zap.Bool("sliding_ttl", cfg.Search.SlidingTTL))
	}

	// Initialize answer cache
	if cfg.Answer.Enabled {
		ttl := time.Duration(cfg.Answer.TTLSeconds) * time.Second
		m.answerCache, err = m.newStore("answer", cfg.Answer.MaxSize, ttl)
		if err != nil {
			return nil, err
		}
		m.logger.Info("answer cache initialized",
			zap.Int("max_size", cfg.Answer.MaxSize),
			zap.Duration("ttl", ttl),
			zap.Bool("sliding_ttl", cfg.Answer.SlidingTTL))
	}

	m.logger.Info("cache manager initialized",
		zap.String("store_type", cfg.StoreType),
		zap.Bool("embedding_enabled", cfg.Embedding.Enabled),
		zap.Bool("search_enabled", cfg.Search.Enabled),
		zap.Bool("answer_enabled", cfg.Answer.Enabled))

	return m, nil
}

// newStore creates a cache store based on configuration
func (m *Manager) newStore(tier string, maxSize int, ttl time.Duration) (cache.CacheInterface[[]byte], error) {
	switch m.cfg.StoreType {
	case "redis":
		return m.newRedisStore(ttl)
	case "chain":
		return m.newChainStore(tier, maxSize, ttl)
	default: // "memory" or anything else
		return m.newRistrettoStore(maxSize)
	}
}

func (m *Manager) newRistrettoStore(maxSize int) (cache.CacheInterface[[]byte], error) {
	ristrettoCache, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
		NumCounters: int64(maxSize * 10),
		MaxCost:     int64(maxSize),
		BufferItems: 64,
	})
	if err != nil {
		return nil, err
	}

	ristrettoStore := ristretto_store.NewRistretto(ristrettoCache)
	return cache.New[[]byte](ristrettoStore), nil
}

func (m *Manager) newRedisStore(ttl time.Duration) (cache.CacheInterface[[]byte], error) {
	client := redis.NewClient(&redis.Options{
		Addr:     m.cfg.Redis.Address,
		Password: m.cfg.Redis.Password,
		DB:       m.cfg.Redis.DB,
	})

	redisStore := redis_store.NewRedis(client, store.WithExpiration(ttl))
	return cache.New[[]byte](redisStore), nil
}

func (m *Manager) newChainStore(tier string, maxSize int, ttl time.Duration) (cache.CacheInterface[[]byte], error) {
	ristrettoCache, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
		NumCounters: int64(maxSize * 10),
		MaxCost:     int64(maxSize),
		BufferItems: 64,
	})
	if err != nil {
		return nil, err
	}
	ristrettoStore := ristretto_store.NewRistretto(ristrettoCache)
	memCache := cache.New[[]byte](ristrettoStore)

	client := redis.NewClient(&redis.Options{
		Addr:     m.cfg.Redis.Address,
		Password: m.cfg.Redis.Password,
		DB:       m.cfg.Redis.DB,
	})
	redisStore := redis_store.NewRedis(client, store.WithExpiration(ttl))
	redisCache := cache.New[[]byte](redisStore)

	return cache.NewChain[[]byte](memCache, redisCache), nil
}

// GetEmbedding retrieves a cached embedding
func (m *Manager) GetEmbedding(ctx context.Context, queryText string) ([]float64, bool) {
	if m.embeddingCache == nil {
		return nil, false
	}

	key := EmbeddingKey(queryText)
	data, err := m.embeddingCache.Get(ctx, key)
	if err != nil {
		atomic.AddInt64(&m.stats.EmbeddingMisses, 1)
		return nil, false
	}

	var embedding []float64
	if err := json.Unmarshal(data, &embedding); err != nil {
		m.logger.Warn("failed to unmarshal cached embedding", zap.Error(err))
		atomic.AddInt64(&m.stats.EmbeddingMisses, 1)
		return nil, false
	}

	atomic.AddInt64(&m.stats.EmbeddingHits, 1)
	m.logger.Debug("embedding cache hit", zap.String("key_prefix", key[:20]))

	// Sliding TTL: re-set the value to extend TTL on hit
	if m.cfg.Embedding.SlidingTTL {
		ttl := time.Duration(m.cfg.Embedding.TTLSeconds) * time.Second
		if err := m.embeddingCache.Set(ctx, key, data, store.WithExpiration(ttl)); err != nil {
			m.logger.Debug("failed to extend embedding TTL", zap.Error(err))
		}
	}

	return embedding, true
}

// SetEmbedding stores an embedding in cache
func (m *Manager) SetEmbedding(ctx context.Context, queryText string, embedding []float64) error {
	if m.embeddingCache == nil {
		return nil
	}

	data, err := json.Marshal(embedding)
	if err != nil {
		return err
	}

	key := EmbeddingKey(queryText)
	ttl := time.Duration(m.cfg.Embedding.TTLSeconds) * time.Second
	return m.embeddingCache.Set(ctx, key, data, store.WithExpiration(ttl))
}

// GetSearchResults retrieves cached search results
func (m *Manager) GetSearchResults(ctx context.Context, embedding []float64, topK int, tenantID xid.ID) ([]shared.Chunk, bool) {
	if m.searchCache == nil {
		return nil, false
	}

	key := SearchKey(embedding, topK, tenantID)
	data, err := m.searchCache.Get(ctx, key)
	if err != nil {
		atomic.AddInt64(&m.stats.SearchMisses, 1)
		return nil, false
	}

	var chunks []shared.Chunk
	if err := json.Unmarshal(data, &chunks); err != nil {
		m.logger.Warn("failed to unmarshal cached search results", zap.Error(err))
		atomic.AddInt64(&m.stats.SearchMisses, 1)
		return nil, false
	}

	atomic.AddInt64(&m.stats.SearchHits, 1)
	m.logger.Debug("search cache hit", zap.String("key_prefix", key[:20]), zap.Int("chunks", len(chunks)))

	// Sliding TTL: re-set the value to extend TTL on hit
	if m.cfg.Search.SlidingTTL {
		ttl := time.Duration(m.cfg.Search.TTLSeconds) * time.Second
		if err := m.searchCache.Set(ctx, key, data, store.WithExpiration(ttl)); err != nil {
			m.logger.Debug("failed to extend search TTL", zap.Error(err))
		}
	}

	return chunks, true
}

// SetSearchResults stores search results in cache
func (m *Manager) SetSearchResults(ctx context.Context, embedding []float64, topK int, tenantID xid.ID, chunks []shared.Chunk) error {
	if m.searchCache == nil {
		return nil
	}

	data, err := json.Marshal(chunks)
	if err != nil {
		return err
	}

	key := SearchKey(embedding, topK, tenantID)
	ttl := time.Duration(m.cfg.Search.TTLSeconds) * time.Second
	return m.searchCache.Set(ctx, key, data, store.WithExpiration(ttl))
}

// GetAnswer retrieves a cached answer
func (m *Manager) GetAnswer(ctx context.Context, queryText string, chunkIDs []string, maxTokens int) (*shared.Answer, bool) {
	if m.answerCache == nil {
		return nil, false
	}

	key := AnswerKey(queryText, chunkIDs, maxTokens)
	data, err := m.answerCache.Get(ctx, key)
	if err != nil {
		atomic.AddInt64(&m.stats.AnswerMisses, 1)
		return nil, false
	}

	var answer shared.Answer
	if err := json.Unmarshal(data, &answer); err != nil {
		m.logger.Warn("failed to unmarshal cached answer", zap.Error(err))
		atomic.AddInt64(&m.stats.AnswerMisses, 1)
		return nil, false
	}

	atomic.AddInt64(&m.stats.AnswerHits, 1)
	m.logger.Debug("answer cache hit", zap.String("key_prefix", key[:20]))

	// Sliding TTL: re-set the value to extend TTL on hit
	if m.cfg.Answer.SlidingTTL {
		ttl := time.Duration(m.cfg.Answer.TTLSeconds) * time.Second
		if err := m.answerCache.Set(ctx, key, data, store.WithExpiration(ttl)); err != nil {
			m.logger.Debug("failed to extend answer TTL", zap.Error(err))
		}
	}

	return &answer, true
}

// SetAnswer stores an answer in cache
func (m *Manager) SetAnswer(ctx context.Context, queryText string, chunkIDs []string, maxTokens int, answer *shared.Answer) error {
	if m.answerCache == nil {
		return nil
	}

	data, err := json.Marshal(answer)
	if err != nil {
		return err
	}

	key := AnswerKey(queryText, chunkIDs, maxTokens)
	ttl := time.Duration(m.cfg.Answer.TTLSeconds) * time.Second
	return m.answerCache.Set(ctx, key, data, store.WithExpiration(ttl))
}

// InvalidateTenant invalidates cache for a specific tenant
func (m *Manager) InvalidateTenant(ctx context.Context, tenantID xid.ID) error {
	// For in-memory cache, we can't do pattern-based invalidation easily
	// So we clear the search and answer caches entirely (embeddings are tenant-agnostic)
	// For Redis, we could use SCAN with pattern matching

	m.logger.Info("invalidating cache for tenant", zap.String("tenant_id", tenantID.String()))

	if m.searchCache != nil {
		if err := m.searchCache.Clear(ctx); err != nil {
			m.logger.Warn("failed to clear search cache", zap.Error(err))
		}
	}

	if m.answerCache != nil {
		if err := m.answerCache.Clear(ctx); err != nil {
			m.logger.Warn("failed to clear answer cache", zap.Error(err))
		}
	}

	return nil
}

// InvalidateAll clears all caches
func (m *Manager) InvalidateAll(ctx context.Context) error {
	m.logger.Info("invalidating all caches")

	if m.embeddingCache != nil {
		if err := m.embeddingCache.Clear(ctx); err != nil {
			m.logger.Warn("failed to clear embedding cache", zap.Error(err))
		}
	}

	if m.searchCache != nil {
		if err := m.searchCache.Clear(ctx); err != nil {
			m.logger.Warn("failed to clear search cache", zap.Error(err))
		}
	}

	if m.answerCache != nil {
		if err := m.answerCache.Clear(ctx); err != nil {
			m.logger.Warn("failed to clear answer cache", zap.Error(err))
		}
	}

	return nil
}

// Stats returns cache statistics
func (m *Manager) Stats() CacheStats {
	return CacheStats{
		EmbeddingHits:   atomic.LoadInt64(&m.stats.EmbeddingHits),
		EmbeddingMisses: atomic.LoadInt64(&m.stats.EmbeddingMisses),
		SearchHits:      atomic.LoadInt64(&m.stats.SearchHits),
		SearchMisses:    atomic.LoadInt64(&m.stats.SearchMisses),
		AnswerHits:      atomic.LoadInt64(&m.stats.AnswerHits),
		AnswerMisses:    atomic.LoadInt64(&m.stats.AnswerMisses),
	}
}

// IsEnabled returns true if the cache is enabled
func (m *Manager) IsEnabled() bool {
	return m.enabled
}

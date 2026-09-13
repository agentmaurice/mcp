package buffer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"go.uber.org/zap"
)

// Default thresholds
const (
	DefaultSoftThreshold = 512 * 1024     // 512 KB
	DefaultHardThreshold = 2 * 1024 * 1024 // 2 MB
	DefaultTTL           = 600            // 10 minutes
	LocalCacheSize       = 100            // Number of entries in local cache
	LocalCacheTTL        = 2 * time.Minute // Local cache TTL
)

// Reference represents a reference to buffered content
type Reference struct {
	Type      string    `json:"type"`
	Key       string    `json:"key"`
	Size      int64     `json:"size"`
	MimeType  string    `json:"mime_type,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Preview   string    `json:"preview,omitempty"`
	ExpiresIn int64     `json:"expires_in,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// StoreRequest for storing content
type StoreRequest struct {
	Namespace  string                 `json:"namespace"`
	Content    interface{}            `json:"content"`
	MimeType   string                 `json:"mime_type,omitempty"`
	TTLSeconds int                    `json:"ttl_seconds,omitempty"`
	Summary    string                 `json:"summary,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	Text       string                 `json:"text,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// StoreResponse after storage
type StoreResponse struct {
	Reference Reference `json:"reference"`
	Message   string    `json:"message,omitempty"`
}

// LoadResponse when loading content
type LoadResponse struct {
	Content   BufferedContent `json:"content"`
	Reference Reference       `json:"reference"`
}

// BufferedContent is the stored content structure
type BufferedContent struct {
	Version     string                 `json:"version"`
	ToolName    string                 `json:"tool_name,omitempty"`
	Namespace   string                 `json:"namespace"`
	MimeType    string                 `json:"mime_type,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	MCPServerID string                 `json:"mcp_server_id,omitempty"`
	Structured  interface{}            `json:"structured,omitempty"`
	Text        string                 `json:"text,omitempty"`
	Binary      string                 `json:"binary,omitempty"`
	Summary     string                 `json:"summary,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// Metrics holds buffer operation metrics
type Metrics struct {
	mu              sync.RWMutex
	StoreCount      int64
	StoreErrors     int64
	LoadCount       int64
	LoadErrors      int64
	CacheHits       int64
	CacheMisses     int64
	TotalBytesStore int64
	TotalBytesLoad  int64
}

func (m *Metrics) IncrStore(bytes int64, err bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.StoreCount++
	if err {
		m.StoreErrors++
	} else {
		m.TotalBytesStore += bytes
	}
}

func (m *Metrics) IncrLoad(bytes int64, err bool, cacheHit bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LoadCount++
	if err {
		m.LoadErrors++
	} else {
		m.TotalBytesLoad += bytes
	}
	if cacheHit {
		m.CacheHits++
	} else {
		m.CacheMisses++
	}
}

func (m *Metrics) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]interface{}{
		"store_count":        m.StoreCount,
		"store_errors":       m.StoreErrors,
		"load_count":         m.LoadCount,
		"load_errors":        m.LoadErrors,
		"cache_hits":         m.CacheHits,
		"cache_misses":       m.CacheMisses,
		"total_bytes_stored": m.TotalBytesStore,
		"total_bytes_loaded": m.TotalBytesLoad,
	}
}

// Client interacts with the AgentMaurice buffer service
type Client struct {
	baseURL    string
	token      string
	namespace  string
	httpClient *http.Client
	softLimit  int64
	hardLimit  int64
	defaultTTL int
	logger     *zap.Logger
	localCache *expirable.LRU[string, []byte]
	metrics    *Metrics
}

// ClientConfig holds client configuration
type ClientConfig struct {
	ServiceURL    string
	Token         string
	Namespace     string
	SoftThreshold int64
	HardThreshold int64
	DefaultTTL    int
	Logger        *zap.Logger
}

// NewClient creates a new buffer client
func NewClient(cfg ClientConfig) *Client {
	if cfg.SoftThreshold <= 0 {
		cfg.SoftThreshold = DefaultSoftThreshold
	}
	if cfg.HardThreshold <= 0 {
		cfg.HardThreshold = DefaultHardThreshold
	}
	if cfg.DefaultTTL <= 0 {
		cfg.DefaultTTL = DefaultTTL
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "rag"
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}

	// Create local cache with expiration
	localCache := expirable.NewLRU[string, []byte](LocalCacheSize, nil, LocalCacheTTL)

	return &Client{
		baseURL:    cfg.ServiceURL,
		token:      cfg.Token,
		namespace:  cfg.Namespace,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		softLimit:  cfg.SoftThreshold,
		hardLimit:  cfg.HardThreshold,
		defaultTTL: cfg.DefaultTTL,
		logger:     cfg.Logger.Named("buffer-client"),
		localCache: localCache,
		metrics:    &Metrics{},
	}
}

// ShouldBuffer determines if content should be buffered
func (c *Client) ShouldBuffer(size int64) bool {
	return size > c.softLimit
}

// MustBuffer determines if content MUST be buffered
func (c *Client) MustBuffer(size int64) bool {
	return size > c.hardLimit
}

// GetMetrics returns buffer operation metrics
func (c *Client) GetMetrics() map[string]interface{} {
	return c.metrics.GetStats()
}

// Store stores content and returns a reference
func (c *Client) Store(ctx context.Context, req StoreRequest) (*Reference, error) {
	if req.Namespace == "" {
		req.Namespace = c.namespace
	}
	if req.TTLSeconds == 0 {
		req.TTLSeconds = c.defaultTTL
	}

	body, err := json.Marshal(req)
	if err != nil {
		c.metrics.IncrStore(0, true)
		return nil, fmt.Errorf("failed to marshal store request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		c.metrics.IncrStore(0, true)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.metrics.IncrStore(0, true)
		c.logger.Error("Buffer store request failed", zap.Error(err))
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		c.metrics.IncrStore(0, true)
		c.logger.Error("Buffer store failed",
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(bodyBytes)),
		)
		return nil, fmt.Errorf("buffer store failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var storeResp StoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&storeResp); err != nil {
		c.metrics.IncrStore(0, true)
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	c.metrics.IncrStore(int64(len(body)), false)
	c.logger.Info("Content stored in buffer",
		zap.String("key", storeResp.Reference.Key),
		zap.Int64("size", storeResp.Reference.Size),
		zap.String("namespace", req.Namespace),
	)

	// Also store in local cache for quick subsequent access
	if contentBytes, err := json.Marshal(req.Content); err == nil {
		c.localCache.Add(storeResp.Reference.Key, contentBytes)
	}

	return &storeResp.Reference, nil
}

// Load retrieves content from the buffer
func (c *Client) Load(ctx context.Context, key string) (*BufferedContent, error) {
	// Check local cache first
	if cached, ok := c.localCache.Get(key); ok {
		c.metrics.IncrLoad(int64(len(cached)), false, true)
		c.logger.Debug("Buffer content loaded from local cache", zap.String("key", key))

		var content BufferedContent
		if err := json.Unmarshal(cached, &content); err == nil {
			return &content, nil
		}
		// If unmarshal fails, continue to remote fetch
	}

	url := fmt.Sprintf("%s/%s", c.baseURL, key)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		c.metrics.IncrLoad(0, true, false)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.metrics.IncrLoad(0, true, false)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		c.metrics.IncrLoad(0, true, false)
		return nil, fmt.Errorf("buffer key not found: %s", key)
	}
	if resp.StatusCode != http.StatusOK {
		c.metrics.IncrLoad(0, true, false)
		return nil, fmt.Errorf("buffer load failed with status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.metrics.IncrLoad(0, true, false)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var loadResp LoadResponse
	if err := json.Unmarshal(bodyBytes, &loadResp); err != nil {
		c.metrics.IncrLoad(0, true, false)
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	c.metrics.IncrLoad(int64(len(bodyBytes)), false, false)

	// Cache for subsequent access
	c.localCache.Add(key, bodyBytes)

	return &loadResp.Content, nil
}

// Exists checks if a key exists in the buffer
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	// Check local cache first
	if _, ok := c.localCache.Get(key); ok {
		return true, nil
	}

	url := fmt.Sprintf("%s/%s", c.baseURL, key)

	httpReq, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

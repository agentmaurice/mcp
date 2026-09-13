package security

import (
	"crypto/sha256"
	"sync"
)

// ValidationResult caches the outcome of SQL validation.
type ValidationResult struct {
	Info *SQLInfo
	Err  error
}

// ValidationCache is a thread-safe LRU-style cache for SQL validation results.
// It avoids re-parsing identical SQL statements on every request.
type ValidationCache struct {
	mu      sync.RWMutex
	entries map[[32]byte]ValidationResult
	order   [][32]byte
	maxSize int
}

// NewValidationCache creates a cache with the given maximum number of entries.
func NewValidationCache(maxSize int) *ValidationCache {
	if maxSize <= 0 {
		maxSize = 512
	}
	return &ValidationCache{
		entries: make(map[[32]byte]ValidationResult, maxSize),
		order:   make([][32]byte, 0, maxSize),
		maxSize: maxSize,
	}
}

func cacheKey(sql string, rules PortableSQLRules) [32]byte {
	// Include rules in the key so different rule sets don't collide.
	var flags byte
	if rules.AllowSelectStar {
		flags |= 1
	}
	if rules.AllowJSONOperators {
		flags |= 2
	}
	data := append([]byte(sql), flags)
	return sha256.Sum256(data)
}

// Get returns a cached validation result if present.
func (c *ValidationCache) Get(sql string, rules PortableSQLRules) (ValidationResult, bool) {
	key := cacheKey(sql, rules)
	c.mu.RLock()
	defer c.mu.RUnlock()
	result, ok := c.entries[key]
	return result, ok
}

// Put stores a validation result in the cache.
func (c *ValidationCache) Put(sql string, rules PortableSQLRules, result ValidationResult) {
	key := cacheKey(sql, rules)
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[key]; exists {
		return
	}

	// Evict oldest entry if at capacity.
	if len(c.order) >= c.maxSize {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}

	c.entries[key] = result
	c.order = append(c.order, key)
}

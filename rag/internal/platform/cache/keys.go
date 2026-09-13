package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/rs/xid"
)

// EmbeddingKey generates a cache key for embeddings
// Key format: emb:<sha256(normalized_query)>
func EmbeddingKey(queryText string) string {
	normalized := strings.ToLower(strings.TrimSpace(queryText))
	hash := sha256.Sum256([]byte(normalized))
	return "emb:" + hex.EncodeToString(hash[:])
}

// SearchKey generates a cache key for search results
// Key format: search:<sha256(embedding_signature + topK + tenantID)>
func SearchKey(embedding []float64, topK int, tenantID xid.ID) string {
	// Use first 8 and last 8 dimensions for the signature (embeddings are deterministic)
	var embSignature string
	if len(embedding) >= 16 {
		embSignature = fmt.Sprintf("%v%v", embedding[:8], embedding[len(embedding)-8:])
	} else {
		embSignature = fmt.Sprintf("%v", embedding)
	}

	data := fmt.Sprintf("%s:%d:%s", embSignature, topK, tenantID.String())
	hash := sha256.Sum256([]byte(data))
	return "search:" + hex.EncodeToString(hash[:])
}

// AnswerKey generates a cache key for answers
// Key format: ans:<sha256(normalized_query + sorted_chunk_ids + maxTokens)>
func AnswerKey(queryText string, chunkIDs []string, maxTokens int) string {
	// Sort chunk IDs for consistent keys
	sorted := make([]string, len(chunkIDs))
	copy(sorted, chunkIDs)
	sort.Strings(sorted)

	data := fmt.Sprintf("%s:%s:%d",
		strings.ToLower(strings.TrimSpace(queryText)),
		strings.Join(sorted, ","),
		maxTokens)
	hash := sha256.Sum256([]byte(data))
	return "ans:" + hex.EncodeToString(hash[:])
}

// TenantSearchPattern returns a pattern for tenant-specific search cache
// This is used for invalidation - note that Redis supports patterns but in-memory doesn't
func TenantSearchPattern(tenantID xid.ID) string {
	return "search:*" + tenantID.String() + "*"
}

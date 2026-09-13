package retrieve

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
)

// VectorRetriever abstracts a vector-based retrieval backend.
type VectorRetriever interface {
	Retrieve(ctx context.Context, deploymentID string, tenantID string, queryEmbedding []float64, k int, metadataFilters map[string]string) ([]shared.Chunk, error)
	Name() string
}

package retrieve

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"go.uber.org/zap"
)

// FallbackRetriever wraps a primary retriever with a fallback.
type FallbackRetriever struct {
	primary  VectorRetriever
	fallback VectorRetriever
	enabled  bool
	logger   *zap.Logger
}

// NewFallbackRetriever creates a new fallback retriever.
func NewFallbackRetriever(primary, fallback VectorRetriever, enabled bool, logger *zap.Logger) *FallbackRetriever {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FallbackRetriever{
		primary:  primary,
		fallback: fallback,
		enabled:  enabled,
		logger:   logger.Named("fallback-retriever"),
	}
}

func (r *FallbackRetriever) Name() string {
	if r.primary != nil {
		return r.primary.Name()
	}
	return "fallback"
}

func (r *FallbackRetriever) Retrieve(ctx context.Context, deploymentID string, tenantID string, queryEmbedding []float64, k int, metadataFilters map[string]string) ([]shared.Chunk, error) {
	if r.primary == nil {
		return []shared.Chunk{}, nil
	}
	results, err := r.primary.Retrieve(ctx, deploymentID, tenantID, queryEmbedding, k, metadataFilters)
	if err == nil && len(results) > 0 {
		return results, nil
	}
	if !r.enabled || r.fallback == nil {
		return results, err
	}
	r.logger.Warn("primary retriever failed or empty, falling back",
		zap.String("primary", r.primary.Name()),
		zap.String("fallback", r.fallback.Name()),
		zap.Error(err))
	return r.fallback.Retrieve(ctx, deploymentID, tenantID, queryEmbedding, k, metadataFilters)
}

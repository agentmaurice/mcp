package retrieve

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// FloatRetriever uses the existing vector store (float embeddings).
type FloatRetriever struct {
	vectorStore shared.VectorStore
	logger      *zap.Logger
}

// NewFloatRetriever creates a new float retriever.
func NewFloatRetriever(vectorStore shared.VectorStore, logger *zap.Logger) *FloatRetriever {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FloatRetriever{
		vectorStore: vectorStore,
		logger:      logger.Named("float-retriever"),
	}
}

func (r *FloatRetriever) Name() string {
	return "float"
}

func (r *FloatRetriever) Retrieve(ctx context.Context, _ string, tenantID string, queryEmbedding []float64, k int, metadataFilters map[string]string) ([]shared.Chunk, error) {
	tid, err := xid.FromString(tenantID)
	if err != nil {
		return nil, err
	}
	return r.vectorStore.SearchByVector(ctx, queryEmbedding, k, tid, metadataFilters)
}

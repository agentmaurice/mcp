package business

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/pipeline"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// RAGManager handles RAG queries
type RAGManager struct {
	pipeline   *pipeline.Pipeline
	tenantRepo *repository.TenantRepository
	logger     *zap.Logger
}

// NewRAGManager creates a new RAG manager
func NewRAGManager(pipeline *pipeline.Pipeline, tenantRepo *repository.TenantRepository, logger *zap.Logger) *RAGManager {
	return &RAGManager{
		pipeline:   pipeline,
		tenantRepo: tenantRepo,
		logger:     logger.Named("rag-manager"),
	}
}

// Start starts the RAG manager
func (m *RAGManager) Start(ctx context.Context) error {
	m.logger.Info("starting RAG manager")
	return nil
}

// Stop stops the RAG manager
func (m *RAGManager) Stop() error {
	m.logger.Info("stopping RAG manager")
	return nil
}

// Name returns the manager name
func (m *RAGManager) Name() string {
	return "rag-manager"
}

// Health checks the manager health
func (m *RAGManager) Health(ctx context.Context) error {
	// In production, this would check pipeline components
	return nil
}

// Query processes a RAG query
func (m *RAGManager) Query(ctx context.Context, query shared.Query) (*shared.Answer, error) {
	m.logger.Debug("processing RAG query",
		zap.String("query", query.Text),
		zap.String("tenantID", query.TenantID.String()))

	// Resolve tenant if missing using deployment default
	if query.TenantID == xid.NilID() && query.DeploymentID != xid.NilID() {
		t, err := m.tenantRepo.GetDefaultByDeployment(ctx, query.DeploymentID)
		if err != nil {
			created, cerr := m.tenantRepo.CreateForDeployment(ctx, query.DeploymentID, "default", xid.New().String(), true)
			if cerr != nil {
				return nil, err
			}
			query.TenantID = created.ID
		} else {
			query.TenantID = t.ID
		}
	}

	answer, err := m.pipeline.Query(ctx, query)
	if err != nil {
		m.logger.Error("query failed", zap.Error(err))
		return nil, err
	}

	return answer, nil
}

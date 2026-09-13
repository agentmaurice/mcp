package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/jobqueue"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/docint"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/inspect"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/ingestjob"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// IngestSource represents an ingestion source
type IngestSource struct {
	Type                    string                  `json:"type"`
	URL                     string                  `json:"url,omitempty"`
	Content                 string                  `json:"content,omitempty"`
	Title                   string                  `json:"title"`
	Metadata                map[string]interface{}  `json:"metadata,omitempty"`
	DetectDuplicates        bool                    `json:"detect_duplicates,omitempty"`
	DuplicateStrategy       string                  `json:"duplicate_strategy,omitempty"`
	DocType                 string                  `json:"doc_type,omitempty"`
	ContentType             string                  `json:"content_type,omitempty"`
	Size                    int64                   `json:"size,omitempty"`
	DetectContent           bool                    `json:"detect_content,omitempty"`
	ContentDetectionProfile string                  `json:"content_detection_profile,omitempty"`
	CustomDetectionRules    []inspect.DetectionRule `json:"custom_detection_rules,omitempty"`
	ForceIngestion          bool                    `json:"force_ingestion,omitempty"`
}

// IngestResult represents the result of an ingestion request
type IngestResult struct {
	JobID      xid.ID `json:"job_id"`
	DocumentID xid.ID `json:"document_id,omitempty"`
	Status     string `json:"status"` // "pending", "skipped"
	Message    string `json:"message,omitempty"`
}

// ReindexRequest represents a request to reindex embeddings.
type ReindexRequest struct {
	BatchSize int    `json:"batch_size,omitempty"`
	Force     bool   `json:"force,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

// IngestManager manages document ingestion
type IngestManager struct {
	jobRepo    *repository.IngestJobRepository
	tenantRepo *repository.TenantRepository
	deployRepo *repository.DeploymentRepository
	docRepo    *repository.DocumentRepository
	publisher  jobqueue.IngestJobPublisher
	logger     *zap.Logger
}

// NewIngestManager creates a new ingest manager
func NewIngestManager(
	jobRepo *repository.IngestJobRepository,
	tenantRepo *repository.TenantRepository,
	deployRepo *repository.DeploymentRepository,
	docRepo *repository.DocumentRepository,
	publisher jobqueue.IngestJobPublisher,
	logger *zap.Logger,
) *IngestManager {
	return &IngestManager{
		jobRepo:    jobRepo,
		tenantRepo: tenantRepo,
		deployRepo: deployRepo,
		docRepo:    docRepo,
		publisher:  publisher,
		logger:     logger.Named("ingest-manager"),
	}
}

// Start starts the ingest manager
func (m *IngestManager) Start(ctx context.Context) error {
	m.logger.Info("starting ingest manager")
	return nil
}

// Stop stops the ingest manager
func (m *IngestManager) Stop() error {
	m.logger.Info("stopping ingest manager")
	return nil
}

// Name returns the manager name
func (m *IngestManager) Name() string {
	return "ingest-manager"
}

// Health checks the manager health
func (m *IngestManager) Health(ctx context.Context) error {
	// In production, this would check worker status
	return nil
}

// StartIngest creates a new ingestion job or returns existing document if already ingested
func (m *IngestManager) StartIngest(ctx context.Context, deploymentID, tenantID xid.ID, source IngestSource) (*IngestResult, error) {
	m.logger.Info("starting ingestion",
		zap.String("deploymentID", deploymentID.String()),
		zap.String("tenantID", tenantID.String()),
		zap.String("type", source.Type),
		zap.String("title", source.Title),
		zap.Bool("forceIngestion", source.ForceIngestion))

	if deploymentID == xid.NilID() {
		return nil, shared.ErrValidation("deployment_id is required")
	}

	// Ensure tenant exists (create if not)
	if tenantID == xid.NilID() {
		defaultTenant, err := m.tenantRepo.GetDefaultByDeployment(ctx, deploymentID)
		if err != nil {
			// Auto-create default if absent
			created, cerr := m.tenantRepo.CreateForDeployment(ctx, deploymentID, "default", xid.New().String(), true)
			if cerr != nil {
				return nil, shared.ErrValidation("no default tenant and failed to create one")
			}
			tenantID = created.ID
		} else {
			tenantID = defaultTenant.ID
		}
	} else {
		_, err := m.tenantRepo.EnsureTenant(ctx, deploymentID, tenantID, "tenant-"+tenantID.String())
		if err != nil {
			return nil, err
		}
	}

	// Pre-compute a normalized hash when content is already available.
	normalizedHash := ""
	if source.Content != "" {
		normalized := docint.NormalizeText(source.Content)
		if normalized != "" {
			sum := sha256.Sum256([]byte(normalized))
			normalizedHash = hex.EncodeToString(sum[:])
		}
	}

	// Check for existing document or completed job (skip if already successfully ingested)
	if !source.ForceIngestion {
		// URL check
		if source.URL != "" {
			existingDoc, err := m.docRepo.FindByURL(ctx, tenantID, source.URL)
			if err != nil && !isNotFoundError(err) {
				return nil, err
			}
			if err == nil && existingDoc != nil {
				m.logger.Info("document already exists in documents table (url), skipping ingestion",
					zap.String("documentID", existingDoc.ID.String()),
					zap.String("title", existingDoc.Title))
				return &IngestResult{
					JobID:      xid.NilID(),
					DocumentID: existingDoc.ID,
					Status:     "skipped",
					Message:    "document already ingested successfully",
				}, nil
			}
		}

		// Hash check
		if normalizedHash != "" {
			existingDoc, err := m.docRepo.FindByHash(ctx, tenantID, normalizedHash)
			if err != nil && !isNotFoundError(err) {
				return nil, err
			}
			if err == nil && existingDoc != nil {
				m.logger.Info("document already exists in documents table (hash), skipping ingestion",
					zap.String("documentID", existingDoc.ID.String()),
					zap.String("title", existingDoc.Title))
				return &IngestResult{
					JobID:      xid.NilID(),
					DocumentID: existingDoc.ID,
					Status:     "skipped",
					Message:    "document already ingested successfully",
				}, nil
			}
		}

		// Title check
		if source.Title != "" {
			existingDoc, err := m.docRepo.FindByTitle(ctx, tenantID, source.Title)
			if err != nil && !isNotFoundError(err) {
				return nil, err
			}
			if err == nil && existingDoc != nil {
				m.logger.Info("document already exists in documents table (title), skipping ingestion",
					zap.String("documentID", existingDoc.ID.String()),
					zap.String("title", existingDoc.Title))
				return &IngestResult{
					JobID:      xid.NilID(),
					DocumentID: existingDoc.ID,
					Status:     "skipped",
					Message:    "document already ingested successfully",
				}, nil
			}

			// Jobs table fallback by title
			existingJob, err := m.jobRepo.FindCompletedByTitle(ctx, tenantID, source.Title)
			if err != nil {
				m.logger.Warn("failed to check for existing job", zap.Error(err))
			} else if existingJob != nil {
				m.logger.Info("completed job already exists for this title, skipping ingestion",
					zap.String("jobID", existingJob.ID.String()),
					zap.String("title", source.Title))
				return &IngestResult{
					JobID:   existingJob.ID,
					Status:  "skipped",
					Message: "document already processed (job completed)",
				}, nil
			}
		}
	}

	// Convert source to map
	var sourcePayload map[string]interface{}
	sourceBytes, err := json.Marshal(source)
	if err != nil {
		return nil, shared.ErrInternal("failed to serialize source", err)
	}
	if err := json.Unmarshal(sourceBytes, &sourcePayload); err != nil {
		return nil, shared.ErrInternal("failed to convert source to map", err)
	}

	// Create job
	job := &ent.IngestJob{
		ID:            xid.New(),
		TenantID:      tenantID,
		Status:        ingestjob.StatusPending,
		SourceType:    source.Type,
		SourcePayload: sourcePayload,
		Progress:      0,
	}

	created, err := m.jobRepo.Create(ctx, job)
	if err != nil {
		return nil, err
	}

	m.publishJob(ctx, created.ID)

	m.logger.Info("ingestion job created", zap.String("jobID", created.ID.String()))

	return &IngestResult{
		JobID:  created.ID,
		Status: "pending",
	}, nil
}

func isNotFoundError(err error) bool {
	var appErr *shared.AppError
	return errors.As(err, &appErr) && appErr.Code == 404
}

// StartReindex creates a new reindex job for a tenant.
func (m *IngestManager) StartReindex(ctx context.Context, deploymentID, tenantID xid.ID, req ReindexRequest) (*IngestResult, error) {
	m.logger.Info("starting reindex job",
		zap.String("deploymentID", deploymentID.String()),
		zap.String("tenantID", tenantID.String()),
		zap.Int("batch_size", req.BatchSize),
		zap.Bool("force", req.Force))

	if deploymentID == xid.NilID() {
		return nil, shared.ErrValidation("deployment_id is required")
	}

	// Ensure tenant exists and belongs to deployment
	if tenantID == xid.NilID() {
		defaultTenant, err := m.tenantRepo.GetDefaultByDeployment(ctx, deploymentID)
		if err != nil {
			return nil, shared.ErrValidation("tenant_id is required for reindex")
		}
		tenantID = defaultTenant.ID
	} else {
		if _, err := m.tenantRepo.EnsureTenant(ctx, deploymentID, tenantID, "tenant-"+tenantID.String()); err != nil {
			return nil, err
		}
	}

	if req.BatchSize <= 0 {
		req.BatchSize = 100
	}
	if req.Mode == "" {
		req.Mode = "int8_binary"
	}

	payload := map[string]interface{}{
		"batch_size": req.BatchSize,
		"force":      req.Force,
		"mode":       req.Mode,
	}

	job := &ent.IngestJob{
		ID:            xid.New(),
		TenantID:      tenantID,
		Status:        ingestjob.StatusPending,
		SourceType:    "reindex",
		SourcePayload: payload,
		Progress:      0,
	}

	created, err := m.jobRepo.Create(ctx, job)
	if err != nil {
		return nil, err
	}

	m.publishJob(ctx, created.ID)

	return &IngestResult{
		JobID:  created.ID,
		Status: "pending",
	}, nil
}

// GetJobStatus retrieves the status of an ingestion job
func (m *IngestManager) GetJobStatus(ctx context.Context, jobID xid.ID) (*ent.IngestJob, error) {
	job, err := m.jobRepo.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}

	return job, nil
}

func (m *IngestManager) publishJob(ctx context.Context, jobID xid.ID) {
	if m.publisher == nil {
		return
	}

	if err := m.publisher.PublishIngestJob(ctx, jobID); err != nil {
		m.logger.Warn("failed to publish ingest job",
			zap.String("job_id", jobID.String()),
			zap.Error(err))
	}
}

// ListJobs lists all jobs for a tenant
func (m *IngestManager) ListJobs(ctx context.Context, tenantID xid.ID) ([]*ent.IngestJob, error) {
	jobs, err := m.jobRepo.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return jobs, nil
}

package repository

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/ingestjob"
	"github.com/rs/xid"
)

// IngestJobRepository handles ingest job data access
type IngestJobRepository struct {
	client *ent.Client
}

// NewIngestJobRepository creates a new ingest job repository
func NewIngestJobRepository(client *ent.Client) *IngestJobRepository {
	return &IngestJobRepository{client: client}
}

// Create creates a new ingest job
func (r *IngestJobRepository) Create(ctx context.Context, job *ent.IngestJob) (*ent.IngestJob, error) {
	created, err := r.client.IngestJob.Create().
		SetID(job.ID).
		SetTenantID(job.TenantID).
		SetStatus(job.Status).
		SetSourceType(job.SourceType).
		SetSourcePayload(job.SourcePayload).
		SetNillableMessage(&job.Message).
		SetProgress(job.Progress).
		Save(ctx)

	if err != nil {
		return nil, shared.ErrInternal("failed to create ingest job", err)
	}

	return created, nil
}

// CountByTenant returns the number of ingest jobs for a tenant.
func (r *IngestJobRepository) CountByTenant(ctx context.Context, tenantID xid.ID) (int, error) {
	count, err := r.client.IngestJob.Query().
		Where(ingestjob.TenantIDEQ(tenantID)).
		Count(ctx)
	if err != nil {
		return 0, shared.ErrInternal("failed to count ingest jobs", err)
	}
	return count, nil
}

// DeleteByTenant deletes all ingest jobs for a tenant and returns the count.
func (r *IngestJobRepository) DeleteByTenant(ctx context.Context, tenantID xid.ID) (int, error) {
	count, err := r.client.IngestJob.Delete().
		Where(ingestjob.TenantIDEQ(tenantID)).
		Exec(ctx)
	if err != nil {
		return 0, shared.ErrInternal("failed to delete ingest jobs by tenant", err)
	}
	return count, nil
}

// Get retrieves an ingest job by ID
func (r *IngestJobRepository) Get(ctx context.Context, id xid.ID) (*ent.IngestJob, error) {
	job, err := r.client.IngestJob.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("ingest job")
		}
		return nil, shared.ErrInternal("failed to get ingest job", err)
	}
	return job, nil
}

// UpdateStatus updates the status of an ingest job
func (r *IngestJobRepository) UpdateStatus(ctx context.Context, id xid.ID, status ingestjob.Status, message string, progress int) error {
	update := r.client.IngestJob.UpdateOneID(id).
		SetStatus(status).
		SetProgress(progress)

	if message != "" {
		update = update.SetMessage(message)
	}

	err := update.Exec(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return shared.ErrNotFound("ingest job")
		}
		return shared.ErrInternal("failed to update ingest job", err)
	}

	return nil
}

// UpdateFindings stores content inspection findings for a job
func (r *IngestJobRepository) UpdateFindings(ctx context.Context, id xid.ID, profile string, hasFindings bool, findings map[string]interface{}) error {
	update := r.client.IngestJob.UpdateOneID(id).
		SetHasContentFindings(hasFindings).
		SetContentDetectionProfile(profile)
	if findings != nil {
		update = update.SetContentFindings(findings)
	}
	if err := update.Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return shared.ErrNotFound("ingest job")
		}
		return shared.ErrInternal("failed to update ingest job findings", err)
	}
	return nil
}

// ListByTenant retrieves all ingest jobs for a tenant
func (r *IngestJobRepository) ListByTenant(ctx context.Context, tenantID xid.ID) ([]*ent.IngestJob, error) {
	jobs, err := r.client.IngestJob.Query().
		Where(ingestjob.TenantIDEQ(tenantID)).
		Order(ent.Desc(ingestjob.FieldCreatedAt)).
		All(ctx)

	if err != nil {
		return nil, shared.ErrInternal("failed to list ingest jobs", err)
	}

	return jobs, nil
}

// GetPending retrieves pending ingest jobs for processing
func (r *IngestJobRepository) GetPending(ctx context.Context, limit int) ([]*ent.IngestJob, error) {
	jobs, err := r.client.IngestJob.Query().
		Where(ingestjob.StatusEQ(ingestjob.StatusPending)).
		Order(ent.Asc(ingestjob.FieldCreatedAt)).
		Limit(limit).
		All(ctx)

	if err != nil {
		return nil, shared.ErrInternal("failed to get pending jobs", err)
	}

	return jobs, nil
}

// ClaimPending atomically marks a pending job as running and returns it.
// Returns (nil, false, nil) if the job is not pending anymore.
func (r *IngestJobRepository) ClaimPending(ctx context.Context, id xid.ID) (*ent.IngestJob, bool, error) {
	updated, err := r.client.IngestJob.Update().
		Where(
			ingestjob.IDEQ(id),
			ingestjob.StatusEQ(ingestjob.StatusPending),
		).
		SetStatus(ingestjob.StatusRunning).
		SetProgress(10).
		Save(ctx)
	if err != nil {
		return nil, false, shared.ErrInternal("failed to claim ingest job", err)
	}
	if updated == 0 {
		return nil, false, nil
	}

	job, err := r.client.IngestJob.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, false, shared.ErrNotFound("ingest job")
		}
		return nil, false, shared.ErrInternal("failed to get claimed ingest job", err)
	}
	return job, true, nil
}

// FindCompletedByTitle finds a completed job by title in source_payload for a tenant.
// Returns nil if not found (not an error).
func (r *IngestJobRepository) FindCompletedByTitle(ctx context.Context, tenantID xid.ID, title string) (*ent.IngestJob, error) {
	jobs, err := r.client.IngestJob.Query().
		Where(
			ingestjob.TenantIDEQ(tenantID),
			ingestjob.StatusEQ(ingestjob.StatusCompleted),
		).
		Order(ent.Desc(ingestjob.FieldCreatedAt)).
		All(ctx)

	if err != nil {
		return nil, shared.ErrInternal("failed to query jobs", err)
	}

	// Filter by title in source_payload
	for _, job := range jobs {
		if payload := job.SourcePayload; payload != nil {
			if t, ok := payload["title"].(string); ok && t == title {
				return job, nil
			}
		}
	}

	return nil, nil // Not found, but not an error
}

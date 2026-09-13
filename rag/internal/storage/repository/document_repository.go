package repository

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/document"
	"github.com/rs/xid"
)

// DocumentRepository handles document data access
type DocumentRepository struct {
	client *ent.Client
}

// NewDocumentRepository creates a new document repository
func NewDocumentRepository(client *ent.Client) *DocumentRepository {
	return &DocumentRepository{client: client}
}

// Create creates a new document
func (r *DocumentRepository) Create(ctx context.Context, doc *ent.Document) (*ent.Document, error) {
	builder := r.client.Document.Create().
		SetID(doc.ID).
		SetTenantID(doc.TenantID).
		SetTitle(doc.Title).
		SetNillableSourceURL(&doc.SourceURL).
		SetMetadata(doc.Metadata).
		SetNillableNormalizedHash(doc.NormalizedHash).
		SetDocType(doc.DocType).
		SetNillableContentType(doc.ContentType).
		SetNillableSize(doc.Size).
		SetNillableDuplicateOf(doc.DuplicateOf).
		SetHasPii(doc.HasPii).
		SetPiiSummary(doc.PiiSummary)

	created, err := builder.Save(ctx)
	if err != nil {
		return nil, shared.ErrInternal("failed to create document", err)
	}

	return created, nil
}

// CountByTenant returns the number of documents for a tenant.
func (r *DocumentRepository) CountByTenant(ctx context.Context, tenantID xid.ID) (int, error) {
	count, err := r.client.Document.Query().
		Where(document.TenantIDEQ(tenantID)).
		Count(ctx)
	if err != nil {
		return 0, shared.ErrInternal("failed to count documents", err)
	}
	return count, nil
}

// Delete deletes a document by ID.
func (r *DocumentRepository) Delete(ctx context.Context, id xid.ID) error {
	err := r.client.Document.DeleteOneID(id).Exec(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return shared.ErrNotFound("document")
		}
		return shared.ErrInternal("failed to delete document", err)
	}
	return nil
}

// DeleteByTenant deletes all documents for a tenant and returns the count.
func (r *DocumentRepository) DeleteByTenant(ctx context.Context, tenantID xid.ID) (int, error) {
	count, err := r.client.Document.Delete().
		Where(document.TenantIDEQ(tenantID)).
		Exec(ctx)
	if err != nil {
		return 0, shared.ErrInternal("failed to delete documents by tenant", err)
	}
	return count, nil
}

// Get retrieves a document by ID
func (r *DocumentRepository) Get(ctx context.Context, id xid.ID) (*ent.Document, error) {
	doc, err := r.client.Document.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("document")
		}
		return nil, shared.ErrInternal("failed to get document", err)
	}
	return doc, nil
}

// ListByTenant retrieves all documents for a tenant
func (r *DocumentRepository) ListByTenant(ctx context.Context, tenantID xid.ID) ([]*ent.Document, error) {
	docs, err := r.client.Document.Query().
		Where(document.TenantIDEQ(tenantID)).
		WithChunks().
		All(ctx)

	if err != nil {
		return nil, shared.ErrInternal("failed to list documents", err)
	}

	return docs, nil
}

// DocumentListOptions contains options for listing documents
type DocumentListOptions struct {
	TenantID   xid.ID
	Limit      int
	Offset     int
	DocType    string
	TitleQuery string
}

// ListByTenantPaginated retrieves documents for a tenant with pagination and filters
func (r *DocumentRepository) ListByTenantPaginated(ctx context.Context, opts DocumentListOptions) ([]*ent.Document, int, error) {
	query := r.client.Document.Query().
		Where(document.TenantIDEQ(opts.TenantID))

	// Apply filters
	if opts.DocType != "" {
		query = query.Where(document.DocTypeEQ(opts.DocType))
	}
	if opts.TitleQuery != "" {
		query = query.Where(document.TitleContainsFold(opts.TitleQuery))
	}

	// Get total count
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, shared.ErrInternal("failed to count documents", err)
	}

	// Apply pagination and ordering
	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}
	query = query.Order(ent.Desc(document.FieldCreatedAt))

	docs, err := query.All(ctx)
	if err != nil {
		return nil, 0, shared.ErrInternal("failed to list documents", err)
	}

	return docs, total, nil
}

// FindByHash returns a document by normalized hash for a tenant.
func (r *DocumentRepository) FindByHash(ctx context.Context, tenantID xid.ID, hash string) (*ent.Document, error) {
	doc, err := r.client.Document.Query().
		Where(document.TenantIDEQ(tenantID), document.NormalizedHashEQ(hash), document.HasChunks()).
		Order(ent.Desc(document.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("document")
		}
		return nil, shared.ErrInternal("failed to find document by hash", err)
	}
	return doc, nil
}

// FindByURL returns a document by source URL for a tenant.
func (r *DocumentRepository) FindByURL(ctx context.Context, tenantID xid.ID, url string) (*ent.Document, error) {
	doc, err := r.client.Document.Query().
		Where(document.TenantIDEQ(tenantID), document.SourceURLEQ(url), document.HasChunks()).
		Order(ent.Desc(document.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("document")
		}
		return nil, shared.ErrInternal("failed to find document by URL", err)
	}
	return doc, nil
}

// FindByTitle returns a document by title for a tenant.
func (r *DocumentRepository) FindByTitle(ctx context.Context, tenantID xid.ID, title string) (*ent.Document, error) {
	doc, err := r.client.Document.Query().
		Where(document.TenantIDEQ(tenantID), document.TitleEQ(title), document.HasChunks()).
		Order(ent.Desc(document.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("document")
		}
		return nil, shared.ErrInternal("failed to find document by title", err)
	}
	return doc, nil
}

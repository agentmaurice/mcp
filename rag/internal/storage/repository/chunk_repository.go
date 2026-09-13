package repository

import (
	"context"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/chunk"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/document"
	"github.com/rs/xid"
)

// ChunkRepository handles chunk data access
type ChunkRepository struct {
	client *ent.Client
}

// BinaryEmbeddingRecord holds a binary embedding for indexing.
type BinaryEmbeddingRecord struct {
	ID   string
	Bits []byte
}

// NewChunkRepository creates a new chunk repository
func NewChunkRepository(client *ent.Client) *ChunkRepository {
	return &ChunkRepository{client: client}
}

// CreateBatch creates multiple chunks in a transaction
func (r *ChunkRepository) CreateBatch(ctx context.Context, chunks []*ent.Chunk) error {
	bulk := make([]*ent.ChunkCreate, len(chunks))
	for i, ch := range chunks {
		create := r.client.Chunk.Create().
			SetID(ch.ID).
			SetDocumentID(ch.DocumentID).
			SetText(ch.Text).
			SetMetadata(ch.Metadata).
			SetEmbeddingModel(ch.EmbeddingModel).
			SetNillableVectorID(&ch.VectorID)
		if len(ch.EmbeddingInt8) > 0 {
			create.SetEmbeddingInt8(ch.EmbeddingInt8)
		}
		if len(ch.EmbeddingBinary) > 0 {
			create.SetEmbeddingBinary(ch.EmbeddingBinary)
		}
		bulk[i] = create
	}

	if err := r.client.Chunk.CreateBulk(bulk...).Exec(ctx); err != nil {
		return shared.ErrInternal("failed to create chunks", err)
	}

	return nil
}

// CountByTenant returns the number of chunks for a tenant.
func (r *ChunkRepository) CountByTenant(ctx context.Context, tenantID xid.ID) (int, error) {
	count, err := r.client.Chunk.Query().
		Where(chunk.HasDocumentWith(document.TenantIDEQ(tenantID))).
		Count(ctx)
	if err != nil {
		return 0, shared.ErrInternal("failed to count chunks", err)
	}
	return count, nil
}

// Get retrieves a chunk by ID
func (r *ChunkRepository) Get(ctx context.Context, id xid.ID) (*ent.Chunk, error) {
	ch, err := r.client.Chunk.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("chunk")
		}
		return nil, shared.ErrInternal("failed to get chunk", err)
	}
	return ch, nil
}

// ListByDocument retrieves all chunks for a document
func (r *ChunkRepository) ListByDocument(ctx context.Context, documentID xid.ID) ([]*ent.Chunk, error) {
	chunks, err := r.client.Chunk.Query().
		Where(chunk.DocumentIDEQ(documentID)).
		Order(ent.Asc(chunk.FieldID)).
		All(ctx)

	if err != nil {
		return nil, shared.ErrInternal("failed to list chunks", err)
	}

	return chunks, nil
}

// ListByTenant retrieves chunks for a tenant with pagination.
func (r *ChunkRepository) ListByTenant(ctx context.Context, tenantID xid.ID, offset, limit int) ([]*ent.Chunk, error) {
	query := r.client.Chunk.Query().
		Where(chunk.HasDocumentWith(document.TenantIDEQ(tenantID))).
		Order(ent.Asc(chunk.FieldID))
	if offset > 0 {
		query = query.Offset(offset)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	chunks, err := query.All(ctx)
	if err != nil {
		return nil, shared.ErrInternal("failed to list chunks by tenant", err)
	}
	return chunks, nil
}

// GetByIDsForTenant retrieves chunks by IDs for a tenant.
func (r *ChunkRepository) GetByIDsForTenant(ctx context.Context, ids []string, tenantID xid.ID) ([]shared.Chunk, error) {
	if len(ids) == 0 {
		return []shared.Chunk{}, nil
	}
	xids := make([]xid.ID, 0, len(ids))
	for _, id := range ids {
		if parsed, err := xid.FromString(id); err == nil {
			xids = append(xids, parsed)
		}
	}
	if len(xids) == 0 {
		return []shared.Chunk{}, nil
	}
	results, err := r.client.Chunk.Query().
		Where(
			chunk.IDIn(xids...),
			chunk.HasDocumentWith(document.TenantIDEQ(tenantID)),
		).
		All(ctx)
	if err != nil {
		return nil, shared.ErrInternal("failed to get chunks by ids", err)
	}

	out := make([]shared.Chunk, 0, len(results))
	for _, ch := range results {
		out = append(out, shared.Chunk{
			ID:         ch.ID.String(),
			DocumentID: ch.DocumentID,
			TenantID:   tenantID,
			Text:       ch.Text,
			Metadata:   ch.Metadata,
			VectorID:   ch.VectorID,
		})
	}
	return out, nil
}

// GetEmbeddingsInt8ByIDs retrieves int8 embeddings for given chunk IDs.
func (r *ChunkRepository) GetEmbeddingsInt8ByIDs(ctx context.Context, ids []string) (map[string][]byte, error) {
	if len(ids) == 0 {
		return map[string][]byte{}, nil
	}
	xids := make([]xid.ID, 0, len(ids))
	for _, id := range ids {
		if parsed, err := xid.FromString(id); err == nil {
			xids = append(xids, parsed)
		}
	}
	if len(xids) == 0 {
		return map[string][]byte{}, nil
	}
	results, err := r.client.Chunk.Query().
		Where(chunk.IDIn(xids...)).
		All(ctx)
	if err != nil {
		return nil, shared.ErrInternal("failed to load int8 embeddings", err)
	}
	out := make(map[string][]byte, len(results))
	for _, ch := range results {
		if len(ch.EmbeddingInt8) == 0 {
			continue
		}
		out[ch.ID.String()] = ch.EmbeddingInt8
	}
	return out, nil
}

// UpdateEmbeddings updates the int8 and binary embeddings for a chunk.
func (r *ChunkRepository) UpdateEmbeddings(ctx context.Context, id xid.ID, embInt8 []byte, embBinary []byte) error {
	update := r.client.Chunk.UpdateOneID(id)
	changed := false
	if len(embInt8) > 0 {
		update = update.SetEmbeddingInt8(embInt8)
		changed = true
	}
	if len(embBinary) > 0 {
		update = update.SetEmbeddingBinary(embBinary)
		changed = true
	}
	if !changed {
		return nil
	}
	if err := update.Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return shared.ErrNotFound("chunk")
		}
		return shared.ErrInternal("failed to update chunk embeddings", err)
	}
	return nil
}

// ListBinaryEmbeddingsByTenant retrieves binary embeddings for a tenant.
func (r *ChunkRepository) ListBinaryEmbeddingsByTenant(ctx context.Context, tenantID string) ([]BinaryEmbeddingRecord, error) {
	tid, err := xid.FromString(tenantID)
	if err != nil {
		return nil, shared.ErrValidation("invalid tenant_id")
	}
	results, err := r.client.Chunk.Query().
		Where(
			chunk.HasDocumentWith(document.TenantIDEQ(tid)),
			chunk.EmbeddingBinaryNotNil(),
		).
		All(ctx)
	if err != nil {
		return nil, shared.ErrInternal("failed to list binary embeddings", err)
	}
	out := make([]BinaryEmbeddingRecord, 0, len(results))
	for _, ch := range results {
		if len(ch.EmbeddingBinary) == 0 {
			continue
		}
		out = append(out, BinaryEmbeddingRecord{
			ID:   ch.ID.String(),
			Bits: ch.EmbeddingBinary,
		})
	}
	return out, nil
}

// UpdateVectorID updates the vector ID for a chunk
func (r *ChunkRepository) UpdateVectorID(ctx context.Context, id xid.ID, vectorID string) error {
	err := r.client.Chunk.UpdateOneID(id).
		SetVectorID(vectorID).
		Exec(ctx)

	if err != nil {
		if ent.IsNotFound(err) {
			return shared.ErrNotFound("chunk")
		}
		return shared.ErrInternal("failed to update chunk vector ID", err)
	}

	return nil
}

// DeleteByTenant deletes all chunks for a tenant and returns the count.
func (r *ChunkRepository) DeleteByTenant(ctx context.Context, tenantID xid.ID) (int, error) {
	count, err := r.client.Chunk.Delete().
		Where(chunk.HasDocumentWith(document.TenantIDEQ(tenantID))).
		Exec(ctx)
	if err != nil {
		return 0, shared.ErrInternal("failed to delete chunks by tenant", err)
	}
	return count, nil
}

// DeleteByDocument deletes all chunks for a document
func (r *ChunkRepository) DeleteByDocument(ctx context.Context, documentID xid.ID) error {
	_, err := r.client.Chunk.Delete().
		Where(chunk.DocumentIDEQ(documentID)).
		Exec(ctx)

	if err != nil {
		return shared.ErrInternal("failed to delete chunks", err)
	}

	return nil
}

// SearchByKeyword performs a lightweight lexical search (case-insensitive) for a tenant.
func (r *ChunkRepository) SearchByKeyword(ctx context.Context, tenantID xid.ID, query string, limit int) ([]shared.Chunk, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []shared.Chunk{}, nil
	}

	results, err := r.client.Chunk.Query().
		Where(
			chunk.HasDocumentWith(document.TenantIDEQ(tenantID)),
			chunk.TextContainsFold(q),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, shared.ErrInternal("failed to search chunks by keyword", err)
	}

	out := make([]shared.Chunk, 0, len(results))
	for _, ch := range results {
		out = append(out, shared.Chunk{
			ID:         ch.ID.String(),
			DocumentID: ch.DocumentID,
			TenantID:   tenantID,
			Text:       ch.Text,
			Metadata:   ch.Metadata,
			VectorID:   ch.VectorID,
			Score:      0, // will be set during rerank
		})
	}

	return out, nil
}

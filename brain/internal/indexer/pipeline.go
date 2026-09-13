package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/connector"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer/chunker"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer/embedder"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// Pipeline orchestrates the indexation of sources.
type Pipeline struct {
	storage    storage.Manager
	connectors map[string]connector.Connector
	embedder   embedder.Provider
	cfg        *config.Config
	logger     *zap.Logger
	locksMu    sync.Mutex
	locks      map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// DocumentUpsertRequest contains a fully resolved textual document. Buffer
// references are deliberately resolved by the transport before this layer.
type DocumentUpsertRequest struct {
	TenantID    string
	SourceURI   string
	DocumentURI string
	ContentType string
	Content     []byte
	Metadata    map[string]any
}

type DocumentUpsertResult struct {
	JobID      string
	SourceID   string
	DocumentID string
	Status     string
	Outcome    string
}

// NewPipeline creates a new indexation pipeline.
func NewPipeline(storage storage.Manager, connectors map[string]connector.Connector, emb embedder.Provider, cfg *config.Config, logger *zap.Logger) *Pipeline {
	return &Pipeline{
		storage:    storage,
		connectors: connectors,
		embedder:   emb,
		cfg:        cfg,
		logger:     logger.Named("pipeline"),
		locks:      make(map[string]*keyedLock),
	}
}

// IndexSource indexes a data source asynchronously.
func (p *Pipeline) IndexSource(ctx context.Context, tenantID, sourceType, sourceURI, displayName string, sourceCfg json.RawMessage) (string, error) {
	conn, ok := p.connectors[sourceType]
	if !ok {
		return "", fmt.Errorf("unsupported source type: %s", sourceType)
	}

	unlock := p.acquireLock("source:" + tenantID + ":" + sourceType + ":" + sourceURI)

	// Reuse the oldest source for a stable source identity. Existing duplicates
	// are left untouched and can be cleaned up separately.
	sourceID := ""
	jobID := xid.New().String()
	now := time.Now()

	err := p.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		rows, err := q.QueryContext(ctx,
			`SELECT id FROM sources WHERE tenant_id = ? AND source_type = ? AND source_uri = ? ORDER BY created_at ASC LIMIT 1`,
			tenantID, sourceType, sourceURI)
		if err != nil {
			return err
		}
		if len(rows.Rows) > 0 {
			sourceID = fmt.Sprintf("%v", rows.Rows[0]["id"])
			_, err = q.ExecContext(ctx,
				`UPDATE sources SET display_name = ?, config = ?, status = 'indexing', error_message = NULL, updated_at = ? WHERE id = ?`,
				displayName, string(sourceCfg), now, sourceID)
		} else {
			sourceID = xid.New().String()
			_, err = q.ExecContext(ctx,
				`INSERT INTO sources (id, tenant_id, source_type, source_uri, display_name, config, status, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, 'indexing', ?, ?)`,
				sourceID, tenantID, sourceType, sourceURI, displayName, string(sourceCfg), now, now)
		}
		if err != nil {
			return err
		}

		_, err = q.ExecContext(ctx,
			`INSERT INTO index_jobs (id, tenant_id, source_id, status, created_at)
			 VALUES (?, ?, ?, 'pending', ?)`,
			jobID, tenantID, sourceID, now)
		return err
	})
	if err != nil {
		unlock()
		return "", fmt.Errorf("failed to create source/job: %w", err)
	}

	// Run indexation in background
	go func() {
		defer unlock()
		p.runIndexation(context.Background(), tenantID, sourceID, jobID, conn, sourceCfg)
	}()

	return jobID, nil
}

func (p *Pipeline) acquireLock(key string) func() {
	p.locksMu.Lock()
	lock := p.locks[key]
	if lock == nil {
		lock = &keyedLock{}
		p.locks[key] = lock
	}
	lock.refs++
	p.locksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		p.locksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(p.locks, key)
		}
		p.locksMu.Unlock()
	}
}

// WaitForJob waits until a job reaches a terminal state or the caller context
// is cancelled. It is used only when the MCP caller explicitly opts in.
func (p *Pipeline) WaitForJob(ctx context.Context, tenantID, jobID string) (string, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		db, err := p.storage.OpenTenant(ctx, tenantID)
		if err != nil {
			return "", err
		}
		rows, err := db.QueryContext(ctx, `SELECT status FROM index_jobs WHERE tenant_id = ? AND id = ?`, tenantID, jobID)
		if err != nil {
			return "", err
		}
		if len(rows.Rows) == 0 {
			return "", fmt.Errorf("index job not found")
		}
		status := fmt.Sprintf("%v", rows.Rows[0]["status"])
		if status == "completed" || status == "failed" {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *Pipeline) runIndexation(ctx context.Context, tenantID, sourceID, jobID string, conn connector.Connector, sourceCfg json.RawMessage) {
	p.logger.Info("starting indexation",
		zap.String("tenant", tenantID),
		zap.String("source", sourceID),
		zap.String("job", jobID))

	// Update job to running
	_ = p.updateJobStatus(ctx, tenantID, jobID, "running", 0, 0, 0, "")

	// List files
	files, err := conn.ListFiles(ctx, sourceCfg)
	if err != nil {
		p.failJob(ctx, tenantID, sourceID, jobID, fmt.Sprintf("list files failed: %v", err))
		return
	}

	totalFiles := len(files)
	_ = p.updateJobProgress(ctx, tenantID, jobID, totalFiles, 0, 0)

	if totalFiles == 0 {
		var options struct {
			AllowEmpty bool `json:"allow_empty"`
		}
		_ = json.Unmarshal(sourceCfg, &options)
		if options.AllowEmpty {
			p.completeJob(ctx, tenantID, sourceID, jobID, 0, 0)
		} else {
			p.failJob(ctx, tenantID, sourceID, jobID, "source_empty")
		}
		return
	}

	// Get existing documents for incremental indexation
	existingDocs := p.getExistingDocuments(ctx, tenantID, sourceID)
	added, modified, deleted := conn.DetectChanges(ctx, files, existingDocs)

	p.logger.Info("change detection complete",
		zap.Int("added", len(added)),
		zap.Int("modified", len(modified)),
		zap.Int("deleted", len(deleted)))

	// Delete removed files
	for _, f := range deleted {
		p.deleteDocument(ctx, tenantID, sourceID, f.Path)
	}

	// Process new and modified files
	toProcess := append(added, modified...)
	var processedCount int64
	var totalChunks int64
	var failedCount int64
	var firstFailure string
	var failureMu sync.Mutex

	workers := p.cfg.Indexing.Workers
	if workers <= 0 {
		workers = 2
	}

	fileCh := make(chan shared.SourceFile, len(toProcess))
	for _, f := range toProcess {
		fileCh <- f
	}
	close(fileCh)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range fileCh {
				chunks, err := p.processFile(ctx, tenantID, sourceID, conn, sourceCfg, file)
				if err != nil {
					atomic.AddInt64(&failedCount, 1)
					failureMu.Lock()
					if firstFailure == "" {
						firstFailure = err.Error()
					}
					failureMu.Unlock()
					p.logger.Error("failed to process file",
						zap.String("path", file.Path),
						zap.Error(err))
					continue
				}
				atomic.AddInt64(&processedCount, 1)
				atomic.AddInt64(&totalChunks, int64(chunks))

				processed := int(atomic.LoadInt64(&processedCount))
				_ = p.updateJobProgress(ctx, tenantID, jobID, totalFiles, processed, int(atomic.LoadInt64(&totalChunks)))
			}
		}()
	}

	wg.Wait()
	if failedCount > 0 {
		_ = p.updateJobProgress(ctx, tenantID, jobID, totalFiles, int(processedCount), int(totalChunks))
		p.failJob(ctx, tenantID, sourceID, jobID, fmt.Sprintf("%d file(s) failed; first error: %s", failedCount, firstFailure))
		return
	}

	p.completeJob(ctx, tenantID, sourceID, jobID, int(processedCount), int(totalChunks))
}

func (p *Pipeline) processFile(ctx context.Context, tenantID, sourceID string, conn connector.Connector, sourceCfg json.RawMessage, file shared.SourceFile) (int, error) {
	content, err := conn.ReadFile(ctx, sourceCfg, file.Path)
	if err != nil {
		return 0, fmt.Errorf("read file %s: %w", file.Path, err)
	}

	// Chunk the file
	chunks := chunker.Chunk(content, file.Language, file.Path, p.cfg.Indexing.ChunkMaxTokens)

	// Generate embeddings if provider is available
	var embeddings [][]float32
	if p.embedder != nil && len(chunks) > 0 {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Content
		}
		embeddings, err = p.embedder.Embed(ctx, texts)
		if err != nil {
			return 0, fmt.Errorf("embed file %s: %w", file.Path, err)
		}
	}

	// Compute content hash
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	// Store in DB
	docID := xid.New().String()
	now := time.Now()

	err = p.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		// Delete existing document if re-indexing
		_, _ = q.ExecContext(ctx, `DELETE FROM chunks WHERE document_id IN (SELECT id FROM documents WHERE source_id = ? AND file_path = ?)`, sourceID, file.Path)
		_, _ = q.ExecContext(ctx, `DELETE FROM documents WHERE source_id = ? AND file_path = ?`, sourceID, file.Path)

		// Insert document
		title := file.Path
		_, err := q.ExecContext(ctx,
			`INSERT INTO documents (id, tenant_id, source_id, file_path, title, doc_type, language, content_hash, size_bytes, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			docID, tenantID, sourceID, file.Path, title, file.DocType, file.Language, contentHash, file.Size, now, now)
		if err != nil {
			return fmt.Errorf("insert document: %w", err)
		}

		// Insert chunks
		for i, chunk := range chunks {
			chunkID := xid.New().String()

			var embeddingJSON *string
			var embModel *string
			if embeddings != nil && i < len(embeddings) {
				data, _ := json.Marshal(embeddings[i])
				s := string(data)
				embeddingJSON = &s
				m := p.embedder.Model()
				embModel = &m
			}

			metaJSON, _ := json.Marshal(chunk.Metadata)
			bm25Content := chunk.Content // simplified; could be normalized

			_, err := q.ExecContext(ctx,
				`INSERT INTO chunks (id, tenant_id, document_id, chunk_index, content, chunk_type, symbol_name, start_line, end_line, metadata, embedding, embedding_model, bm25_content, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				chunkID, tenantID, docID, i, chunk.Content, chunk.Type, nullString(chunk.SymbolName),
				chunk.StartLine, chunk.EndLine, string(metaJSON), embeddingJSON, embModel, bm25Content, now)
			if err != nil {
				return fmt.Errorf("insert chunk %d: %w", i, err)
			}
		}

		return nil
	})

	if err != nil {
		return 0, err
	}

	return len(chunks), nil
}

// UpsertDocument schedules one already-resolved textual document for
// idempotent indexation. Calls for the same logical document are serialized so
// an older job cannot overwrite a newer invocation.
func (p *Pipeline) UpsertDocument(ctx context.Context, req DocumentUpsertRequest) (*DocumentUpsertResult, error) {
	hash := sha256.Sum256(req.Content)
	contentHash := hex.EncodeToString(hash[:])
	identity := req.TenantID + ":" + req.SourceURI + ":" + req.DocumentURI
	unlock := p.acquireLock("document:" + identity)

	result := &DocumentUpsertResult{JobID: xid.New().String(), Status: "pending"}
	now := time.Now()
	metadataJSON, err := json.Marshal(req.Metadata)
	if err != nil {
		unlock()
		return nil, fmt.Errorf("marshal document metadata: %w", err)
	}

	// Different documents can target the same logical source concurrently.
	// Serialize canonical source selection/creation without extending that
	// source lock over the asynchronous document processing.
	unlockSource := p.acquireLock("source:" + req.TenantID + ":document:" + req.SourceURI)
	err = storage.WithAtomicWriter(ctx, p.storage, req.TenantID, func(ctx context.Context, q storage.Querier) error {
		sources, err := q.QueryContext(ctx,
			`SELECT id FROM sources WHERE tenant_id = ? AND source_type = 'document' AND source_uri = ? ORDER BY created_at ASC, id ASC LIMIT 1`,
			req.TenantID, req.SourceURI)
		if err != nil {
			return err
		}
		if len(sources.Rows) > 0 {
			result.SourceID = fmt.Sprintf("%v", sources.Rows[0]["id"])
			_, err = q.ExecContext(ctx,
				`UPDATE sources SET status = 'indexing', error_message = NULL, updated_at = ? WHERE id = ?`,
				now, result.SourceID)
		} else {
			result.SourceID = xid.New().String()
			_, err = q.ExecContext(ctx,
				`INSERT INTO sources (id, tenant_id, source_type, source_uri, display_name, config, status, created_at, updated_at)
				 VALUES (?, ?, 'document', ?, ?, '{}', 'indexing', ?, ?)`,
				result.SourceID, req.TenantID, req.SourceURI, req.SourceURI, now, now)
		}
		if err != nil {
			return err
		}

		docs, err := q.QueryContext(ctx,
			`SELECT id, content_hash FROM documents WHERE tenant_id = ? AND source_id = ? AND file_path = ? ORDER BY created_at ASC LIMIT 1`,
			req.TenantID, result.SourceID, req.DocumentURI)
		if err != nil {
			return err
		}
		if len(docs.Rows) > 0 {
			result.DocumentID = fmt.Sprintf("%v", docs.Rows[0]["id"])
			currentHash := fmt.Sprintf("%v", docs.Rows[0]["content_hash"])
			if currentHash == contentHash {
				result.Outcome = "unchanged"
				result.Status = "completed"
			}
		} else {
			result.DocumentID = xid.New().String()
			result.Outcome = "created"
			docType := connector.DetectDocType(req.DocumentURI)
			language := connector.DetectLanguage(req.DocumentURI)
			_, err = q.ExecContext(ctx,
				`INSERT INTO documents (id, tenant_id, source_id, file_path, title, doc_type, language, content_hash, metadata, size_bytes, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, 0, ?, ?)`,
				result.DocumentID, req.TenantID, result.SourceID, req.DocumentURI, req.DocumentURI,
				docType, language, string(metadataJSON), now, now)
			if err != nil {
				return err
			}
		}
		if result.Outcome == "" {
			result.Outcome = "updated"
		}

		if result.Outcome == "unchanged" {
			chunks, err := q.QueryContext(ctx, `SELECT COUNT(*) AS total FROM chunks WHERE tenant_id = ? AND document_id = ?`, req.TenantID, result.DocumentID)
			if err != nil {
				return err
			}
			totalChunks := 0
			if len(chunks.Rows) > 0 {
				totalChunks = intValue(chunks.Rows[0]["total"])
			}
			_, err = q.ExecContext(ctx,
				`INSERT INTO index_jobs (id, tenant_id, source_id, status, progress, total_files, processed_files, total_chunks, completed_at, created_at)
				 VALUES (?, ?, ?, 'completed', 1.0, 1, 0, ?, ?, ?)`,
				result.JobID, req.TenantID, result.SourceID, totalChunks, now, now)
			if err == nil {
				_, err = q.ExecContext(ctx, `UPDATE sources SET status = 'ready', last_indexed_at = ?, updated_at = ? WHERE id = ?`, now, now, result.SourceID)
			}
			return err
		}

		_, err = q.ExecContext(ctx,
			`INSERT INTO index_jobs (id, tenant_id, source_id, status, total_files, created_at)
			 VALUES (?, ?, ?, 'pending', 1, ?)`,
			result.JobID, req.TenantID, result.SourceID, now)
		return err
	})
	unlockSource()
	if err != nil {
		unlock()
		return nil, fmt.Errorf("prepare document upsert: %w", err)
	}

	if result.Outcome == "unchanged" {
		unlock()
		return result, nil
	}

	go func() {
		defer unlock()
		p.runDocumentUpsert(context.Background(), req, result, contentHash, metadataJSON)
	}()
	return result, nil
}

func (p *Pipeline) runDocumentUpsert(ctx context.Context, req DocumentUpsertRequest, result *DocumentUpsertResult, contentHash string, metadataJSON []byte) {
	_ = p.updateJobStatus(ctx, req.TenantID, result.JobID, "running", 1, 0, 0, "")
	language := connector.DetectLanguage(req.DocumentURI)
	docType := connector.DetectDocType(req.DocumentURI)
	chunks := chunker.Chunk(req.Content, language, req.DocumentURI, p.cfg.Indexing.ChunkMaxTokens)
	if len(chunks) == 0 {
		p.failJob(ctx, req.TenantID, result.SourceID, result.JobID, "document_empty")
		return
	}

	var embeddings [][]float32
	if p.embedder != nil {
		texts := make([]string, len(chunks))
		for i, chunk := range chunks {
			texts[i] = chunk.Content
		}
		var err error
		embeddings, err = p.embedder.Embed(ctx, texts)
		if err != nil {
			p.failJob(ctx, req.TenantID, result.SourceID, result.JobID, "embedding_failed")
			return
		}
	}

	now := time.Now()
	stagingDocumentID := "staging:" + result.JobID
	err := p.storage.WithWriter(ctx, req.TenantID, func(ctx context.Context, q storage.Querier) error {
		return p.insertChunks(ctx, q, req.TenantID, stagingDocumentID, chunks, embeddings, now)
	})
	if err != nil {
		p.failJob(ctx, req.TenantID, result.SourceID, result.JobID, "store_document_failed")
		return
	}
	err = storage.WithAtomicWriter(ctx, p.storage, req.TenantID, func(ctx context.Context, q storage.Querier) error {
		if _, err := q.ExecContext(ctx, `DELETE FROM chunks WHERE tenant_id = ? AND document_id = ?`, req.TenantID, result.DocumentID); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `UPDATE chunks SET document_id = ? WHERE tenant_id = ? AND document_id = ?`, result.DocumentID, req.TenantID, stagingDocumentID); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx,
			`UPDATE documents SET title = ?, doc_type = ?, language = ?, content_hash = ?, metadata = ?, size_bytes = ?, updated_at = ? WHERE tenant_id = ? AND id = ?`,
			req.DocumentURI, docType, language, contentHash, string(metadataJSON), len(req.Content), now, req.TenantID, result.DocumentID)
		return err
	})
	if err != nil {
		_ = p.storage.WithWriter(ctx, req.TenantID, func(ctx context.Context, q storage.Querier) error {
			_, cleanupErr := q.ExecContext(ctx, `DELETE FROM chunks WHERE tenant_id = ? AND document_id = ?`, req.TenantID, stagingDocumentID)
			return cleanupErr
		})
		p.failJob(ctx, req.TenantID, result.SourceID, result.JobID, "replace_document_failed")
		return
	}
	p.completeJob(ctx, req.TenantID, result.SourceID, result.JobID, 1, len(chunks))
}

func (p *Pipeline) insertChunks(ctx context.Context, q storage.Querier, tenantID, documentID string, chunks []shared.ChunkInput, embeddings [][]float32, now time.Time) error {
	for i, chunk := range chunks {
		var embeddingJSON *string
		var embModel *string
		if embeddings != nil && i < len(embeddings) {
			data, _ := json.Marshal(embeddings[i])
			s := string(data)
			embeddingJSON = &s
			model := p.embedder.Model()
			embModel = &model
		}
		metaJSON, _ := json.Marshal(chunk.Metadata)
		_, err := q.ExecContext(ctx,
			`INSERT INTO chunks (id, tenant_id, document_id, chunk_index, content, chunk_type, symbol_name, start_line, end_line, metadata, embedding, embedding_model, bm25_content, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			xid.New().String(), tenantID, documentID, i, chunk.Content, chunk.Type, nullString(chunk.SymbolName),
			chunk.StartLine, chunk.EndLine, string(metaJSON), embeddingJSON, embModel, chunk.Content, now)
		if err != nil {
			return fmt.Errorf("insert chunk %d: %w", i, err)
		}
	}
	return nil
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func (p *Pipeline) getExistingDocuments(ctx context.Context, tenantID, sourceID string) []shared.Document {
	db, err := p.storage.OpenTenant(ctx, tenantID)
	if err != nil {
		return nil
	}
	qr, err := db.QueryContext(ctx,
		`SELECT id, file_path, content_hash FROM documents WHERE tenant_id = ? AND source_id = ?`,
		tenantID, sourceID)
	if err != nil {
		return nil
	}

	var docs []shared.Document
	for _, row := range qr.Rows {
		var d shared.Document
		if v, ok := row["id"]; ok && v != nil {
			d.ID = fmt.Sprintf("%v", v)
		}
		if v, ok := row["file_path"]; ok && v != nil {
			d.FilePath = fmt.Sprintf("%v", v)
		}
		if v, ok := row["content_hash"]; ok && v != nil {
			d.ContentHash = fmt.Sprintf("%v", v)
		}
		docs = append(docs, d)
	}
	return docs
}

func (p *Pipeline) deleteDocument(ctx context.Context, tenantID, sourceID, filePath string) {
	_ = p.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		_, _ = q.ExecContext(ctx, `DELETE FROM chunks WHERE document_id IN (SELECT id FROM documents WHERE source_id = ? AND file_path = ?)`, sourceID, filePath)
		_, err := q.ExecContext(ctx, `DELETE FROM documents WHERE source_id = ? AND file_path = ?`, sourceID, filePath)
		return err
	})
}

func (p *Pipeline) updateJobStatus(ctx context.Context, tenantID, jobID, status string, totalFiles, processed, chunks int, errMsg string) error {
	return p.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		now := time.Now()
		_, err := q.ExecContext(ctx,
			`UPDATE index_jobs SET status = ?, total_files = ?, processed_files = ?, total_chunks = ?, error_message = ?, started_at = ? WHERE id = ?`,
			status, totalFiles, processed, chunks, errMsg, now, jobID)
		return err
	})
}

func (p *Pipeline) updateJobProgress(ctx context.Context, tenantID, jobID string, total, processed, chunks int) error {
	progress := 0.0
	if total > 0 {
		progress = float64(processed) / float64(total)
	}
	return p.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		_, err := q.ExecContext(ctx,
			`UPDATE index_jobs SET progress = ?, processed_files = ?, total_files = ?, total_chunks = ? WHERE id = ?`,
			progress, processed, total, chunks, jobID)
		return err
	})
}

func (p *Pipeline) failJob(ctx context.Context, tenantID, sourceID, jobID, errMsg string) {
	p.logger.Error("indexation failed", zap.String("job", jobID), zap.String("error", errMsg))
	now := time.Now()
	_ = p.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		_, _ = q.ExecContext(ctx, `UPDATE index_jobs SET status = 'failed', error_message = ?, completed_at = ? WHERE id = ?`, errMsg, now, jobID)
		_, _ = q.ExecContext(ctx, `UPDATE sources SET status = 'error', error_message = ?, updated_at = ? WHERE id = ?`, errMsg, now, sourceID)
		return nil
	})
}

func (p *Pipeline) completeJob(ctx context.Context, tenantID, sourceID, jobID string, processed, chunks int) {
	p.logger.Info("indexation completed",
		zap.String("job", jobID),
		zap.Int("processed", processed),
		zap.Int("chunks", chunks))
	now := time.Now()
	_ = p.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		_, _ = q.ExecContext(ctx, `UPDATE index_jobs SET status = 'completed', progress = 1.0, processed_files = ?, total_chunks = ?, completed_at = ? WHERE id = ?`, processed, chunks, now, jobID)
		_, _ = q.ExecContext(ctx, `UPDATE sources SET status = 'ready', last_indexed_at = ?, updated_at = ? WHERE id = ?`, now, now, sourceID)
		return nil
	})
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

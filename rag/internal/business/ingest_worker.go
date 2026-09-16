package business

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/cache"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/jobqueue"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/pdfextract"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/docint"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/inspect"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/pipeline"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/retrieve"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/ingestjob"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// IngestWorker processes ingestion jobs asynchronously
type IngestWorker struct {
	jobRepo             *repository.IngestJobRepository
	docRepo             *repository.DocumentRepository
	chunkRepo           *repository.ChunkRepository
	tenantRepo          *repository.TenantRepository
	pipeline            *pipeline.Pipeline
	vectorStore         shared.VectorStore
	llmClient           shared.LLMClient
	inspector           inspect.ContentInspector
	queryCache          cache.QueryCache
	quantizer           retrieve.EmbeddingQuantizer
	binaryIndex         retrieve.BinaryIndex
	pdfExtractor        *pdfextract.Extractor
	logger              *zap.Logger
	semanticThreshold   float64
	cvThreshold         float64
	identWarnThreshold  float64
	identBlockThreshold float64
	queueSubscriber     jobqueue.IngestJobSubscriber
	pollInterval        time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewIngestWorker creates a new ingest worker
func NewIngestWorker(
	jobRepo *repository.IngestJobRepository,
	docRepo *repository.DocumentRepository,
	chunkRepo *repository.ChunkRepository,
	pipeline *pipeline.Pipeline,
	vectorStore shared.VectorStore,
	llmClient shared.LLMClient,
	tenantRepo *repository.TenantRepository,
	inspector inspect.ContentInspector,
	queryCache cache.QueryCache,
	quantizer retrieve.EmbeddingQuantizer,
	binaryIndex retrieve.BinaryIndex,
	pdfExtractor *pdfextract.Extractor,
	queueSubscriber jobqueue.IngestJobSubscriber,
	pollInterval time.Duration,
	logger *zap.Logger,
) *IngestWorker {
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	return &IngestWorker{
		jobRepo:             jobRepo,
		docRepo:             docRepo,
		chunkRepo:           chunkRepo,
		tenantRepo:          tenantRepo,
		pipeline:            pipeline,
		vectorStore:         vectorStore,
		llmClient:           llmClient,
		inspector:           inspector,
		queryCache:          queryCache,
		quantizer:           quantizer,
		binaryIndex:         binaryIndex,
		pdfExtractor:        pdfExtractor,
		logger:              logger.Named("ingest-worker"),
		semanticThreshold:   0.9,
		cvThreshold:         0.85,
		identWarnThreshold:  0.5,
		identBlockThreshold: 0.8,
		queueSubscriber:     queueSubscriber,
		pollInterval:        pollInterval,
	}
}

// Start starts the ingest worker
func (w *IngestWorker) Start(ctx context.Context) error {
	w.logger.Info("starting ingest worker")

	w.ctx, w.cancel = context.WithCancel(ctx)

	if w.queueSubscriber != nil {
		if err := w.queueSubscriber.SubscribeIngestJobs(w.ctx, func(ctx context.Context, jobID xid.ID) error {
			return w.processJobByID(ctx, jobID)
		}); err != nil {
			return err
		}
		w.logger.Info("ingest worker subscribed to async queue")
	}

	w.wg.Add(1)
	go w.processLoop()

	return nil
}

// Stop stops the ingest worker
func (w *IngestWorker) Stop() error {
	w.logger.Info("stopping ingest worker")

	if w.cancel != nil {
		w.cancel()
	}

	w.wg.Wait()

	return nil
}

// SetThresholds configures detection thresholds.
func (w *IngestWorker) SetThresholds(semantic, cv, identWarn, identBlock float64) {
	if semantic > 0 {
		w.semanticThreshold = semantic
	}
	if cv > 0 {
		w.cvThreshold = cv
	}
	if identWarn > 0 {
		w.identWarnThreshold = identWarn
	}
	if identBlock > 0 {
		w.identBlockThreshold = identBlock
	}
}

func int8ToBytes(data []int8) []byte {
	if len(data) == 0 {
		return nil
	}
	out := make([]byte, len(data))
	for i, v := range data {
		out[i] = byte(v)
	}
	return out
}

// Name returns the worker name
func (w *IngestWorker) Name() string {
	return "ingest-worker"
}

// Health checks the worker health
func (w *IngestWorker) Health(ctx context.Context) error {
	return nil
}

// processLoop continuously processes pending jobs
func (w *IngestWorker) processLoop() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			w.logger.Info("process loop stopped")
			return
		case <-ticker.C:
			w.processPendingJobs()
		}
	}
}

// processPendingJobs processes pending jobs
func (w *IngestWorker) processPendingJobs() {
	jobs, err := w.jobRepo.GetPending(w.ctx, 10)
	if err != nil {
		w.logger.Error("failed to get pending jobs", zap.Error(err))
		return
	}

	if len(jobs) == 0 {
		return
	}

	w.logger.Info("processing pending jobs", zap.Int("count", len(jobs)))

	for _, job := range jobs {
		if err := w.processJobByID(w.ctx, job.ID); err != nil {
			w.logger.Error("job processing failed",
				zap.String("jobID", job.ID.String()),
				zap.Error(err))
		}
	}
}

func (w *IngestWorker) processJobByID(ctx context.Context, jobID xid.ID) error {
	job, claimed, err := w.jobRepo.ClaimPending(ctx, jobID)
	if err != nil {
		return err
	}
	if !claimed || job == nil {
		return nil
	}
	return w.processClaimedJob(job)
}

// processClaimedJob processes a single already-claimed job.
func (w *IngestWorker) processClaimedJob(job *ent.IngestJob) error {
	w.logger.Info("processing job", zap.String("jobID", job.ID.String()))

	if job.SourceType == "reindex" {
		if err := w.processReindexJob(job); err != nil {
			_ = w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, err.Error(), 0)
			return err
		}
		return nil
	}

	// Parse source from map
	var source IngestSource
	sourceBytes, err := json.Marshal(job.SourcePayload)
	if err != nil {
		w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, "failed to serialize source payload", 0)
		return shared.ErrInternal("failed to serialize source payload", err)
	}
	if err := json.Unmarshal(sourceBytes, &source); err != nil {
		w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, "failed to parse source", 0)
		return shared.ErrInternal("failed to parse source", err)
	}

	// Get content
	content := source.Content
	contentType := source.ContentType
	contentSize := source.Size
	if content == "" && source.URL != "" {
		w.logger.Info("fetching content from URL", zap.String("url", source.URL))
		fetchedResult, err := w.fetchURL(source.URL)
		if err != nil {
			w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, "failed to fetch URL: "+err.Error(), 0)
			return shared.ErrInternal("failed to fetch URL", err)
		}
		content = fetchedResult.Content
		if contentType == "" {
			contentType = fetchedResult.ContentType
		}
		if contentSize == 0 {
			contentSize = fetchedResult.Size
		}
		w.logger.Info("content fetched successfully", zap.Int("length", len(content)), zap.String("content_type", contentType))
	} else if content != "" && contentSize == 0 {
		// Calculate size from provided content
		contentSize = int64(len(content))
	}

	// PDF extraction step: convert PDF binary to text before further processing
	if isPDFContentType(contentType, content) {
		if w.pdfExtractor == nil {
			w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, "PDF extraction not enabled on this server", 0)
			return shared.ErrInternal("PDF extraction not enabled", nil)
		}
		w.logger.Info("extracting text from PDF",
			zap.String("jobID", job.ID.String()),
			zap.String("title", source.Title),
			zap.Int("content_length", len(content)))

		pdfBytes, err := decodePDFContent(content)
		if err != nil {
			msg := "failed to decode PDF content: " + err.Error()
			w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, msg, 0)
			return shared.ErrInternal(msg, err)
		}

		extracted, err := w.pdfExtractor.ExtractText(w.ctx, pdfBytes, pdfextract.Options{})
		if err != nil {
			msg := "PDF text extraction failed: " + err.Error()
			w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, msg, 0)
			return shared.ErrInternal(msg, err)
		}

		w.logger.Info("PDF text extracted successfully",
			zap.String("jobID", job.ID.String()),
			zap.Int("original_size", len(content)),
			zap.Int("extracted_length", len(extracted)))

		content = extracted
		contentType = "text/plain"
	}

	// Normalize content for duplicate detection
	normalized := docint.NormalizeText(content)
	docType := source.DocType
	if docType == "" {
		docType = "generic"
	}
	// enrich metadata with doc_type
	if source.Metadata == nil {
		source.Metadata = make(map[string]interface{})
	}
	source.Metadata["doc_type"] = docType

	// Duplicate detection
	var normalizedHash *string
	if normalized != "" {
		h := sha256.Sum256([]byte(normalized))
		hx := hex.EncodeToString(h[:])
		normalizedHash = &hx
	}
	if source.DuplicateStrategy == "" {
		source.DuplicateStrategy = "auto"
	}
	selectedStrategy := resolveDuplicateStrategy(source.DuplicateStrategy, docType, normalized)
	semanticThreshold := 0.9
	if selectedStrategy == "cv" {
		semanticThreshold = w.cvThreshold
	} else if w.semanticThreshold > 0 {
		semanticThreshold = w.semanticThreshold
	}

	if source.DetectDuplicates && normalized != "" && selectedStrategy != "none" {
		// hash check
		if selectedStrategy == "hash" || selectedStrategy == "cv" {
			if normalizedHash != nil {
				if existing, err := w.docRepo.FindByHash(w.ctx, job.TenantID, *normalizedHash); err == nil && existing != nil {
					msg := "duplicate of " + existing.ID.String() + " (similarity 100%)"
					w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, msg, 100)
					return shared.ErrConflict(msg)
				}
			}
		}
		// semantic check
		if selectedStrategy == "semantic" || selectedStrategy == "cv" {
			w.logger.Debug("generating embedding for duplicate check",
				zap.String("jobID", job.ID.String()),
				zap.String("title", source.Title),
				zap.Int("text_length", len(normalized)),
				zap.String("text_preview", previewText(normalized, 200)),
				zap.String("doc_type", docType))
			embed, err := w.llmClient.GenerateEmbedding(w.ctx, normalized)
			if err != nil {
				w.logger.Warn("failed to generate embedding for duplicate check",
					zap.String("jobID", job.ID.String()),
					zap.String("title", source.Title),
					zap.Error(err))
			} else {
				results, err := w.vectorStore.SearchDocEmbedding(w.ctx, embed, 1, job.TenantID)
				if err == nil && len(results) > 0 && results[0].Score >= semanticThreshold {
					percent := fmt.Sprintf("%.1f%%", results[0].Score*100)
					msg := "semantic duplicate of " + results[0].DocumentID.String() + " (similarity " + percent + ")"
					w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, msg, 100)
					return shared.ErrConflict(msg)
				}
			}
		}
	}

	// Content inspection
	var inspectionFindings map[string]interface{}
	hasFindings := false
	if source.DetectContent {
		profile := source.ContentDetectionProfile
		if profile == "" {
			profile = "pii_basic"
		}
		rules := inspect.DefaultProfile(profile)
		if len(source.CustomDetectionRules) > 0 {
			rules = append(rules, source.CustomDetectionRules...)
		}
		if len(rules) > 0 {
			result, _ := w.inspector.Inspect(w.ctx, job.TenantID.String(), job.TenantID.String(), normalized, source.Metadata, rules)
			if result.HasFindings {
				hasFindings = true
				inspectionFindings = map[string]interface{}{
					"has_findings": result.HasFindings,
					"findings":     result.Findings,
				}
				block := false
				for _, f := range result.Findings {
					if w.shouldBlockOnFinding(profile, f) {
						block = true
					}
				}
				_ = w.jobRepo.UpdateFindings(w.ctx, job.ID, profile, hasFindings, inspectionFindings)
				if block {
					msg := "Document blocked due to content policy"
					w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, msg, 100)
					return shared.ErrForbidden(msg)
				}
			}
		}
	}

	// Process document through pipeline (pass metadata from source)
	chunks, err := w.pipeline.ProcessDocument(w.ctx, content, source.Title, 1000, source.Metadata)
	if err != nil {
		w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, err.Error(), 0)
		return err
	}

	// Update progress
	w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusRunning, "", 30)

	// Generate embeddings and stage chunks
	type stagedChunk struct {
		Text            string
		Metadata        map[string]interface{}
		Embedding       []float64
		EmbeddingInt8   []int8
		EmbeddingBinary []byte
	}
	preparedChunks := make([]stagedChunk, 0, len(chunks))
	for i, chunk := range chunks {
		w.logger.Debug("generating embedding for chunk",
			zap.String("jobID", job.ID.String()),
			zap.String("title", source.Title),
			zap.Int("chunk_index", i),
			zap.Int("text_length", len(chunk.Text)),
			zap.String("text_preview", previewText(chunk.Text, 200)))
		// Generate embedding
		embedding, err := w.llmClient.GenerateEmbedding(w.ctx, chunk.Text)
		if err != nil {
			w.logger.Warn("failed to generate embedding",
				zap.String("jobID", job.ID.String()),
				zap.String("title", source.Title),
				zap.Int("chunk_index", i),
				zap.Error(err))
			msg := fmt.Sprintf("failed to generate embedding for chunk %d: %v", i, err)
			statusErr := w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, msg, 0)
			return errors.Join(shared.ErrInternal(msg, err), statusErr)
		}

		var embInt8 []int8
		var embBin []byte
		if w.quantizer != nil {
			if q8, qErr := w.quantizer.ToInt8(embedding); qErr == nil {
				embInt8 = q8
			} else {
				w.logger.Warn("failed to quantize embedding to int8", zap.Error(qErr))
			}
			if qb, qErr := w.quantizer.ToBinary(embedding); qErr == nil {
				embBin = qb
			} else {
				w.logger.Warn("failed to quantize embedding to binary", zap.Error(qErr))
			}
		}

		// Sanitize text to ensure valid UTF-8 (PDF parsing may produce invalid sequences)
		sanitizedText := shared.SanitizeUTF8Clean(chunk.Text)
		preparedChunks = append(preparedChunks, stagedChunk{
			Text:            sanitizedText,
			Metadata:        chunk.Metadata,
			Embedding:       embedding,
			EmbeddingInt8:   embInt8,
			EmbeddingBinary: embBin,
		})
	}

	// Update progress
	w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusRunning, "", 50)

	// Check if we have any valid chunks
	if len(preparedChunks) == 0 {
		w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, "no chunks could be processed (embedding generation failed)", 0)
		return shared.ErrInternal("no chunks could be processed", nil)
	}

	// Create document only after we have valid chunks
	doc := &ent.Document{
		ID:             xid.New(),
		TenantID:       job.TenantID,
		Title:          source.Title,
		SourceURL:      source.URL,
		Metadata:       source.Metadata,
		NormalizedHash: normalizedHash,
		DocType:        docType,
		HasPii:         hasFindings,
		PiiSummary:     inspectionFindings,
	}
	if contentType != "" {
		doc.ContentType = &contentType
	}
	if contentSize > 0 {
		doc.Size = &contentSize
	}

	created, err := w.docRepo.Create(w.ctx, doc)
	if err != nil {
		w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, err.Error(), 0)
		return err
	}

	// Build chunk entities and vector payloads now that we have a document ID
	entChunks := make([]*ent.Chunk, 0, len(preparedChunks))
	validChunks := make([]shared.Chunk, 0, len(preparedChunks))
	for _, chunk := range preparedChunks {
		chunkID := xid.New()
		entChunks = append(entChunks, &ent.Chunk{
			ID:              chunkID,
			DocumentID:      created.ID,
			Text:            chunk.Text,
			Metadata:        chunk.Metadata,
			EmbeddingModel:  "text-embedding-ada-002",
			VectorID:        "", // Will be set after vector store upsert
			EmbeddingInt8:   int8ToBytes(chunk.EmbeddingInt8),
			EmbeddingBinary: chunk.EmbeddingBinary,
		})

		validChunks = append(validChunks, shared.Chunk{
			ID:         chunkID.String(),
			DocumentID: created.ID,
			TenantID:   job.TenantID,
			Text:       chunk.Text,
			Metadata:   chunk.Metadata,
			Embedding:  chunk.Embedding,
		})
	}

	// Update progress
	w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusRunning, "", 70)

	// Store in database
	if err := w.chunkRepo.CreateBatch(w.ctx, entChunks); err != nil {
		w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusFailed, err.Error(), 0)
		_ = w.docRepo.Delete(w.ctx, created.ID)
		return err
	}

	// Update progress
	w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusRunning, "", 80)

	// Store in vector store
	if err := w.vectorStore.Upsert(w.ctx, validChunks); err != nil {
		return w.failPersistedIngestion(job.ID, created.ID, validChunks, fmt.Errorf("failed to upsert to vector store: %w", err))
	}

	// Update progress
	w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusRunning, "", 90)

	// Update vector IDs in database
	for _, chunk := range validChunks {
		if chunk.ID != "" {
			chunkID, _ := xid.FromString(chunk.ID)
			if err := w.chunkRepo.UpdateVectorID(w.ctx, chunkID, chunk.ID); err != nil {
				return w.failPersistedIngestion(job.ID, created.ID, validChunks, fmt.Errorf("failed to persist vector ID: %w", err))
			}
		}
	}

	// Publish auxiliary indexes only after all chunks have been persisted.
	for _, chunk := range entChunks {
		if w.binaryIndex != nil && len(chunk.EmbeddingBinary) > 0 {
			w.binaryIndex.Add(chunk.ID.String(), chunk.EmbeddingBinary, job.TenantID.String())
		}
	}

	// Upsert doc-level embedding for semantic duplicate detection (only after chunks are stored)
	if normalized != "" {
		w.logger.Debug("generating document embedding",
			zap.String("jobID", job.ID.String()),
			zap.String("title", source.Title),
			zap.Int("text_length", len(normalized)),
			zap.String("text_preview", previewText(normalized, 200)),
			zap.String("doc_type", docType))
		if emb, err := w.llmClient.GenerateEmbedding(w.ctx, normalized); err == nil {
			_ = w.vectorStore.UpsertDocEmbedding(w.ctx, shared.Chunk{
				DocumentID: created.ID,
				TenantID:   job.TenantID,
				Embedding:  emb,
				Metadata: map[string]interface{}{
					"doc_type": docType,
				},
			})
		}
	}

	// Mark as completed only after the mandatory writes succeeded.
	if err := w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusCompleted, "", 100); err != nil {
		return err
	}

	// Invalidate cache for the tenant (new documents may affect search results)
	if w.queryCache != nil && w.queryCache.IsEnabled() {
		if err := w.queryCache.InvalidateTenant(w.ctx, job.TenantID); err != nil {
			w.logger.Warn("failed to invalidate cache for tenant", zap.Error(err))
		} else {
			w.logger.Debug("cache invalidated for tenant", zap.String("tenant_id", job.TenantID.String()))
		}
	}

	w.logger.Info("job completed",
		zap.String("jobID", job.ID.String()),
		zap.String("documentID", created.ID.String()),
		zap.Int("chunks", len(chunks)))

	return nil
}

// failPersistedIngestion removes this job's incomplete document so a retry is not
// skipped as a duplicate. Remote cleanup is best effort; failures remain visible
// in the job message and returned error, never as a completed ingestion.
func (w *IngestWorker) failPersistedIngestion(jobID, documentID xid.ID, chunks []shared.Chunk, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(w.ctx), 10*time.Second)
	defer cancel()
	ids := make([]string, len(chunks))
	for i, chunk := range chunks {
		ids[i] = chunk.ID
	}
	vectorCtx, stopVectorCleanup := context.WithTimeout(ctx, 3*time.Second)
	defer stopVectorCleanup()
	if err := w.vectorStore.Delete(vectorCtx, ids); err != nil {
		cause = errors.Join(cause, fmt.Errorf("vector cleanup failed: %w", err))
	}
	if err := w.chunkRepo.DeleteByDocument(ctx, documentID); err != nil {
		cause = errors.Join(cause, fmt.Errorf("chunk cleanup failed: %w", err))
	} else if err := w.docRepo.Delete(ctx, documentID); err != nil {
		cause = errors.Join(cause, fmt.Errorf("document cleanup failed: %w", err))
	}
	statusErr := w.jobRepo.UpdateStatus(ctx, jobID, ingestjob.StatusFailed, cause.Error(), 0)
	return errors.Join(cause, statusErr)
}

type reindexPayload struct {
	BatchSize int    `json:"batch_size"`
	Force     bool   `json:"force"`
	Mode      string `json:"mode"`
}

func (w *IngestWorker) processReindexJob(job *ent.IngestJob) error {
	quantizer := w.quantizer
	if quantizer == nil {
		quantizer = retrieve.NewLinearQuantizer()
	}

	payload := reindexPayload{BatchSize: 100}
	if job.SourcePayload != nil {
		b, _ := json.Marshal(job.SourcePayload)
		_ = json.Unmarshal(b, &payload)
	}
	if payload.BatchSize <= 0 {
		payload.BatchSize = 100
	}

	total, err := w.chunkRepo.CountByTenant(w.ctx, job.TenantID)
	if err != nil {
		return err
	}
	if total == 0 {
		_ = w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusCompleted, "no chunks to reindex", 100)
		return nil
	}

	w.logger.Info("reindex job started",
		zap.String("jobID", job.ID.String()),
		zap.String("tenantID", job.TenantID.String()),
		zap.Int("total_chunks", total),
		zap.Int("batch_size", payload.BatchSize),
		zap.Bool("force", payload.Force))

	scanned := 0
	updated := 0
	skipped := 0
	failed := 0

	for offset := 0; ; offset += payload.BatchSize {
		chunks, err := w.chunkRepo.ListByTenant(w.ctx, job.TenantID, offset, payload.BatchSize)
		if err != nil {
			return err
		}
		if len(chunks) == 0 {
			break
		}

		for _, ch := range chunks {
			scanned++
			if !payload.Force && len(ch.EmbeddingInt8) > 0 && len(ch.EmbeddingBinary) > 0 {
				skipped++
				continue
			}

			embedding, err := w.llmClient.GenerateEmbedding(w.ctx, ch.Text)
			if err != nil {
				failed++
				w.logger.Warn("failed to generate embedding for reindex",
					zap.String("chunk_id", ch.ID.String()),
					zap.Error(err))
				continue
			}

			int8Emb, err := quantizer.ToInt8(embedding)
			if err != nil {
				failed++
				w.logger.Warn("failed to quantize embedding to int8",
					zap.String("chunk_id", ch.ID.String()),
					zap.Error(err))
				continue
			}
			binEmb, err := quantizer.ToBinary(embedding)
			if err != nil {
				failed++
				w.logger.Warn("failed to quantize embedding to binary",
					zap.String("chunk_id", ch.ID.String()),
					zap.Error(err))
				continue
			}

			if err := w.chunkRepo.UpdateEmbeddings(w.ctx, ch.ID, int8ToBytes(int8Emb), binEmb); err != nil {
				failed++
				w.logger.Warn("failed to update chunk embeddings",
					zap.String("chunk_id", ch.ID.String()),
					zap.Error(err))
				continue
			}
			if w.binaryIndex != nil {
				w.binaryIndex.Add(ch.ID.String(), binEmb, job.TenantID.String())
			}
			updated++
		}

		progress := int(float64(scanned) / float64(total) * 100)
		if progress > 99 {
			progress = 99
		}
		_ = w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusRunning, "reindexing embeddings", progress)
	}

	msg := fmt.Sprintf("reindex completed: updated=%d skipped=%d failed=%d", updated, skipped, failed)
	_ = w.jobRepo.UpdateStatus(w.ctx, job.ID, ingestjob.StatusCompleted, msg, 100)
	return nil
}

func previewText(input string, limit int) string {
	if limit <= 0 || input == "" {
		return ""
	}
	trimmed := strings.TrimSpace(shared.SanitizeUTF8Clean(input))
	trimmed = strings.ReplaceAll(trimmed, "\n", " ")
	trimmed = strings.ReplaceAll(trimmed, "\r", " ")
	trimmed = strings.ReplaceAll(trimmed, "\t", " ")
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	return string(runes[:limit]) + "..."
}

// fetchResult holds the result of fetching a URL
type fetchResult struct {
	Content     string
	ContentType string
	Size        int64
}

// fetchURL fetches content from a URL
func (w *IngestWorker) fetchURL(url string) (*fetchResult, error) {
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Create request with context
	req, err := http.NewRequestWithContext(w.ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set user agent
	req.Header.Set("User-Agent", "RAG-Server/1.0")

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Get content type from response
	contentType := resp.Header.Get("Content-Type")
	// Clean content type (remove charset and other params)
	if idx := strings.Index(contentType, ";"); idx != -1 {
		contentType = strings.TrimSpace(contentType[:idx])
	}

	// Read body with size limit (10MB max)
	const maxSize = 10 * 1024 * 1024
	limitedReader := io.LimitReader(resp.Body, maxSize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return &fetchResult{
		Content:     string(body),
		ContentType: contentType,
		Size:        int64(len(body)),
	}, nil
}

// resolveDuplicateStrategy selects a concrete strategy from auto/cv/hash/semantic.
func resolveDuplicateStrategy(strategy string, docType string, normalized string) string {
	s := strings.ToLower(strategy)
	dt := strings.ToLower(docType)
	if s == "none" {
		return "none"
	}
	if s != "auto" {
		return s
	}
	// auto mode
	if dt != "" && dt != "generic" {
		if dt == "cv" {
			return "cv"
		}
		return "semantic"
	}
	if isCVLike(normalized) {
		return "cv"
	}
	if len(normalized) < 500 {
		return "hash"
	}
	return "semantic"
}

func (w *IngestWorker) shouldBlockOnFinding(profile string, finding inspect.DetectionFinding) bool {
	if strings.EqualFold(finding.Severity, "block") {
		return true
	}
	// cv_identifiability is used to mark CVs with native identifiability/PII
	// findings, not to reject ingestion based on a heuristic score alone.
	if profile == "cv_identifiability" {
		return false
	}
	return finding.IdentificationRiskScore > w.identBlockThreshold
}

// isPDFContentType checks if the content is a PDF based on content type or raw content inspection.
func isPDFContentType(contentType string, content string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if ct == "application/pdf" {
		return true
	}
	// Check if raw content starts with PDF magic bytes
	if len(content) >= 4 && content[:4] == "%PDF" {
		return true
	}
	// Check if base64-encoded content starts with PDF magic (JVBER = base64 of %PDF)
	trimmed := strings.TrimSpace(content)
	if len(trimmed) >= 5 && trimmed[:5] == "JVBER" {
		return true
	}
	return false
}

// decodePDFContent attempts to decode the content as base64, falling back to raw bytes.
func decodePDFContent(content string) ([]byte, error) {
	raw := []byte(content)
	// If it starts with raw PDF magic bytes, return as-is
	if len(raw) >= 4 && raw[0] == '%' && raw[1] == 'P' && raw[2] == 'D' && raw[3] == 'F' {
		return raw, nil
	}
	// Try base64 decoding
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
	if err != nil {
		// Try URL-safe base64
		decoded, err = base64.URLEncoding.DecodeString(strings.TrimSpace(content))
		if err != nil {
			// Try raw base64 (no padding)
			decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(content))
			if err != nil {
				return nil, fmt.Errorf("content is neither raw PDF nor valid base64: %w", err)
			}
		}
	}
	return decoded, nil
}

func isCVLike(text string) bool {
	t := strings.ToLower(text)
	cvKeywords := []string{"experience", "experiences", "skills", "compétences", "education", "formation", "professional summary", "linkedin", "curriculum vitae"}
	matches := 0
	for _, kw := range cvKeywords {
		if strings.Contains(t, kw) {
			matches++
		}
	}
	return matches >= 2
}

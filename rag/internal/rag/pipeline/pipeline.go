package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/docint"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/experience"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/queryint"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/reason"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/retrieve"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"go.uber.org/zap"
)

// Pipeline orchestrates the RAG pipeline
type Pipeline struct {
	docAnalyzer   *docint.Analyzer
	queryAnalyzer *queryint.Analyzer
	retriever     *retrieve.Retriever
	answerer      *reason.Answerer
	expLogger     *experience.Logger
	logger        *zap.Logger
}

// NewPipeline creates a new RAG pipeline
func NewPipeline(
	docAnalyzer *docint.Analyzer,
	queryAnalyzer *queryint.Analyzer,
	retriever *retrieve.Retriever,
	answerer *reason.Answerer,
	expLogger *experience.Logger,
	logger *zap.Logger,
) *Pipeline {
	return &Pipeline{
		docAnalyzer:   docAnalyzer,
		queryAnalyzer: queryAnalyzer,
		retriever:     retriever,
		answerer:      answerer,
		expLogger:     expLogger,
		logger:        logger.Named("pipeline"),
	}
}

// ProcessDocument processes a document through the pipeline
func (p *Pipeline) ProcessDocument(ctx context.Context, text, title string, maxChunkSize int, docMetadata map[string]interface{}) ([]shared.Chunk, error) {
	p.logger.Debug("processing document", zap.String("title", title))

	// Layer 0: Document Intelligence
	analyzed, err := p.docAnalyzer.Analyze(ctx, text, title)
	if err != nil {
		return nil, shared.ErrInternal("document analysis failed", err)
	}

	// Create chunks from sections
	chunks := p.docAnalyzer.ChunkSections(analyzed.Sections, maxChunkSize)

	// Merge document metadata into each chunk's metadata
	for i := range chunks {
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = make(map[string]interface{})
		}
		// Add document title
		chunks[i].Metadata["document_title"] = title
		// Add doc_type if present
		if docMetadata != nil {
			if dt, ok := docMetadata["doc_type"]; ok {
				chunks[i].Metadata["doc_type"] = dt
			}
		}
		// Merge document metadata
		if docMetadata != nil {
			for k, v := range docMetadata {
				chunks[i].Metadata[k] = v
			}
		}
	}

	p.logger.Info("document processed",
		zap.String("title", title),
		zap.Int("sections", len(analyzed.Sections)),
		zap.Int("chunks", len(chunks)))

	return chunks, nil
}

// Query processes a query through the pipeline
func (p *Pipeline) Query(ctx context.Context, query shared.Query) (*shared.Answer, error) {
	start := time.Now()

	p.logger.Debug("processing query",
		zap.String("query", query.Text),
		zap.String("tenantID", query.TenantID.String()))

	// Layer 1: Query Intelligence
	analysis, err := p.queryAnalyzer.Analyze(ctx, query.Text)
	if err != nil {
		return nil, shared.ErrInternal("query analysis failed", err)
	}

	// Layer 2: Retrieval (with light query expansion)
	expandedQuery := buildExpandedQuery(query.Text, analysis)

	// Adaptive topK: when metadata filters are active, request more candidates
	// because native Qdrant filtering may reduce results, and we also apply
	// post-filtering as a safety net for data not yet re-ingested.
	retrievalTopK := 5
	hasFilters := len(query.MetadataFilters) > 0
	if hasFilters {
		retrievalTopK = 20
	}

	chunks, err := p.retriever.RetrieveWithReranking(ctx, expandedQuery, analysis, retrievalTopK, query.TenantID, query.DeploymentID, query.MetadataFilters)
	if err != nil {
		return nil, shared.ErrInternal("retrieval failed", err)
	}

	// Phase 1: Post-filter chunks by metadata as a safety net.
	// This handles data that was ingested before the metadata promotion (Phase 2).
	// Metadata may be stored in chunk.Metadata as JSON or as top-level keys.
	if hasFilters && len(chunks) > 0 {
		chunks = postFilterChunks(chunks, query.MetadataFilters, p.logger)
	}

	// Cap to desired result count after post-filtering
	if len(chunks) > 5 {
		chunks = chunks[:5]
	}

	// Layer 3: Reasoning
	answer, err := p.answerer.GenerateAnswer(ctx, query.Text, chunks, query.MaxTokens)
	if err != nil {
		return nil, shared.ErrInternal("answer generation failed", err)
	}

	// Layer 4: Experience
	duration := time.Since(start)
	interaction := &experience.Interaction{
		Query:        query.Text,
		Answer:       answer.Text,
		Chunks:       chunks,
		TenantID:     query.TenantID,
		DeploymentID: query.DeploymentID,
		Timestamp:    start,
		Duration:     duration,
	}

	if err := p.expLogger.LogInteraction(ctx, interaction); err != nil {
		p.logger.Warn("failed to log interaction", zap.Error(err))
	}

	p.logger.Info("query processed",
		zap.String("query", query.Text),
		zap.Int("chunks_retrieved", len(chunks)),
		zap.Duration("duration", duration))

	return answer, nil
}

// buildExpandedQuery appends extracted keywords to the user query for broader recall.
func buildExpandedQuery(original string, analysis *shared.QueryAnalysis) string {
	if analysis == nil || len(analysis.Keywords) == 0 {
		return original
	}
	kw := strings.Join(analysis.Keywords, " ")
	return strings.TrimSpace(original + " " + kw)
}

// postFilterChunks filters retrieved chunks based on metadata filters.
// It checks both the parsed Metadata map and the raw metadata JSON string
// for matching values. This ensures filtering works both before and after
// re-ingestion with promoted payload fields.
func postFilterChunks(chunks []shared.Chunk, filters map[string]string, logger *zap.Logger) []shared.Chunk {
	if len(filters) == 0 {
		return chunks
	}

	// Build alias map: filter key -> list of metadata keys to check
	// Supports both snake_case and camelCase naming conventions used in recipes
	aliases := map[string][]string{
		"offre_id":        {"offre_id", "offreId"},
		"offreId":         {"offre_id", "offreId"},
		"consultation_id": {"consultation_id", "consultationId"},
		"consultationId":  {"consultation_id", "consultationId"},
		"trigramme":       {"trigramme"},
		"doc_type":        {"doc_type"},
		"file_name":       {"file_name"},
	}

	beforeCount := len(chunks)
	filtered := make([]shared.Chunk, 0, len(chunks))

	for _, chunk := range chunks {
		if chunkMatchesFilters(chunk, filters, aliases) {
			filtered = append(filtered, chunk)
		}
	}

	if logger != nil && len(filtered) < beforeCount {
		logger.Debug("post-filtered chunks by metadata",
			zap.Int("before", beforeCount),
			zap.Int("after", len(filtered)),
			zap.Any("filters", filters))
	}

	return filtered
}

// chunkMatchesFilters checks if a chunk matches ALL provided metadata filters.
func chunkMatchesFilters(chunk shared.Chunk, filters map[string]string, aliases map[string][]string) bool {
	for filterKey, filterVal := range filters {
		keysToCheck := aliases[filterKey]
		if len(keysToCheck) == 0 {
			keysToCheck = []string{filterKey}
		}

		matched := false

		// Check in parsed Metadata map
		if chunk.Metadata != nil {
			for _, key := range keysToCheck {
				if val, ok := chunk.Metadata[key]; ok {
					strVal := metadataValueToString(val)
					if strVal == filterVal {
						matched = true
						break
					}
				}
			}
		}

		// If not matched yet, try parsing the raw "metadata" field from chunk text
		// (for chunks where metadata is a JSON string embedded in Metadata["metadata"])
		if !matched && chunk.Metadata != nil {
			if rawMeta, ok := chunk.Metadata["metadata"]; ok {
				if rawStr, isStr := rawMeta.(string); isStr {
					var parsed map[string]interface{}
					if json.Unmarshal([]byte(rawStr), &parsed) == nil {
						for _, key := range keysToCheck {
							if val, ok := parsed[key]; ok {
								strVal := metadataValueToString(val)
								if strVal == filterVal {
									matched = true
									break
								}
							}
						}
					}
				}
			}
		}

		if !matched {
			return false
		}
	}
	return true
}

// metadataValueToString converts a metadata value to string for comparison.
func metadataValueToString(val interface{}) string {
	switch v := val.(type) {
	case string:
		return v
	case float64:
		// JSON numbers are float64; represent as integer if whole number
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

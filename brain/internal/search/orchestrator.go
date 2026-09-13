package search

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer/embedder"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	"go.uber.org/zap"
)

var (
	// Patterns for auto mode detection
	camelCasePattern = regexp.MustCompile(`[a-z][A-Z]`)
	snakeCasePattern = regexp.MustCompile(`[a-z]_[a-z]`)
	dotNotation      = regexp.MustCompile(`\w+\.\w+\.\w+`)
	errorCodePattern = regexp.MustCompile(`(?i)(ERR_|ERROR_|E\d{4}|status\s*=?\s*\d{3})`)

	naturalLangWords = []string{"comment", "pourquoi", "explain", "how", "what", "why", "describe", "quoi", "quel"}
	structuralWords  = []string{"appelle", "calls", "dépend", "depends", "import", "extends", "implements", "impact"}
)

// Orchestrator selects the best search mode and executes the search.
type Orchestrator struct {
	storage  storage.Manager
	embedder embedder.Provider
	cfg      *config.Config
	logger   *zap.Logger
}

// NewOrchestrator creates a new search orchestrator.
func NewOrchestrator(storage storage.Manager, embedder embedder.Provider, cfg *config.Config, logger *zap.Logger) *Orchestrator {
	return &Orchestrator{
		storage:  storage,
		embedder: embedder,
		cfg:      cfg,
		logger:   logger.Named("search"),
	}
}

// Search performs a search with the given options.
func (o *Orchestrator) Search(ctx context.Context, tenantID string, query string, opts shared.SearchOpts) (*shared.SearchResponse, error) {
	start := time.Now()

	mode := opts.Mode
	reason := "explicitly requested"

	if mode == "" || mode == "auto" {
		mode, reason = o.detectMode(query)
	}

	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = o.cfg.Search.DefaultMaxResults
	}
	if maxResults > o.cfg.Search.MaxResultsLimit {
		maxResults = o.cfg.Search.MaxResultsLimit
	}

	alpha := opts.Alpha
	if alpha <= 0 {
		alpha = o.cfg.Search.HybridDefaultAlpha
	}

	var results []shared.SearchResult
	var meta shared.SearchMetadata
	var err error

	switch mode {
	case "bm25":
		results, meta, err = o.searchBM25(ctx, tenantID, query, maxResults, opts)
	case "vector":
		results, meta, err = o.searchVector(ctx, tenantID, query, maxResults, opts)
	case "hybrid":
		results, meta, err = o.searchHybrid(ctx, tenantID, query, maxResults, alpha, opts)
	default:
		// Default to hybrid
		mode = "hybrid"
		reason = "default fallback"
		results, meta, err = o.searchHybrid(ctx, tenantID, query, maxResults, alpha, opts)
	}

	if err != nil {
		return nil, err
	}

	elapsed := time.Since(start)
	meta.ModeSelected = mode
	meta.ModeReason = reason
	meta.TotalTimeMs = elapsed.Milliseconds()

	// Assign ranks
	for i := range results {
		results[i].Rank = i + 1
	}

	return &shared.SearchResponse{
		Mode:           mode,
		Query:          query,
		TotalResults:   len(results),
		Results:        results,
		SearchMetadata: meta,
	}, nil
}

// detectMode analyzes the query and selects the best search mode.
func (o *Orchestrator) detectMode(query string) (string, string) {
	q := strings.ToLower(query)

	// Check for exact identifiers (BM25)
	if camelCasePattern.MatchString(query) || snakeCasePattern.MatchString(query) {
		return "bm25", "exact identifier detected (camelCase/snake_case)"
	}
	if dotNotation.MatchString(query) {
		return "bm25", "dot notation identifier detected"
	}
	if errorCodePattern.MatchString(query) {
		return "bm25", "error code or constant detected"
	}

	// Check for structural queries (future: graph)
	for _, word := range structuralWords {
		if strings.Contains(q, word) {
			// Phase 1: fallback to hybrid for structural queries (graph not yet available)
			return "hybrid", fmt.Sprintf("structural query detected ('%s'), using hybrid (graph not yet available)", word)
		}
	}

	// Check for natural language queries (vector)
	for _, word := range naturalLangWords {
		if strings.Contains(q, word) {
			return "vector", fmt.Sprintf("natural language query detected ('%s')", word)
		}
	}

	// Check if query has multiple words (likely conceptual → hybrid)
	words := strings.Fields(query)
	if len(words) >= 3 {
		return "hybrid", "multi-word query detected, using hybrid"
	}

	// Short query, likely a keyword
	return "bm25", "short query, defaulting to keyword search"
}

// searchBM25 performs a BM25-only search.
func (o *Orchestrator) searchBM25(ctx context.Context, tenantID, query string, maxResults int, opts shared.SearchOpts) ([]shared.SearchResult, shared.SearchMetadata, error) {
	start := time.Now()

	db, err := o.storage.OpenTenant(ctx, tenantID)
	if err != nil {
		return nil, shared.SearchMetadata{}, fmt.Errorf("failed to open tenant: %w", err)
	}

	qr, err := db.QueryContext(ctx, `
		SELECT c.id, c.document_id, c.content, c.chunk_type, c.symbol_name, c.start_line, c.end_line,
			   d.id, d.file_path, d.title, d.doc_type, d.language, d.source_id
		FROM chunks c
		JOIN documents d ON c.document_id = d.id
		WHERE c.tenant_id = ? AND c.bm25_content LIKE '%' || ? || '%'
		LIMIT ?
	`, tenantID, query, maxResults)
	if err != nil {
		return nil, shared.SearchMetadata{}, fmt.Errorf("BM25 search failed: %w", err)
	}

	var results []shared.SearchResult
	score := 1.0
	for _, row := range qr.Rows {
		var r shared.SearchResult
		r.Chunk.ID = toString(row["id"])
		r.Chunk.DocumentID = toString(row["document_id"])
		r.Chunk.Content = toString(row["content"])
		r.Chunk.ChunkType = toString(row["chunk_type"])
		r.Chunk.SymbolName = toString(row["symbol_name"])
		r.Chunk.StartLine = toInt(row["start_line"])
		r.Chunk.EndLine = toInt(row["end_line"])
		r.Document.ID = toString(row[qr.Columns[7]])
		r.Document.FilePath = toString(row["file_path"])
		r.Document.Title = toString(row["title"])
		r.Document.DocType = toString(row["doc_type"])
		r.Document.Language = toString(row["language"])
		r.Document.SourceID = toString(row["source_id"])
		r.Score = score
		score -= 0.01
		r.ChunkID = r.Chunk.ID
		results = append(results, r)
	}

	meta := shared.SearchMetadata{
		BM25TimeMs: time.Since(start).Milliseconds(),
	}

	return results, meta, nil
}

// searchVector performs a vector-only search.
func (o *Orchestrator) searchVector(ctx context.Context, tenantID, query string, maxResults int, opts shared.SearchOpts) ([]shared.SearchResult, shared.SearchMetadata, error) {
	start := time.Now()

	// Embed the query
	if o.embedder == nil {
		return nil, shared.SearchMetadata{}, fmt.Errorf("embedding provider not configured")
	}

	embeddings, err := o.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, shared.SearchMetadata{}, fmt.Errorf("failed to embed query: %w", err)
	}
	if len(embeddings) == 0 {
		return nil, shared.SearchMetadata{}, fmt.Errorf("empty embedding returned")
	}

	queryEmbedding := embeddings[0]

	db, err := o.storage.OpenTenant(ctx, tenantID)
	if err != nil {
		return nil, shared.SearchMetadata{}, fmt.Errorf("failed to open tenant: %w", err)
	}

	// Build embedding array literal
	embStr := floatsToArrayLiteral(queryEmbedding)

	sqlQuery := fmt.Sprintf(`
		SELECT c.id, c.document_id, c.content, c.chunk_type, c.symbol_name, c.start_line, c.end_line,
			   d.id, d.file_path, d.title, d.doc_type, d.language, d.source_id,
			   array_cosine_similarity(c.embedding, %s::FLOAT[]) AS score
		FROM chunks c
		JOIN documents d ON c.document_id = d.id
		WHERE c.tenant_id = ? AND c.embedding IS NOT NULL
		ORDER BY score DESC
		LIMIT ?
	`, embStr)

	qr, err := db.QueryContext(ctx, sqlQuery, tenantID, maxResults)
	if err != nil {
		return nil, shared.SearchMetadata{}, fmt.Errorf("vector search failed: %w", err)
	}

	var results []shared.SearchResult
	for _, row := range qr.Rows {
		var r shared.SearchResult
		r.Chunk.ID = toString(row["id"])
		r.Chunk.DocumentID = toString(row["document_id"])
		r.Chunk.Content = toString(row["content"])
		r.Chunk.ChunkType = toString(row["chunk_type"])
		r.Chunk.SymbolName = toString(row["symbol_name"])
		r.Chunk.StartLine = toInt(row["start_line"])
		r.Chunk.EndLine = toInt(row["end_line"])
		r.Document.ID = toString(row[qr.Columns[7]])
		r.Document.FilePath = toString(row["file_path"])
		r.Document.Title = toString(row["title"])
		r.Document.DocType = toString(row["doc_type"])
		r.Document.Language = toString(row["language"])
		r.Document.SourceID = toString(row["source_id"])
		r.Score = toFloat64(row["score"])
		r.ChunkID = r.Chunk.ID
		results = append(results, r)
	}

	meta := shared.SearchMetadata{
		VectorTimeMs: time.Since(start).Milliseconds(),
	}

	return results, meta, nil
}

// searchHybrid performs a combined BM25 + vector search.
func (o *Orchestrator) searchHybrid(ctx context.Context, tenantID, query string, maxResults int, alpha float64, opts shared.SearchOpts) ([]shared.SearchResult, shared.SearchMetadata, error) {
	// Run BM25 and vector in parallel
	type searchResult struct {
		results []shared.SearchResult
		meta    shared.SearchMetadata
		err     error
	}

	bm25Ch := make(chan searchResult, 1)
	vectorCh := make(chan searchResult, 1)

	go func() {
		results, meta, err := o.searchBM25(ctx, tenantID, query, maxResults*2, opts)
		bm25Ch <- searchResult{results, meta, err}
	}()

	go func() {
		results, meta, err := o.searchVector(ctx, tenantID, query, maxResults*2, opts)
		vectorCh <- searchResult{results, meta, err}
	}()

	bm25Result := <-bm25Ch
	vectorResult := <-vectorCh

	var meta shared.SearchMetadata

	// Collect results from both, normalize and fuse
	scoreMap := make(map[string]*shared.SearchResult) // chunk_id → result

	if bm25Result.err == nil && len(bm25Result.results) > 0 {
		meta.BM25TimeMs = bm25Result.meta.BM25TimeMs
		maxBM25 := bm25Result.results[0].Score
		minBM25 := bm25Result.results[len(bm25Result.results)-1].Score
		rangeBM25 := maxBM25 - minBM25
		if rangeBM25 == 0 {
			rangeBM25 = 1
		}
		for _, r := range bm25Result.results {
			normScore := (r.Score - minBM25) / rangeBM25
			r.Score = (1 - alpha) * normScore
			scoreMap[r.ChunkID] = &r
		}
	}

	if vectorResult.err == nil && len(vectorResult.results) > 0 {
		meta.VectorTimeMs = vectorResult.meta.VectorTimeMs
		for _, r := range vectorResult.results {
			// Vector scores are already 0-1 (cosine similarity)
			vectorScore := alpha * r.Score
			if existing, ok := scoreMap[r.ChunkID]; ok {
				existing.Score += vectorScore
			} else {
				r.Score = vectorScore
				scoreMap[r.ChunkID] = &r
			}
		}
	}

	// Sort by fused score
	results := make([]shared.SearchResult, 0, len(scoreMap))
	for _, r := range scoreMap {
		results = append(results, *r)
	}

	// Simple sort (bubble sort for small N)
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	return results, meta, nil
}

func floatsToArrayLiteral(v []float32) string {
	var b strings.Builder
	b.WriteString("[")
	for i, f := range v {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%g", f)
	}
	b.WriteString("]")
	return b.String()
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

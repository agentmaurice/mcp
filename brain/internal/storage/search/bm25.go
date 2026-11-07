package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
)

// BM25SearchResult holds a raw keyword search result with its document.
type BM25SearchResult struct {
	ChunkID    string
	DocumentID string
	Score      float64
	Content    string
	ChunkType  string
	SymbolName string
	StartLine  int
	EndLine    int
	FilePath   string
	Title      string
	DocType    string
	Language   string
	SourceID   string
}

// Filters restricts keyword searches to documents of the given types, languages
// and sources. An empty list means no restriction on that dimension.
type Filters struct {
	DocTypes  []string
	Languages []string
	SourceIDs []string
}

// MaxExactIdentifiers bounds the number of identifiers searched verbatim by one
// query. Matching is one scan of the tenant chunks whatever the count, so the bound
// only guards against abusive queries. Identifiers beyond it are not searched;
// SearchExactIdentifiers returns how many were left out, and the caller reports them.
const MaxExactIdentifiers = 1000

// SearchBM25 ranks chunks with the DuckDB FTS index (fts_main_chunks.match_bm25).
// The index must be current: callers go through storage.FullTextIndexer first.
func SearchBM25(ctx context.Context, q storage.Querier, tenantID, query string, maxResults int, filters Filters) ([]BM25SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	whereSQL, args := filterSQL(tenantID, filters)
	sqlQuery := fmt.Sprintf(`
		SELECT c.id, c.document_id, c.content, c.chunk_type, c.symbol_name, c.start_line, c.end_line,
		       d.file_path, d.title, d.doc_type, d.language, d.source_id, fts.score
		FROM chunks c
		JOIN documents d ON c.document_id = d.id
		JOIN (
			SELECT id, fts_main_chunks.match_bm25(id, ?) AS score
			FROM chunks
			WHERE tenant_id = ?
		) fts ON c.id = fts.id
		WHERE %s AND fts.score IS NOT NULL
		ORDER BY fts.score DESC, c.id ASC
		LIMIT ?
	`, whereSQL)

	allArgs := append([]any{query, tenantID}, args...)
	allArgs = append(allArgs, maxResults)
	result, err := q.QueryContext(ctx, sqlQuery, allArgs...)
	if err != nil {
		return nil, fmt.Errorf("BM25 search failed: %w", err)
	}
	return scanBM25Rows(result.Rows, true), nil
}

// SearchExactIdentifiers returns chunks that contain one of the identifiers as a
// whole token, compared case-insensitively. A token is a run of letters, digits,
// underscores, dots and hyphens; trailing dots and hyphens are not part of it. So
// "AGENT-530." (sentence end) matches AGENT-530, while "AGENT-530--1" and
// "2026.10.19-3015.2" do not. It does not rank: the caller places these hits before
// the BM25 hits.
func SearchExactIdentifiers(ctx context.Context, q storage.Querier, tenantID string, identifiers []string, maxResults int, filters Filters) ([]BM25SearchResult, []string, error) {
	identifiers = dedupeNonEmpty(identifiers)
	if len(identifiers) == 0 {
		return nil, nil, nil
	}
	var ignored []string
	if len(identifiers) > MaxExactIdentifiers {
		ignored = append([]string(nil), identifiers[MaxExactIdentifiers:]...)
		identifiers = identifiers[:MaxExactIdentifiers]
	}
	lowered := make([]string, len(identifiers))
	for i, id := range identifiers {
		lowered[i] = strings.ToLower(id)
	}

	// Tokens of each chunk are computed once in a CTE. Hits are ordered by how many
	// query identifiers the chunk contains, most first, then by position: a chunk that
	// repeats the identifier outranks one that names it once.
	whereSQL, args := filterSQL(tenantID, filters)
	args = append(append(args, lowered), lowered, maxResults)
	sqlQuery := fmt.Sprintf(`
		WITH tokened AS (
			SELECT c.id, c.document_id, c.content, c.chunk_type, c.symbol_name, c.start_line, c.end_line, c.chunk_index,
			       d.file_path, d.title, d.doc_type, d.language, d.source_id,
			       list_transform(regexp_extract_all(lower(c.bm25_content), '[\p{L}\p{N}_][\p{L}\p{N}_.\-]*'), lambda x: rtrim(x, '.-')) AS toks
			FROM chunks c
			JOIN documents d ON c.document_id = d.id
			WHERE %s
		)
		SELECT id, document_id, content, chunk_type, symbol_name, start_line, end_line,
		       file_path, title, doc_type, language, source_id, 0.0 AS score
		FROM tokened
		WHERE list_has_any(toks, ?::VARCHAR[])
		ORDER BY len(list_filter(toks, lambda x: list_contains(?::VARCHAR[], x))) DESC, document_id ASC, chunk_index ASC
		LIMIT ?
	`, whereSQL)

	result, err := q.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("exact identifier search failed: %w", err)
	}
	return scanBM25Rows(result.Rows, false), ignored, nil
}

// SearchSubstring is the explicit fallback used when no full-text index is
// available. It matches substrings only and does not rank.
func SearchSubstring(ctx context.Context, q storage.Querier, tenantID, query string, maxResults int, filters Filters) ([]BM25SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	whereSQL, args := filterSQL(tenantID, filters)
	sqlQuery := fmt.Sprintf(`
		SELECT c.id, c.document_id, c.content, c.chunk_type, c.symbol_name, c.start_line, c.end_line,
		       d.file_path, d.title, d.doc_type, d.language, d.source_id, 0.0 AS score
		FROM chunks c
		JOIN documents d ON c.document_id = d.id
		WHERE %s AND contains(lower(c.bm25_content), lower(?))
		ORDER BY c.document_id ASC, c.chunk_index ASC
		LIMIT ?
	`, whereSQL)
	args = append(args, query, maxResults)
	result, err := q.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("substring search failed: %w", err)
	}
	return scanBM25Rows(result.Rows, false), nil
}

func filterSQL(tenantID string, filters Filters) (string, []any) {
	clauses := []string{"c.tenant_id = ?"}
	args := []any{tenantID}
	appendIn := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, len(values))
		for i, v := range values {
			placeholders[i] = "?"
			args = append(args, v)
		}
		clauses = append(clauses, fmt.Sprintf("%s IN (%s)", column, strings.Join(placeholders, ",")))
	}
	appendIn("d.doc_type", filters.DocTypes)
	appendIn("d.language", filters.Languages)
	appendIn("d.source_id", filters.SourceIDs)
	return strings.Join(clauses, " AND "), args
}

func scanBM25Rows(rows []map[string]any, withScore bool) []BM25SearchResult {
	results := make([]BM25SearchResult, 0, len(rows))
	for _, row := range rows {
		r := BM25SearchResult{
			ChunkID:    toString(row["id"]),
			DocumentID: toString(row["document_id"]),
			Content:    toString(row["content"]),
			ChunkType:  toString(row["chunk_type"]),
			SymbolName: toString(row["symbol_name"]),
			StartLine:  toInt(row["start_line"]),
			EndLine:    toInt(row["end_line"]),
			FilePath:   toString(row["file_path"]),
			Title:      toString(row["title"]),
			DocType:    toString(row["doc_type"]),
			Language:   toString(row["language"]),
			SourceID:   toString(row["source_id"]),
		}
		if withScore {
			r.Score = toFloat64(row["score"])
		}
		results = append(results, r)
	}
	return results
}

func dedupeNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

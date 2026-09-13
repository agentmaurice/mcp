package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
)

// BM25SearchResult holds a raw BM25 search result.
type BM25SearchResult struct {
	ChunkID    string
	DocumentID string
	Score      float64
	Content    string
	ChunkType  string
	SymbolName string
	StartLine  int
	EndLine    int
}

// SearchBM25 performs a BM25 full-text search on chunks.
func SearchBM25(ctx context.Context, q storage.Querier, tenantID, query string, maxResults int, docTypes, languages, sourceIDs []string) ([]BM25SearchResult, error) {
	if query == "" {
		return nil, nil
	}

	var whereClauses []string
	var args []any

	whereClauses = append(whereClauses, "c.tenant_id = ?")
	args = append(args, tenantID)

	if len(docTypes) > 0 {
		placeholders := make([]string, len(docTypes))
		for i, dt := range docTypes {
			placeholders[i] = "?"
			args = append(args, dt)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("d.doc_type IN (%s)", strings.Join(placeholders, ",")))
	}

	if len(languages) > 0 {
		placeholders := make([]string, len(languages))
		for i, l := range languages {
			placeholders[i] = "?"
			args = append(args, l)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("d.language IN (%s)", strings.Join(placeholders, ",")))
	}

	if len(sourceIDs) > 0 {
		placeholders := make([]string, len(sourceIDs))
		for i, s := range sourceIDs {
			placeholders[i] = "?"
			args = append(args, s)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("d.source_id IN (%s)", strings.Join(placeholders, ",")))
	}

	whereSQL := strings.Join(whereClauses, " AND ")
	args = append(args, query, maxResults)

	sqlQuery := fmt.Sprintf(`
		SELECT c.id, c.document_id, fts.score, c.content, c.chunk_type, c.symbol_name, c.start_line, c.end_line
		FROM chunks c
		JOIN documents d ON c.document_id = d.id
		JOIN (
			SELECT *, fts_main_chunks.match_bm25(id, ?) AS score
			FROM chunks
		) fts ON c.id = fts.id
		WHERE %s AND fts.score IS NOT NULL
		ORDER BY fts.score DESC
		LIMIT ?
	`, whereSQL)

	result, err := q.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("BM25 search failed: %w", err)
	}

	var results []BM25SearchResult
	for _, row := range result.Rows {
		r := BM25SearchResult{
			ChunkID:    toString(row["id"]),
			DocumentID: toString(row["document_id"]),
			Score:      toFloat64(row["score"]),
			Content:    toString(row["content"]),
			ChunkType:  toString(row["chunk_type"]),
			SymbolName: toString(row["symbol_name"]),
			StartLine:  toInt(row["start_line"]),
			EndLine:    toInt(row["end_line"]),
		}
		results = append(results, r)
	}
	return results, nil
}

package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
)

// VectorSearchResult holds a raw vector search result.
type VectorSearchResult struct {
	ChunkID    string
	DocumentID string
	Score      float64
	Content    string
	ChunkType  string
	SymbolName string
	StartLine  int
	EndLine    int
}

// SearchVector performs a cosine similarity search using DuckDB vss.
func SearchVector(ctx context.Context, q storage.Querier, tenantID string, queryEmbedding []float32, maxResults int, minScore float32, docTypes, languages, sourceIDs []string) ([]VectorSearchResult, error) {
	if len(queryEmbedding) == 0 {
		return nil, nil
	}

	var whereClauses []string
	var args []any

	embeddingStr := floatsToArrayLiteral(queryEmbedding)

	whereClauses = append(whereClauses, "c.tenant_id = ?")
	args = append(args, tenantID)
	whereClauses = append(whereClauses, "c.embedding IS NOT NULL")

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
	args = append(args, minScore, maxResults)

	sqlQuery := fmt.Sprintf(`
		SELECT c.id, c.document_id,
			array_cosine_similarity(c.embedding, %s::FLOAT[]) AS score,
			c.content, c.chunk_type, c.symbol_name, c.start_line, c.end_line
		FROM chunks c
		JOIN documents d ON c.document_id = d.id
		WHERE %s
			AND array_cosine_similarity(c.embedding, %s::FLOAT[]) >= ?
		ORDER BY score DESC
		LIMIT ?
	`, embeddingStr, whereSQL, embeddingStr)

	result, err := q.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search failed: %w", err)
	}

	var results []VectorSearchResult
	for _, row := range result.Rows {
		r := VectorSearchResult{
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

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
)

const (
	defaultSearchSummaryLimit  = 220
	defaultContentPreviewLimit = 240
	defaultChunkContentLimit   = 800
	defaultContextLimit        = 500
	searchNextHint             = "Use brain.timeline for nearby context or brain.get for exact details."
)

type compactSearchResponse struct {
	Mode           string                `json:"mode"`
	Query          string                `json:"query"`
	TotalResults   int                   `json:"total_results"`
	Results        []compactSearchResult `json:"results"`
	SearchMetadata shared.SearchMetadata `json:"search_metadata"`
}

type compactSearchResult struct {
	ID         string          `json:"id"`
	Score      float64         `json:"score"`
	DocumentID string          `json:"document_id"`
	Document   compactDocument `json:"document"`
	ChunkType  string          `json:"chunk_type"`
	SymbolName string          `json:"symbol_name,omitempty"`
	StartLine  int             `json:"start_line,omitempty"`
	EndLine    int             `json:"end_line,omitempty"`
	Summary    string          `json:"summary"`
	NextHint   string          `json:"next_hint"`
}

type compactDocument struct {
	ID       string `json:"id"`
	FilePath string `json:"file_path"`
	Title    string `json:"title,omitempty"`
	DocType  string `json:"doc_type,omitempty"`
	Language string `json:"language,omitempty"`
}

type minimalSource struct {
	ID          string `json:"id,omitempty"`
	SourceType  string `json:"source_type,omitempty"`
	SourceURI   string `json:"source_uri,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type timelineResponse struct {
	Groups []timelineGroup `json:"groups"`
}

type timelineGroup struct {
	Document        compactDocument `json:"document"`
	Source          minimalSource   `json:"source,omitempty"`
	EstimatedChars  int             `json:"estimated_chars"`
	EstimatedTokens int             `json:"estimated_tokens"`
	Items           []timelineItem  `json:"items"`
}

type timelineItem struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	ChunkIndex int    `json:"chunk_index"`
	ChunkType  string `json:"chunk_type"`
	SymbolName string `json:"symbol_name,omitempty"`
	StartLine  int    `json:"start_line,omitempty"`
	EndLine    int    `json:"end_line,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Metadata   any    `json:"metadata,omitempty"`
	Content    string `json:"content,omitempty"`
}

type getResponse struct {
	Items         []getItem `json:"items"`
	Truncated     bool      `json:"truncated"`
	ReturnedChars int       `json:"returned_chars"`
}

type getItem struct {
	Kind     string          `json:"kind"`
	ID       string          `json:"id"`
	Document compactDocument `json:"document,omitempty"`
	Source   minimalSource   `json:"source,omitempty"`

	Chunk  *getChunkPayload    `json:"chunk,omitempty"`
	Chunks []getChunkReference `json:"chunks,omitempty"`
}

type getChunkPayload struct {
	ID         string `json:"id"`
	ChunkIndex int    `json:"chunk_index"`
	ChunkType  string `json:"chunk_type"`
	SymbolName string `json:"symbol_name,omitempty"`
	StartLine  int    `json:"start_line,omitempty"`
	EndLine    int    `json:"end_line,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Context    string `json:"context,omitempty"`
	Metadata   any    `json:"metadata,omitempty"`
	Content    string `json:"content,omitempty"`
}

type getChunkReference struct {
	ID             string `json:"id"`
	ChunkIndex     int    `json:"chunk_index"`
	ChunkType      string `json:"chunk_type"`
	SymbolName     string `json:"symbol_name,omitempty"`
	StartLine      int    `json:"start_line,omitempty"`
	EndLine        int    `json:"end_line,omitempty"`
	Summary        string `json:"summary,omitempty"`
	ContentPreview string `json:"content_preview,omitempty"`
	Metadata       any    `json:"metadata,omitempty"`
}

type chunkRecord struct {
	ChunkID    string
	TenantID   string
	DocumentID string
	ChunkIndex int
	Content    string
	Summary    string
	Context    string
	ChunkType  string
	SymbolName string
	StartLine  int
	EndLine    int
	ChunkMeta  any
	Document   compactDocument
	Source     minimalSource
}

func buildCompactSearchResponse(result *shared.SearchResponse) *compactSearchResponse {
	if result == nil {
		return &compactSearchResponse{}
	}

	items := make([]compactSearchResult, 0, len(result.Results))
	for _, entry := range result.Results {
		items = append(items, compactSearchResult{
			ID:         canonicalBrainID("chunk", entry.Chunk.ID),
			Score:      entry.Score,
			DocumentID: entry.Document.ID,
			Document: compactDocument{
				ID:       entry.Document.ID,
				FilePath: entry.Document.FilePath,
				Title:    entry.Document.Title,
				DocType:  entry.Document.DocType,
				Language: entry.Document.Language,
			},
			ChunkType:  entry.Chunk.ChunkType,
			SymbolName: entry.Chunk.SymbolName,
			StartLine:  entry.Chunk.StartLine,
			EndLine:    entry.Chunk.EndLine,
			Summary:    buildSummary(entry.Chunk.Summary, entry.Chunk.Content, defaultSearchSummaryLimit),
			NextHint:   searchNextHint,
		})
	}

	return &compactSearchResponse{
		Mode:           result.Mode,
		Query:          result.Query,
		TotalResults:   result.TotalResults,
		Results:        items,
		SearchMetadata: result.SearchMetadata,
	}
}

func buildSummary(summary, content string, limit int) string {
	value := strings.TrimSpace(summary)
	if value == "" {
		value = strings.TrimSpace(content)
	}
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 {
		limit = defaultSearchSummaryLimit
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func estimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

func canonicalBrainID(kind, value string) string {
	return kind + ":" + value
}

func parseBrainID(raw string) (string, string, error) {
	kind, value, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || kind == "" || value == "" {
		return "", "", fmt.Errorf("invalid id: %s", raw)
	}
	return kind, value, nil
}

func includeField(include []string, field string) bool {
	for _, item := range include {
		if strings.EqualFold(strings.TrimSpace(item), field) {
			return true
		}
	}
	return false
}

func parseJSONValue(raw any) any {
	switch value := raw.(type) {
	case nil:
		return nil
	case map[string]any, []any:
		return value
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil
		}
		var out any
		if err := json.Unmarshal([]byte(text), &out); err == nil {
			return out
		}
		return text
	case []byte:
		if len(value) == 0 {
			return nil
		}
		var out any
		if err := json.Unmarshal(value, &out); err == nil {
			return out
		}
		return string(value)
	default:
		return value
	}
}

func fetchChunkByID(ctx context.Context, q storage.Querier, tenantID, chunkID string) (*chunkRecord, error) {
	result, err := q.QueryContext(ctx, `
		SELECT
			c.id,
			c.tenant_id,
			c.document_id,
			c.chunk_index,
			c.content,
			c.summary,
			c.context,
			c.chunk_type,
			c.symbol_name,
			c.start_line,
			c.end_line,
			c.metadata,
			d.id AS doc_id,
			d.file_path,
			d.title,
			d.doc_type,
			d.language,
			s.id AS source_id,
			s.source_type,
			s.source_uri,
			s.display_name
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		LEFT JOIN sources s ON s.id = d.source_id
		WHERE c.tenant_id = ? AND c.id = ?
		LIMIT 1
	`, tenantID, chunkID)
	if err != nil {
		return nil, err
	}
	if len(result.Rows) == 0 {
		return nil, fmt.Errorf("chunk not found: %s", chunkID)
	}
	return recordFromRow(result.Rows[0]), nil
}

func fetchChunkWindow(ctx context.Context, q storage.Querier, tenantID, documentID string, minIndex, maxIndex int) ([]chunkRecord, error) {
	result, err := q.QueryContext(ctx, `
		SELECT
			c.id,
			c.tenant_id,
			c.document_id,
			c.chunk_index,
			c.content,
			c.summary,
			c.context,
			c.chunk_type,
			c.symbol_name,
			c.start_line,
			c.end_line,
			c.metadata,
			d.id AS doc_id,
			d.file_path,
			d.title,
			d.doc_type,
			d.language,
			s.id AS source_id,
			s.source_type,
			s.source_uri,
			s.display_name
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		LEFT JOIN sources s ON s.id = d.source_id
		WHERE c.tenant_id = ? AND c.document_id = ? AND c.chunk_index BETWEEN ? AND ?
		ORDER BY c.chunk_index ASC
	`, tenantID, documentID, minIndex, maxIndex)
	if err != nil {
		return nil, err
	}
	records := make([]chunkRecord, 0, len(result.Rows))
	for _, row := range result.Rows {
		records = append(records, *recordFromRow(row))
	}
	return records, nil
}

func fetchDocumentByID(ctx context.Context, q storage.Querier, tenantID, documentID string) (*chunkRecord, error) {
	result, err := q.QueryContext(ctx, `
		SELECT
			d.id AS doc_id,
			d.file_path,
			d.title,
			d.doc_type,
			d.language,
			s.id AS source_id,
			s.source_type,
			s.source_uri,
			s.display_name
		FROM documents d
		LEFT JOIN sources s ON s.id = d.source_id
		WHERE d.tenant_id = ? AND d.id = ?
		LIMIT 1
	`, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	if len(result.Rows) == 0 {
		return nil, fmt.Errorf("document not found: %s", documentID)
	}
	row := result.Rows[0]
	return &chunkRecord{
		DocumentID: documentID,
		Document: compactDocument{
			ID:       toString(row["doc_id"]),
			FilePath: toString(row["file_path"]),
			Title:    toString(row["title"]),
			DocType:  toString(row["doc_type"]),
			Language: toString(row["language"]),
		},
		Source: minimalSource{
			ID:          toString(row["source_id"]),
			SourceType:  toString(row["source_type"]),
			SourceURI:   toString(row["source_uri"]),
			DisplayName: toString(row["display_name"]),
		},
	}, nil
}

func fetchDocumentChunks(ctx context.Context, q storage.Querier, tenantID, documentID string) ([]chunkRecord, error) {
	return fetchChunkWindow(ctx, q, tenantID, documentID, 0, 1_000_000)
}

func recordFromRow(row map[string]any) *chunkRecord {
	return &chunkRecord{
		ChunkID:    toString(row["id"]),
		TenantID:   toString(row["tenant_id"]),
		DocumentID: toString(row["document_id"]),
		ChunkIndex: toInt(row["chunk_index"]),
		Content:    toString(row["content"]),
		Summary:    toString(row["summary"]),
		Context:    toString(row["context"]),
		ChunkType:  toString(row["chunk_type"]),
		SymbolName: toString(row["symbol_name"]),
		StartLine:  toInt(row["start_line"]),
		EndLine:    toInt(row["end_line"]),
		ChunkMeta:  parseJSONValue(row["metadata"]),
		Document: compactDocument{
			ID:       firstNonEmpty(toString(row["doc_id"]), toString(row["document_id"])),
			FilePath: toString(row["file_path"]),
			Title:    toString(row["title"]),
			DocType:  toString(row["doc_type"]),
			Language: toString(row["language"]),
		},
		Source: minimalSource{
			ID:          toString(row["source_id"]),
			SourceType:  toString(row["source_type"]),
			SourceURI:   toString(row["source_uri"]),
			DisplayName: toString(row["display_name"]),
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sortTimelineItems(items []timelineItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].ChunkIndex == items[j].ChunkIndex {
			return items[i].ID < items[j].ID
		}
		return items[i].ChunkIndex < items[j].ChunkIndex
	})
}

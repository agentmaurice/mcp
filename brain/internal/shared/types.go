package shared

import (
	"encoding/json"
	"time"
)

// Source represents an indexed data source.
type Source struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenant_id"`
	SourceType    string          `json:"source_type"`    // 'filesystem', 'git', 'confluence', 'memory', 'jira'
	SourceURI     string          `json:"source_uri"`     // path or URL
	DisplayName   string          `json:"display_name"`
	Config        json.RawMessage `json:"config,omitempty"`
	LastIndexedAt *time.Time      `json:"last_indexed_at,omitempty"`
	Status        string          `json:"status"` // 'pending', 'indexing', 'ready', 'error'
	ErrorMessage  string          `json:"error_message,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// Document represents an indexed file.
type Document struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenant_id"`
	SourceID    string          `json:"source_id"`
	FilePath    string          `json:"file_path"`
	Title       string          `json:"title,omitempty"`
	DocType     string          `json:"doc_type"`  // 'code', 'markdown', 'config', 'api_spec', 'ticket', 'wiki'
	Language    string          `json:"language"`  // 'go', 'python', 'typescript', etc.
	ContentHash string          `json:"content_hash"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	SizeBytes   int64           `json:"size_bytes"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Chunk represents a searchable unit within a document.
type Chunk struct {
	ID             string          `json:"id"`
	TenantID       string          `json:"tenant_id"`
	DocumentID     string          `json:"document_id"`
	ChunkIndex     int             `json:"chunk_index"`
	Content        string          `json:"content"`
	Summary        string          `json:"summary,omitempty"`
	Context        string          `json:"context,omitempty"`
	ChunkType      string          `json:"chunk_type"`       // 'function', 'class', 'method', 'section', 'paragraph', 'config_block'
	SymbolName     string          `json:"symbol_name,omitempty"`
	StartLine      int             `json:"start_line"`
	EndLine        int             `json:"end_line"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	Embedding      []float32       `json:"-"`
	EmbeddingModel string          `json:"embedding_model,omitempty"`
	BM25Content    string          `json:"-"`
	CreatedAt      time.Time       `json:"created_at"`
}

// IndexJob represents an indexation job.
type IndexJob struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	SourceID       string     `json:"source_id"`
	Status         string     `json:"status"` // 'pending', 'running', 'completed', 'failed'
	Progress       float64    `json:"progress"`
	TotalFiles     int        `json:"total_files"`
	ProcessedFiles int        `json:"processed_files"`
	TotalChunks    int        `json:"total_chunks"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// SearchResult represents a single search result.
type SearchResult struct {
	Rank     int      `json:"rank"`
	Score    float64  `json:"score"`
	ChunkID  string   `json:"chunk_id"`
	Document Document `json:"document"`
	Chunk    Chunk    `json:"chunk"`
	Source   Source   `json:"source,omitempty"`
}

// SearchResponse wraps the full search response.
type SearchResponse struct {
	Mode           string         `json:"mode"`
	Query          string         `json:"query"`
	TotalResults   int            `json:"total_results"`
	Results        []SearchResult `json:"results"`
	SearchMetadata SearchMetadata `json:"search_metadata"`
}

// SearchMetadata provides execution metadata.
type SearchMetadata struct {
	BM25TimeMs   int64  `json:"bm25_time_ms,omitempty"`
	VectorTimeMs int64  `json:"vector_time_ms,omitempty"`
	TotalTimeMs  int64  `json:"total_time_ms"`
	ModeSelected string `json:"mode_selected"`
	ModeReason   string `json:"mode_reason"`
}

// SearchOpts defines search parameters.
type SearchOpts struct {
	Mode       string   `json:"mode"`        // 'auto', 'bm25', 'vector', 'hybrid', 'multi'
	MaxResults int      `json:"max_results"`
	Alpha      float64  `json:"alpha"`
	DocTypes   []string `json:"doc_types"`
	Languages  []string `json:"languages"`
	SourceIDs  []string `json:"source_ids"`
}

// SourceFile represents a file discovered by a connector.
type SourceFile struct {
	Path        string `json:"path"`
	ContentHash string `json:"content_hash"`
	Size        int64  `json:"size"`
	Language    string `json:"language"`
	DocType     string `json:"doc_type"`
}

// ChunkInput represents a chunk produced by the chunker (before storage).
type ChunkInput struct {
	Content    string
	Type       string // 'function', 'class', 'method', 'section', 'paragraph', 'config_block'
	SymbolName string
	StartLine  int
	EndLine    int
	Metadata   map[string]interface{}
}

// SourceStats provides aggregated statistics for a source.
type SourceStats struct {
	Source         Source `json:"source"`
	DocumentCount  int   `json:"document_count"`
	ChunkCount     int   `json:"chunk_count"`
}

// TenantStats provides aggregated statistics for a tenant.
type TenantStats struct {
	TenantID       string `json:"tenant_id"`
	SourceCount    int    `json:"source_count"`
	DocumentCount  int    `json:"document_count"`
	ChunkCount     int    `json:"chunk_count"`
	EmbeddingModel string `json:"embedding_model"`
	LastIndexedAt  string `json:"last_indexed_at,omitempty"`
}

package shared

import "github.com/rs/xid"

// Query represents a RAG query
type Query struct {
	Text            string
	Language        string
	MaxTokens       int
	TenantID        xid.ID
	DeploymentID    xid.ID
	MetadataFilters map[string]string // Optional metadata filters (e.g. {"offre_id": "123"})
}

// Chunk represents a document chunk with score
type Chunk struct {
	ID         string
	DocumentID xid.ID
	TenantID   xid.ID
	Text       string
	Score      float64
	Metadata   map[string]interface{}
	VectorID   string
	Embedding  []float64
}

// AnalyzedDoc represents an analyzed document
type AnalyzedDoc struct {
	Title    string
	Sections []Section
	Metadata map[string]interface{}
}

// Section represents a document section
type Section struct {
	ID    string
	Type  string // summary, content, code, etc.
	Text  string
	Level int
}

// Answer represents a RAG answer
type Answer struct {
	Text      string
	Citations []Citation
	Metadata  map[string]interface{}
}

// Citation represents a citation source
type Citation struct {
	ChunkID    string
	DocumentID xid.ID
	Snippet    string
	Metadata   map[string]interface{}
}

// QueryAnalysis represents query analysis result
type QueryAnalysis struct {
	Intent     string // factual, procedural, exploratory
	Keywords   []string
	Language   string
	Complexity int
}

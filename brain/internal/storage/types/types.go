package types

import "context"

// QueryResult holds rows returned by a query, decoupled from database/sql.
type QueryResult struct {
	Columns []string
	Rows    []map[string]any
}

// Querier abstracts SQL execution across backends (CGO sql.DB or HTTP).
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*QueryResult, error)
	ExecContext(ctx context.Context, query string, args ...any) (int64, error)
}

// FullTextOptions configures the BM25 full-text index built on chunks.
type FullTextOptions struct {
	// Stemmer is a DuckDB FTS stemmer name (for example "porter", "french", "none").
	Stemmer string
	// Stopwords is "none", "english", or the name of a DuckDB table of stopwords.
	Stopwords string
}

// FullTextIndexer is implemented by backends that maintain a BM25 full-text
// index over chunks. Backends without it (for example duckhttp) make the search
// orchestrator fall back explicitly and report the effective mode.
type FullTextIndexer interface {
	// MarkFullTextStale records that chunks or documents of tenantID changed.
	// Writers call it while they hold the tenant writer, so the next
	// EnsureFullTextIndex cannot run between a write and its marking.
	MarkFullTextStale(tenantID string)
	// EnsureFullTextIndex rebuilds the index when it is stale, when it was never
	// built in this process, or when opts differ from the last build. Concurrent
	// callers are coalesced through the tenant writer.
	EnsureFullTextIndex(ctx context.Context, tenantID string, opts FullTextOptions) error
}

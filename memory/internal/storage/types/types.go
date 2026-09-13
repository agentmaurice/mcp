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

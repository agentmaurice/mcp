package shared

// Column describes a query result column.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// QueryStats provides execution metadata.
type QueryStats struct {
	ElapsedMs int64 `json:"elapsedMs"`
	RowCount  int   `json:"rowCount"`
}

// QueryResult is the structured content returned by query tools.
type QueryResult struct {
	Columns    []Column   `json:"columns"`
	Rows       [][]any    `json:"rows"`
	Truncated  bool       `json:"truncated"`
	QueryStats QueryStats `json:"queryStats"`
}

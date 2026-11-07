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
//
// Truncated is true only when rows were dropped because the row cap was reached
// (or the response size limit was hit). An explicit or added LIMIT alone does not
// set it. TenantEmpty and Warnings report a tenant without any entity, document or
// fact, whatever the number of rows, so the result is explained rather than silent.
type QueryResult struct {
	Columns    []Column   `json:"columns"`
	Rows       [][]any    `json:"rows"`
	Truncated  bool       `json:"truncated"`
	QueryStats QueryStats `json:"queryStats"`
	// TenantEmpty is true or false when the tenant emptiness check ran, nil (JSON null)
	// when it failed.
	TenantEmpty *bool    `json:"tenant_empty"`
	Warnings    []string `json:"warnings,omitempty"`
}

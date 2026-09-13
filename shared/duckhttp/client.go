package duckhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// QueryResult holds the result of a DuckDB query.
type QueryResult struct {
	Columns []string
	Rows    []map[string]any
}

// Client communicates with a DuckDB httpserver instance.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new DuckDB HTTP client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Query executes a SQL query and returns results.
func (c *Client) Query(ctx context.Context, sql string, args ...any) (*QueryResult, error) {
	finalSQL := sql
	if len(args) > 0 {
		finalSQL = interpolateArgs(sql, args)
	}

	body := bytes.NewBufferString(finalSQL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/query", body)
	if err != nil {
		return nil, fmt.Errorf("duckhttp: failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("duckhttp: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("duckhttp: failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("duckhttp: server error %d: %s", resp.StatusCode, string(respBody))
	}

	return parseQueryResult(respBody)
}

// Exec executes a SQL statement that doesn't return rows.
func (c *Client) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	_, err := c.Query(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return 0, nil
}

// HealthCheck verifies the DuckDB httpserver is responsive.
func (c *Client) HealthCheck(ctx context.Context) error {
	_, err := c.Query(ctx, "SELECT 1")
	return err
}

// parseQueryResult parses the JSON response from DuckDB httpserver.
// NOTE: This format is based on expected httpserver behavior.
// Adapt after validating with the PoC (Task 1).
func parseQueryResult(data []byte) (*QueryResult, error) {
	var raw struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("duckhttp: failed to parse response: %w", err)
	}

	result := &QueryResult{
		Columns: raw.Columns,
		Rows:    make([]map[string]any, len(raw.Rows)),
	}
	for i, row := range raw.Rows {
		m := make(map[string]any, len(raw.Columns))
		for j, col := range raw.Columns {
			if j < len(row) {
				m[col] = row[j]
			}
		}
		result.Rows[i] = m
	}
	return result, nil
}

// interpolateArgs replaces ? placeholders with formatted values.
// DuckDB httpserver may not support parameterized queries,
// so we do client-side interpolation with proper escaping.
func interpolateArgs(sql string, args []any) string {
	result := make([]byte, 0, len(sql))
	argIdx := 0
	for i := 0; i < len(sql); i++ {
		if sql[i] == '?' && argIdx < len(args) {
			result = append(result, []byte(formatArg(args[argIdx]))...)
			argIdx++
		} else {
			result = append(result, sql[i])
		}
	}
	return string(result)
}

func formatArg(arg any) string {
	switch v := arg.(type) {
	case string:
		escaped := bytes.ReplaceAll([]byte(v), []byte("'"), []byte("''"))
		return "'" + string(escaped) + "'"
	case int:
		return fmt.Sprintf("%d", v)
	case int32:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case float32:
		return fmt.Sprintf("%g", v)
	case float64:
		return fmt.Sprintf("%g", v)
	case nil:
		return "NULL"
	default:
		return fmt.Sprintf("'%v'", v)
	}
}

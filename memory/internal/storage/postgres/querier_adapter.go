package postgres

import (
	"context"
	"database/sql"
	"fmt"

	stypes "github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage/types"
)

// sqlQuerier wraps *sql.DB to implement stypes.Querier.
type sqlQuerier struct {
	db *sql.DB
}

func newSQLQuerier(db *sql.DB) stypes.Querier {
	return &sqlQuerier{db: db}
}

func (q *sqlQuerier) QueryContext(ctx context.Context, query string, args ...any) (*stypes.QueryResult, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

func (q *sqlQuerier) ExecContext(ctx context.Context, query string, args ...any) (int64, error) {
	result, err := q.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// scanRows converts *sql.Rows into a stypes.QueryResult.
func scanRows(rows *sql.Rows) (*stypes.QueryResult, error) {
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	var result []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			row[col] = values[i]
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &stypes.QueryResult{Columns: columns, Rows: result}, nil
}

// sqlConnQuerier wraps *sql.Conn to implement stypes.Querier.
type sqlConnQuerier struct {
	conn *sql.Conn
}

func newSQLConnQuerier(conn *sql.Conn) stypes.Querier {
	return &sqlConnQuerier{conn: conn}
}

func (q *sqlConnQuerier) QueryContext(ctx context.Context, query string, args ...any) (*stypes.QueryResult, error) {
	rows, err := q.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

func (q *sqlConnQuerier) ExecContext(ctx context.Context, query string, args ...any) (int64, error) {
	result, err := q.conn.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

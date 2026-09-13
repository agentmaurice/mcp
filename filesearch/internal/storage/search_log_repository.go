package storage

import (
	"context"
	"fmt"
)

// SearchLogRepository manages search log records in the database.
type SearchLogRepository struct {
	db *DB
}

// NewSearchLogRepository creates a new search log repository.
func NewSearchLogRepository(db *DB) *SearchLogRepository {
	return &SearchLogRepository{db: db}
}

// Create inserts a new search log record.
func (r *SearchLogRepository) Create(ctx context.Context, log *SearchLog) error {
	query := `
		INSERT INTO search_logs (id, store_id, query, results_count, execution_time_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		log.ID,
		log.StoreID,
		log.Query,
		log.ResultsCount,
		log.ExecutionTimeMs,
		log.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create search log: %w", err)
	}
	return nil
}

// ListByStoreID lists search logs for a given store.
func (r *SearchLogRepository) ListByStoreID(ctx context.Context, storeID string, limit int) ([]*SearchLog, error) {
	query := `
		SELECT id, store_id, query, results_count, execution_time_ms, created_at
		FROM search_logs
		WHERE store_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`
	rows, err := r.db.QueryContext(ctx, query, storeID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list search logs: %w", err)
	}
	defer rows.Close()

	var logs []*SearchLog
	for rows.Next() {
		log := &SearchLog{}
		if err := rows.Scan(
			&log.ID,
			&log.StoreID,
			&log.Query,
			&log.ResultsCount,
			&log.ExecutionTimeMs,
			&log.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan search log: %w", err)
		}
		logs = append(logs, log)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating search logs: %w", err)
	}

	return logs, nil
}

// GetStats retrieves search statistics for a store.
func (r *SearchLogRepository) GetStats(ctx context.Context, storeID string) (map[string]interface{}, error) {
	query := `
		SELECT 
			COUNT(*) as total_queries,
			AVG(results_count) as avg_results,
			AVG(execution_time_ms) as avg_execution_time
		FROM search_logs
		WHERE store_id = ?
	`
	var totalQueries int
	var avgResults, avgExecutionTime float64
	err := r.db.QueryRowContext(ctx, query, storeID).Scan(&totalQueries, &avgResults, &avgExecutionTime)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}

	return map[string]interface{}{
		"total_queries":       totalQueries,
		"avg_results":         avgResults,
		"avg_execution_time":  avgExecutionTime,
	}, nil
}

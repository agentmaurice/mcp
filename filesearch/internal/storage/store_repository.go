package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// StoreRepository manages store records in the database.
type StoreRepository struct {
	db *DB
}

// NewStoreRepository creates a new store repository.
func NewStoreRepository(db *DB) *StoreRepository {
	return &StoreRepository{db: db}
}

// Create inserts a new store record.
func (r *StoreRepository) Create(ctx context.Context, store *Store) error {
	query := `
		INSERT INTO stores (id, deployment_id, gemini_store_name, display_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		store.ID,
		store.DeploymentID,
		store.GeminiStoreName,
		store.DisplayName,
		store.CreatedAt,
		store.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}
	return nil
}

// GetByDeploymentID retrieves a store by deployment ID.
func (r *StoreRepository) GetByDeploymentID(ctx context.Context, deploymentID string) (*Store, error) {
	query := `
		SELECT id, deployment_id, gemini_store_name, display_name, created_at, updated_at
		FROM stores
		WHERE deployment_id = ?
	`
	store := &Store{}
	err := r.db.QueryRowContext(ctx, query, deploymentID).Scan(
		&store.ID,
		&store.DeploymentID,
		&store.GeminiStoreName,
		&store.DisplayName,
		&store.CreatedAt,
		&store.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get store: %w", err)
	}
	return store, nil
}

// GetByID retrieves a store by its ID.
func (r *StoreRepository) GetByID(ctx context.Context, id string) (*Store, error) {
	query := `
		SELECT id, deployment_id, gemini_store_name, display_name, created_at, updated_at
		FROM stores
		WHERE id = ?
	`
	store := &Store{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&store.ID,
		&store.DeploymentID,
		&store.GeminiStoreName,
		&store.DisplayName,
		&store.CreatedAt,
		&store.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get store: %w", err)
	}
	return store, nil
}

// Update updates a store record.
func (r *StoreRepository) Update(ctx context.Context, store *Store) error {
	store.UpdatedAt = time.Now()
	query := `
		UPDATE stores
		SET gemini_store_name = ?, display_name = ?, updated_at = ?
		WHERE id = ?
	`
	_, err := r.db.ExecContext(ctx, query,
		store.GeminiStoreName,
		store.DisplayName,
		store.UpdatedAt,
		store.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update store: %w", err)
	}
	return nil
}

// Delete deletes a store by ID.
func (r *StoreRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM stores WHERE id = ?`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete store: %w", err)
	}
	return nil
}

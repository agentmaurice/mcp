package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// FileRepository manages file records in the database.
type FileRepository struct {
	db *DB
}

// NewFileRepository creates a new file repository.
func NewFileRepository(db *DB) *FileRepository {
	return &FileRepository{db: db}
}

// Create inserts a new file record.
func (r *FileRepository) Create(ctx context.Context, file *File) error {
	query := `
		INSERT INTO files (id, store_id, file_name, public_url, mime_type, google_file_name, status, metadata, error_message, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		file.ID,
		file.StoreID,
		file.FileName,
		file.PublicURL,
		file.MimeType,
		file.GoogleFileName,
		file.Status,
		file.Metadata,
		file.ErrorMessage,
		file.CreatedAt,
		file.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	return nil
}

// GetByID retrieves a file by its ID.
func (r *FileRepository) GetByID(ctx context.Context, id string) (*File, error) {
	query := `
		SELECT id, store_id, file_name, public_url, mime_type, google_file_name, status, metadata, error_message, created_at, updated_at
		FROM files
		WHERE id = ?
	`
	file := &File{}
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&file.ID,
		&file.StoreID,
		&file.FileName,
		&file.PublicURL,
		&file.MimeType,
		&file.GoogleFileName,
		&file.Status,
		&file.Metadata,
		&file.ErrorMessage,
		&file.CreatedAt,
		&file.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get file: %w", err)
	}
	return file, nil
}

// GetByGoogleFileName retrieves a file by its Google file name.
func (r *FileRepository) GetByGoogleFileName(ctx context.Context, googleFileName string) (*File, error) {
	query := `
		SELECT id, store_id, file_name, public_url, mime_type, google_file_name, status, metadata, error_message, created_at, updated_at
		FROM files
		WHERE google_file_name = ?
	`
	file := &File{}
	err := r.db.QueryRowContext(ctx, query, googleFileName).Scan(
		&file.ID,
		&file.StoreID,
		&file.FileName,
		&file.PublicURL,
		&file.MimeType,
		&file.GoogleFileName,
		&file.Status,
		&file.Metadata,
		&file.ErrorMessage,
		&file.CreatedAt,
		&file.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get file: %w", err)
	}
	return file, nil
}

// ListByStoreID lists all files for a given store.
func (r *FileRepository) ListByStoreID(ctx context.Context, storeID string) ([]*File, error) {
	query := `
		SELECT id, store_id, file_name, public_url, mime_type, google_file_name, status, metadata, error_message, created_at, updated_at
		FROM files
		WHERE store_id = ?
		ORDER BY created_at DESC
	`
	rows, err := r.db.QueryContext(ctx, query, storeID)
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}
	defer rows.Close()

	var files []*File
	for rows.Next() {
		file := &File{}
		if err := rows.Scan(
			&file.ID,
			&file.StoreID,
			&file.FileName,
			&file.PublicURL,
			&file.MimeType,
			&file.GoogleFileName,
			&file.Status,
			&file.Metadata,
			&file.ErrorMessage,
			&file.CreatedAt,
			&file.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan file: %w", err)
		}
		files = append(files, file)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating files: %w", err)
	}

	return files, nil
}

// Update updates a file record.
func (r *FileRepository) Update(ctx context.Context, file *File) error {
	file.UpdatedAt = time.Now()
	query := `
		UPDATE files
		SET status = ?, metadata = ?, error_message = ?, updated_at = ?
		WHERE id = ?
	`
	_, err := r.db.ExecContext(ctx, query,
		file.Status,
		file.Metadata,
		file.ErrorMessage,
		file.UpdatedAt,
		file.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update file: %w", err)
	}
	return nil
}

// Delete deletes a file by ID.
func (r *FileRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM files WHERE id = ?`
	_, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}

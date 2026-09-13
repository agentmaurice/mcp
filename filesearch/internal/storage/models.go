package storage

import "time"

// Store represents a Gemini FileSearch store.
type Store struct {
	ID              string
	DeploymentID    string
	GeminiStoreName string
	DisplayName     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// File represents an imported file in a store.
type File struct {
	ID              string
	StoreID         string
	FileName        string
	PublicURL       string
	MimeType        string
	GoogleFileName  string
	Status          string // pending, active, failed
	Metadata        string // JSON
	ErrorMessage    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SearchLog represents a search query log entry.
type SearchLog struct {
	ID              string
	StoreID         string
	Query           string
	ResultsCount    int
	ExecutionTimeMs int
	CreatedAt       time.Time
}

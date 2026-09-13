package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Credentials represents stored MCP credentials.
type Credentials struct {
	MCPID    string `json:"mcp_id"`
	APIKey   string `json:"api_key"`
	TenantID string `json:"tenant_id,omitempty"`
	SavedAt  string `json:"saved_at"`
}

// Store handles persistence of credentials to the filesystem.
type Store struct {
	path string
}

// NewStore creates a new credential store.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Save saves credentials to disk.
func (s *Store) Save(creds *Credentials) error {
	// Ensure the directory exists
	dir := s.path
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// Update saved timestamp
	creds.SavedAt = time.Now().UTC().Format(time.RFC3339)

	// Marshal to JSON
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	// Write to file with restricted permissions
	credFile := s.credentialsFile()
	if err := os.WriteFile(credFile, data, 0600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	return nil
}

// Load loads credentials from disk.
func (s *Store) Load() (*Credentials, error) {
	data, err := os.ReadFile(s.credentialsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("credentials not found")
		}
		return nil, fmt.Errorf("read credentials: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	return &creds, nil
}

// Exists checks if credentials exist.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.credentialsFile())
	return err == nil
}

// Delete removes stored credentials.
func (s *Store) Delete() error {
	credFile := s.credentialsFile()
	if err := os.Remove(credFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete credentials: %w", err)
	}
	return nil
}

// credentialsFile returns the path to the credentials file.
func (s *Store) credentialsFile() string {
	return filepath.Join(s.path, "credentials.json")
}

// Path returns the credentials directory path.
func (s *Store) Path() string {
	return s.path
}

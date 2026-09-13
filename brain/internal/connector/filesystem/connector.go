package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/connector"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
)

// Config holds filesystem connector configuration.
type Config struct {
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"` // glob patterns to include
	Exclude []string `json:"exclude,omitempty"` // glob patterns to exclude
}

// Connector implements the filesystem data source connector.
type Connector struct {
}

// New creates a new filesystem connector.
func New() *Connector {
	return &Connector{}
}

func (c *Connector) Type() string { return "filesystem" }

func (c *Connector) ListFiles(ctx context.Context, cfg json.RawMessage) ([]shared.SourceFile, error) {
	var config Config
	if err := json.Unmarshal(cfg, &config); err != nil {
		return nil, fmt.Errorf("invalid filesystem config: %w", err)
	}

	if config.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	var files []shared.SourceFile

	err := filepath.WalkDir(config.Path, func(path string, d os.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}

		// Skip directories
		if d.IsDir() {
			name := d.Name()
			// Skip hidden and common non-useful directories
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		// Get relative path
		relPath, err := filepath.Rel(config.Path, path)
		if err != nil {
			return err
		}

		// Check exclude patterns
		if shouldExclude(relPath, config.Exclude) {
			return nil
		}

		// Check include patterns (if specified)
		if len(config.Include) > 0 && !shouldInclude(relPath, config.Include) {
			return nil
		}

		// Skip binary and large files
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > 1024*1024 { // Skip files > 1MB
			return nil
		}

		// Check if it's a text file we can index
		lang := connector.DetectLanguage(path)
		docType := connector.DetectDocType(path)
		if docType == "" {
			return nil
		}

		// Compute content hash
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(content)

		files = append(files, shared.SourceFile{
			Path:        relPath,
			ContentHash: hex.EncodeToString(hash[:]),
			Size:        info.Size(),
			Language:    lang,
			DocType:     docType,
		})

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("walk directory failed: %w", err)
	}

	return files, nil
}

func (c *Connector) ReadFile(ctx context.Context, cfg json.RawMessage, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var config Config
	if err := json.Unmarshal(cfg, &config); err != nil {
		return nil, fmt.Errorf("invalid filesystem config: %w", err)
	}
	if config.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	cleanPath := filepath.Clean(path)
	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("file path escapes source root")
	}
	fullPath := filepath.Join(config.Path, cleanPath)
	return os.ReadFile(fullPath)
}

func (c *Connector) DetectChanges(ctx context.Context, current []shared.SourceFile, indexed []shared.Document) (added, modified, deleted []shared.SourceFile) {
	indexedMap := make(map[string]shared.Document, len(indexed))
	for _, d := range indexed {
		indexedMap[d.FilePath] = d
	}

	currentMap := make(map[string]struct{}, len(current))
	for _, f := range current {
		currentMap[f.Path] = struct{}{}
		doc, exists := indexedMap[f.Path]
		if !exists {
			added = append(added, f)
		} else if doc.ContentHash != f.ContentHash {
			modified = append(modified, f)
		}
	}

	for path, doc := range indexedMap {
		if _, exists := currentMap[path]; !exists {
			deleted = append(deleted, shared.SourceFile{
				Path:        path,
				ContentHash: doc.ContentHash,
			})
		}
	}

	return added, modified, deleted
}

func shouldExclude(path string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, _ := filepath.Match(pattern, path)
		if matched {
			return true
		}
		// Also check if any path component matches
		if strings.Contains(path, strings.TrimPrefix(strings.TrimSuffix(pattern, "/**"), "**/")) {
			return true
		}
	}
	return false
}

func shouldInclude(path string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, _ := filepath.Match(pattern, filepath.Base(path))
		if matched {
			return true
		}
	}
	return false
}

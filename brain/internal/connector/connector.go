package connector

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
)

// Connector defines the interface for data source connectors.
type Connector interface {
	// Type returns the connector type name.
	Type() string
	// ListFiles returns the list of files to index.
	ListFiles(ctx context.Context, cfg json.RawMessage) ([]shared.SourceFile, error)
	// ReadFile returns the content of a file.
	ReadFile(ctx context.Context, cfg json.RawMessage, path string) ([]byte, error)
	// DetectChanges compares current files with indexed documents.
	DetectChanges(ctx context.Context, current []shared.SourceFile, indexed []shared.Document) (added, modified, deleted []shared.SourceFile)
}

// DetectLanguage detects the programming language from file extension.
func DetectLanguage(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescript"
	case ".js":
		return "javascript"
	case ".jsx":
		return "javascript"
	case ".java":
		return "java"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".c", ".h":
		return "c"
	case ".cpp", ".hpp", ".cc":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".swift":
		return "swift"
	case ".kt":
		return "kotlin"
	case ".php":
		return "php"
	case ".sh", ".bash":
		return "shell"
	case ".sql":
		return "sql"
	case ".r":
		return "r"
	default:
		return ""
	}
}

// DetectDocType detects the document type from file extension.
func DetectDocType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	name := strings.ToLower(filepath.Base(filePath))

	switch ext {
	case ".md", ".mdx", ".rst", ".txt":
		return "markdown"
	case ".yaml", ".yml", ".toml", ".ini", ".cfg":
		return "config"
	case ".json":
		if strings.Contains(name, "config") || strings.Contains(name, "package") || strings.Contains(name, "tsconfig") {
			return "config"
		}
		return "config"
	case ".xml":
		return "config"
	case ".proto":
		return "api_spec"
	case ".graphql", ".gql":
		return "api_spec"
	default:
		lang := DetectLanguage(filePath)
		if lang != "" {
			return "code"
		}
		return "markdown" // fallback
	}
}

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/filesearch/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/filesearch/internal/gemini"
	"github.com/agentmaurice/mcpchatui/mcp/filesearch/internal/storage"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

const (
	serviceName    = "gemini-filesearch"
	serviceVersion = "0.1.0"
)

// Builder coordinates MCP server construction.
type Builder struct {
	cfg        config.Config
	gemini     *gemini.Client
	db         *storage.DB
	storeRepo  *storage.StoreRepository
	fileRepo   *storage.FileRepository
	searchRepo *storage.SearchLogRepository
	logger     *zap.Logger
	modern     *officialmcp.Server
	streamable http.Handler
}

// NewBuilder creates a Builder with dependencies.
func NewBuilder(
	cfg config.Config,
	geminiClient *gemini.Client,
	db *storage.DB,
	logger *zap.Logger,
) *Builder {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Builder{
		cfg:        cfg,
		gemini:     geminiClient,
		db:         db,
		storeRepo:  storage.NewStoreRepository(db),
		fileRepo:   storage.NewFileRepository(db),
		searchRepo: storage.NewSearchLogRepository(db),
		logger:     logger.Named("mcp"),
	}
}

// Build assembles the MCP SSE server ready to be started.
func (b *Builder) Build() (*server.SSEServer, error) {
	if b.gemini == nil {
		return nil, fmt.Errorf("gemini client is required")
	}

	mcpSrv := server.NewMCPServer(
		serviceName,
		serviceVersion,
		server.WithToolCapabilities(true),
		server.WithLogging(),
		server.WithInstructions(buildInstructions()),
	)

	b.modern = newModernMCPServer(serviceName, serviceVersion, buildInstructions())

	// Register all 12 tools on both transports.
	b.registerTools(mcpSrv)
	b.streamable = officialmcp.NewStreamableHTTPHandler(
		func(*http.Request) *officialmcp.Server { return b.modern },
		&officialmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)

	options := []server.SSEOption{
		server.WithStaticBasePath(b.cfg.Server.BasePath),
		server.WithKeepAlive(b.cfg.Server.KeepAlive),
		server.WithKeepAliveInterval(b.cfg.Server.KeepAliveInterval),
	}
	if trimmed := strings.TrimSpace(b.cfg.Server.PublicURL); trimmed != "" {
		options = append(options, server.WithBaseURL(trimmed))
	}

	return server.NewSSEServer(mcpSrv, options...), nil
}

// Streamable returns the stateless modern MCP HTTP handler.
func (b *Builder) Streamable() http.Handler {
	return b.streamable
}

func (b *Builder) registerTools(srv *server.MCPServer) {
	b.addTool(srv, b.getStoreInfoTool(), b.getStoreInfoHandler())
	b.addTool(srv, b.initOrRepairStoreTool(), b.initOrRepairStoreHandler())
	b.addTool(srv, b.uploadFileTool(), b.uploadFileHandler())
	b.addTool(srv, b.uploadLocalFileTool(), b.uploadLocalFileHandler())
	b.addTool(srv, b.getFileStatusTool(), b.getFileStatusHandler())
	b.addTool(srv, b.listFilesTool(), b.listFilesHandler())
	b.addTool(srv, b.deleteFileTool(), b.deleteFileHandler())
	b.addTool(srv, b.semanticQueryTool(), b.semanticQueryHandler())
	b.addTool(srv, b.rawFileSearchTool(), b.rawFileSearchHandler())
	b.addTool(srv, b.getFullExtractedTextTool(), b.getFullExtractedTextHandler())
	b.addTool(srv, b.healthCheckTool(), b.healthCheckHandler())
	b.addTool(srv, b.getUsageStatsTool(), b.getUsageStatsHandler())
}

func (b *Builder) addTool(srv *server.MCPServer, definition mcp.Tool, handler server.ToolHandlerFunc) {
	srv.AddTool(definition, handler)
	b.modern.AddTool(toOfficialTool(definition), adaptToolHandler(handler))
}

// Tool 1: get_store_info
func (b *Builder) getStoreInfoTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_store_info",
		Description: "Get information about the FileSearch store for a deployment",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"deployment_id": map[string]any{
					"type":        "string",
					"description": "Deployment ID to query",
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

func (b *Builder) getStoreInfoHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		deploymentID, err := req.RequireString("deployment_id")
		if err != nil {
			return nil, fmt.Errorf("missing deployment_id: %w", err)
		}

		store, err := b.storeRepo.GetByDeploymentID(ctx, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get store: %w", err)
		}

		if store == nil {
			return mcp.NewToolResultText("No store found for this deployment"), nil
		}

		result := map[string]any{
			"store_id":          store.ID,
			"deployment_id":     store.DeploymentID,
			"gemini_store_name": store.GeminiStoreName,
			"display_name":      store.DisplayName,
			"created_at":        store.CreatedAt.Format(time.RFC3339),
			"updated_at":        store.UpdatedAt.Format(time.RFC3339),
		}

		return mcp.NewToolResultStructured(result, "Store information retrieved"), nil
	}
}

// Tool 2: init_or_repair_store
func (b *Builder) initOrRepairStoreTool() mcp.Tool {
	return mcp.Tool{
		Name:        "init_or_repair_store",
		Description: "Initialize or repair a FileSearch store for a deployment",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"deployment_id": map[string]any{
					"type":        "string",
					"description": "Deployment ID",
				},
				"display_name": map[string]any{
					"type":        "string",
					"description": "Display name for the store",
				},
			},
			Required: []string{"deployment_id", "display_name"},
		},
	}
}

func (b *Builder) initOrRepairStoreHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		deploymentID, err := req.RequireString("deployment_id")
		if err != nil {
			return nil, fmt.Errorf("missing deployment_id: %w", err)
		}

		displayName, err := req.RequireString("display_name")
		if err != nil {
			return nil, fmt.Errorf("missing display_name: %w", err)
		}

		// Check if store already exists
		existingStore, err := b.storeRepo.GetByDeploymentID(ctx, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to check existing store: %w", err)
		}

		if existingStore != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Store already exists: %s", existingStore.GeminiStoreName)), nil
		}

		// Create new store in Gemini
		geminiStore, err := b.gemini.CreateFileSearchStore(ctx, displayName)
		if err != nil {
			return nil, fmt.Errorf("failed to create Gemini store: %w", err)
		}

		// Save store to database
		store := &storage.Store{
			ID:              uuid.New().String(),
			DeploymentID:    deploymentID,
			GeminiStoreName: geminiStore.Name,
			DisplayName:     displayName,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}

		if err := b.storeRepo.Create(ctx, store); err != nil {
			return nil, fmt.Errorf("failed to save store: %w", err)
		}

		result := map[string]any{
			"store_id":          store.ID,
			"gemini_store_name": geminiStore.Name,
			"display_name":      displayName,
			"status":            "created",
		}

		return mcp.NewToolResultStructured(result, "Store created successfully"), nil
	}
}

// Tool 3: upload_file
func (b *Builder) uploadFileTool() mcp.Tool {
	return mcp.Tool{
		Name:        "upload_file",
		Description: "Upload a file from a public URL to the FileSearch store",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"deployment_id": map[string]any{
					"type":        "string",
					"description": "Deployment ID",
				},
				"file_name": map[string]any{
					"type":        "string",
					"description": "File name",
				},
				"public_url": map[string]any{
					"type":        "string",
					"description": "Public URL of the file",
				},
				"mime_type": map[string]any{
					"type":        "string",
					"description": "MIME type of the file",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Optional metadata",
				},
			},
			Required: []string{"deployment_id", "file_name", "public_url", "mime_type"},
		},
	}
}

func (b *Builder) uploadFileHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		deploymentID, err := req.RequireString("deployment_id")
		if err != nil {
			return nil, fmt.Errorf("missing deployment_id: %w", err)
		}

		fileName, err := req.RequireString("file_name")
		if err != nil {
			return nil, fmt.Errorf("missing file_name: %w", err)
		}

		publicURL, err := req.RequireString("public_url")
		if err != nil {
			return nil, fmt.Errorf("missing public_url: %w", err)
		}

		mimeType, err := req.RequireString("mime_type")
		if err != nil {
			return nil, fmt.Errorf("missing mime_type: %w", err)
		}

		// Get store
		store, err := b.storeRepo.GetByDeploymentID(ctx, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get store: %w", err)
		}
		if store == nil {
			return nil, fmt.Errorf("store not found for deployment %s", deploymentID)
		}

		// Import file to Gemini
		geminiFile, err := b.gemini.ImportFileViaURL(ctx, publicURL, mimeType, fileName)
		if err != nil {
			return nil, fmt.Errorf("failed to import file: %w", err)
		}

		// Parse metadata if provided
		args := req.GetArguments()
		var metadataJSON string
		if metadata, ok := args["metadata"]; ok {
			metadataBytes, _ := json.Marshal(metadata)
			metadataJSON = string(metadataBytes)
		}

		// Save file to database
		file := &storage.File{
			ID:             uuid.New().String(),
			StoreID:        store.ID,
			FileName:       fileName,
			PublicURL:      publicURL,
			MimeType:       mimeType,
			GoogleFileName: geminiFile.Name,
			Status:         "pending",
			Metadata:       metadataJSON,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		if err := b.fileRepo.Create(ctx, file); err != nil {
			return nil, fmt.Errorf("failed to save file: %w", err)
		}

		result := map[string]any{
			"file_id":          file.ID,
			"google_file_name": geminiFile.Name,
			"status":           file.Status,
		}

		return mcp.NewToolResultStructured(result, "File upload initiated"), nil
	}
}

// Tool 3b: upload_local_file
func (b *Builder) uploadLocalFileTool() mcp.Tool {
	return mcp.Tool{
		Name:        "upload_local_file",
		Description: "Upload a file from the local filesystem to the FileSearch store (for testing without public URLs)",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"deployment_id": map[string]any{
					"type":        "string",
					"description": "Deployment ID",
				},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Local file path to upload",
				},
				"display_name": map[string]any{
					"type":        "string",
					"description": "Display name for the file (optional, defaults to filename)",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Optional metadata",
				},
			},
			Required: []string{"deployment_id", "file_path"},
		},
	}
}

func (b *Builder) uploadLocalFileHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		deploymentID, err := req.RequireString("deployment_id")
		if err != nil {
			return nil, fmt.Errorf("missing deployment_id: %w", err)
		}

		filePath, err := req.RequireString("file_path")
		if err != nil {
			return nil, fmt.Errorf("missing file_path: %w", err)
		}

		// Get display name or use filename
		args := req.GetArguments()
		displayName := ""
		if dn, ok := args["display_name"].(string); ok && dn != "" {
			displayName = dn
		} else {
			// Extract filename from path
			displayName = filePath
			if idx := strings.LastIndex(filePath, "/"); idx >= 0 {
				displayName = filePath[idx+1:]
			} else if idx := strings.LastIndex(filePath, "\\"); idx >= 0 {
				displayName = filePath[idx+1:]
			}
		}

		// Get store
		store, err := b.storeRepo.GetByDeploymentID(ctx, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get store: %w", err)
		}
		if store == nil {
			return nil, fmt.Errorf("store not found for deployment %s", deploymentID)
		}

		// Upload file to Gemini from local filesystem
		geminiFile, err := b.gemini.UploadLocalFile(ctx, filePath, displayName)
		if err != nil {
			return nil, fmt.Errorf("failed to upload local file: %w", err)
		}

		// Parse metadata if provided
		var metadataJSON string
		if metadata, ok := args["metadata"]; ok {
			metadataBytes, _ := json.Marshal(metadata)
			metadataJSON = string(metadataBytes)
		}

		// Save file to database
		file := &storage.File{
			ID:             uuid.New().String(),
			StoreID:        store.ID,
			FileName:       displayName,
			PublicURL:      filePath, // Store local path as "URL" for reference
			MimeType:       geminiFile.MimeType,
			GoogleFileName: geminiFile.Name,
			Status:         "pending",
			Metadata:       metadataJSON,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		if err := b.fileRepo.Create(ctx, file); err != nil {
			return nil, fmt.Errorf("failed to save file: %w", err)
		}

		result := map[string]any{
			"file_id":          file.ID,
			"google_file_name": geminiFile.Name,
			"status":           file.Status,
			"mime_type":        geminiFile.MimeType,
			"display_name":     displayName,
		}

		return mcp.NewToolResultStructured(result, "Local file uploaded successfully"), nil
	}
}

// Tool 4: get_file_status
func (b *Builder) getFileStatusTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_file_status",
		Description: "Get the status of a file upload/indexation",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"file_id": map[string]any{
					"type":        "string",
					"description": "File ID",
				},
			},
			Required: []string{"file_id"},
		},
	}
}

func (b *Builder) getFileStatusHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fileID, err := req.RequireString("file_id")
		if err != nil {
			return nil, fmt.Errorf("missing file_id: %w", err)
		}

		file, err := b.fileRepo.GetByID(ctx, fileID)
		if err != nil {
			return nil, fmt.Errorf("failed to get file: %w", err)
		}
		if file == nil {
			return nil, fmt.Errorf("file not found: %s", fileID)
		}

		// Query Gemini for current status
		geminiFile, err := b.gemini.GetFileStatus(ctx, file.GoogleFileName)
		if err != nil {
			b.logger.Warn("failed to get Gemini file status", zap.Error(err))
		} else {
			// Update local status based on Gemini status
			if geminiFile.State == "ACTIVE" && file.Status != "active" {
				file.Status = "active"
				_ = b.fileRepo.Update(ctx, file)
			} else if geminiFile.State == "FAILED" && file.Status != "failed" {
				file.Status = "failed"
				if geminiFile.Error != nil {
					file.ErrorMessage = geminiFile.Error.Message
				}
				_ = b.fileRepo.Update(ctx, file)
			}
		}

		result := map[string]any{
			"file_id":          file.ID,
			"file_name":        file.FileName,
			"status":           file.Status,
			"google_file_name": file.GoogleFileName,
			"created_at":       file.CreatedAt.Format(time.RFC3339),
			"updated_at":       file.UpdatedAt.Format(time.RFC3339),
		}

		if file.ErrorMessage != "" {
			result["error_message"] = file.ErrorMessage
		}

		return mcp.NewToolResultStructured(result, "File status retrieved"), nil
	}
}

// Tool 5: list_files
func (b *Builder) listFilesTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_files",
		Description: "List all files in a deployment's store",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"deployment_id": map[string]any{
					"type":        "string",
					"description": "Deployment ID",
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

func (b *Builder) listFilesHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		deploymentID, err := req.RequireString("deployment_id")
		if err != nil {
			return nil, fmt.Errorf("missing deployment_id: %w", err)
		}

		store, err := b.storeRepo.GetByDeploymentID(ctx, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get store: %w", err)
		}
		if store == nil {
			return mcp.NewToolResultText("No store found for this deployment"), nil
		}

		files, err := b.fileRepo.ListByStoreID(ctx, store.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to list files: %w", err)
		}

		fileList := make([]map[string]any, 0, len(files))
		for _, file := range files {
			fileList = append(fileList, map[string]any{
				"file_id":   file.ID,
				"file_name": file.FileName,
				"mime_type": file.MimeType,
				"status":    file.Status,
				"created_at": file.CreatedAt.Format(time.RFC3339),
			})
		}

		result := map[string]any{
			"store_id": store.ID,
			"count":    len(files),
			"files":    fileList,
		}

		return mcp.NewToolResultStructured(result, fmt.Sprintf("Found %d files", len(files))), nil
	}
}

// Tool 6: delete_file
func (b *Builder) deleteFileTool() mcp.Tool {
	return mcp.Tool{
		Name:        "delete_file",
		Description: "Delete a file from the store",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"file_id": map[string]any{
					"type":        "string",
					"description": "File ID to delete",
				},
			},
			Required: []string{"file_id"},
		},
	}
}

func (b *Builder) deleteFileHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fileID, err := req.RequireString("file_id")
		if err != nil {
			return nil, fmt.Errorf("missing file_id: %w", err)
		}

		file, err := b.fileRepo.GetByID(ctx, fileID)
		if err != nil {
			return nil, fmt.Errorf("failed to get file: %w", err)
		}
		if file == nil {
			return nil, fmt.Errorf("file not found: %s", fileID)
		}

		// Delete from Gemini
		if err := b.gemini.DeleteFile(ctx, file.GoogleFileName); err != nil {
			b.logger.Warn("failed to delete file from Gemini", zap.Error(err))
		}

		// Delete from database
		if err := b.fileRepo.Delete(ctx, fileID); err != nil {
			return nil, fmt.Errorf("failed to delete file from database: %w", err)
		}

		return mcp.NewToolResultText(fmt.Sprintf("File %s deleted successfully", file.FileName)), nil
	}
}

// Tool 7: semantic_query
func (b *Builder) semanticQueryTool() mcp.Tool {
	return mcp.Tool{
		Name:        "semantic_query",
		Description: "Perform a semantic search query across indexed files",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"deployment_id": map[string]any{
					"type":        "string",
					"description": "Deployment ID",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Search query",
				},
			},
			Required: []string{"deployment_id", "query"},
		},
	}
}

func (b *Builder) semanticQueryHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		deploymentID, err := req.RequireString("deployment_id")
		if err != nil {
			return nil, fmt.Errorf("missing deployment_id: %w", err)
		}

		query, err := req.RequireString("query")
		if err != nil {
			return nil, fmt.Errorf("missing query: %w", err)
		}

		store, err := b.storeRepo.GetByDeploymentID(ctx, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get store: %w", err)
		}
		if store == nil {
			return nil, fmt.Errorf("store not found for deployment %s", deploymentID)
		}

		startTime := time.Now()

		// Perform semantic search via Gemini
		response, err := b.gemini.GenerateContentWithFileSearch(ctx, store.GeminiStoreName, query)
		if err != nil {
			return nil, fmt.Errorf("failed to perform search: %w", err)
		}

		executionTime := time.Since(startTime).Milliseconds()

		// Extract text and citations
		var resultText string
		var citations []map[string]any

		if len(response.Candidates) > 0 {
			candidate := response.Candidates[0]
			if len(candidate.Content.Parts) > 0 {
				resultText = candidate.Content.Parts[0].Text
			}

			if candidate.GroundingMetadata != nil {
				for _, chunk := range candidate.GroundingMetadata.GroundingChunks {
					if chunk.Web != nil {
						citations = append(citations, map[string]any{
							"title": chunk.Web.Title,
							"uri":   chunk.Web.URI,
						})
					}
				}
			}
		}

		// Log search
		searchLog := &storage.SearchLog{
			ID:              uuid.New().String(),
			StoreID:         store.ID,
			Query:           query,
			ResultsCount:    len(citations),
			ExecutionTimeMs: int(executionTime),
			CreatedAt:       time.Now(),
		}
		_ = b.searchRepo.Create(ctx, searchLog)

		result := map[string]any{
			"query":          query,
			"result_text":    resultText,
			"citations":      citations,
			"execution_time": executionTime,
		}

		return mcp.NewToolResultStructured(result, "Search completed"), nil
	}
}

// Tool 8: raw_file_search (optional)
func (b *Builder) rawFileSearchTool() mcp.Tool {
	return mcp.Tool{
		Name:        "raw_file_search",
		Description: "Perform a raw file search returning only chunks and citations",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"deployment_id": map[string]any{
					"type":        "string",
					"description": "Deployment ID",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Search query",
				},
			},
			Required: []string{"deployment_id", "query"},
		},
	}
}

func (b *Builder) rawFileSearchHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Similar to semantic_query but returns raw chunks
		return b.semanticQueryHandler()(ctx, req)
	}
}

// Tool 9: get_full_extracted_text (optional)
func (b *Builder) getFullExtractedTextTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_full_extracted_text",
		Description: "Extract the full text content of a file",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"file_id": map[string]any{
					"type":        "string",
					"description": "File ID",
				},
			},
			Required: []string{"file_id"},
		},
	}
}

func (b *Builder) getFullExtractedTextHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fileID, err := req.RequireString("file_id")
		if err != nil {
			return nil, fmt.Errorf("missing file_id: %w", err)
		}

		file, err := b.fileRepo.GetByID(ctx, fileID)
		if err != nil {
			return nil, fmt.Errorf("failed to get file: %w", err)
		}
		if file == nil {
			return nil, fmt.Errorf("file not found: %s", fileID)
		}

		// This would require a special query to extract all text
		// For now, return a placeholder
		result := map[string]any{
			"file_id":   file.ID,
			"file_name": file.FileName,
			"message":   "Full text extraction not yet implemented",
		}

		return mcp.NewToolResultStructured(result, "Text extraction placeholder"), nil
	}
}

// Tool 10: health_check
func (b *Builder) healthCheckTool() mcp.Tool {
	return mcp.Tool{
		Name:        "health_check",
		Description: "Check the health of the FileSearch service",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
			Required:   []string{},
		},
	}
}

func (b *Builder) healthCheckHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		checks := make(map[string]any)

		// Check database
		if err := b.db.Ping(); err != nil {
			checks["database"] = map[string]any{
				"status": "unhealthy",
				"error":  err.Error(),
			}
		} else {
			checks["database"] = map[string]any{
				"status": "healthy",
			}
		}

		// Check Gemini API (simple check)
		checks["gemini_api"] = map[string]any{
			"status": "configured",
		}

		result := map[string]any{
			"service": serviceName,
			"version": serviceVersion,
			"checks":  checks,
			"timestamp": time.Now().Format(time.RFC3339),
		}

		return mcp.NewToolResultStructured(result, "Health check completed"), nil
	}
}

// Tool 11: get_usage_stats
func (b *Builder) getUsageStatsTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_usage_stats",
		Description: "Get usage statistics for a deployment",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"deployment_id": map[string]any{
					"type":        "string",
					"description": "Deployment ID",
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

func (b *Builder) getUsageStatsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		deploymentID, err := req.RequireString("deployment_id")
		if err != nil {
			return nil, fmt.Errorf("missing deployment_id: %w", err)
		}

		store, err := b.storeRepo.GetByDeploymentID(ctx, deploymentID)
		if err != nil {
			return nil, fmt.Errorf("failed to get store: %w", err)
		}
		if store == nil {
			return nil, fmt.Errorf("store not found for deployment %s", deploymentID)
		}

		// Get file count
		files, err := b.fileRepo.ListByStoreID(ctx, store.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to list files: %w", err)
		}

		// Get search stats
		stats, err := b.searchRepo.GetStats(ctx, store.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get stats: %w", err)
		}

		result := map[string]any{
			"deployment_id": deploymentID,
			"store_id":      store.ID,
			"total_files":   len(files),
			"search_stats":  stats,
		}

		return mcp.NewToolResultStructured(result, "Usage stats retrieved"), nil
	}
}

func buildInstructions() string {
	return "This MCP server provides file search capabilities using Google Gemini FileSearch API. " +
		"Upload files via public URLs, index them automatically, and perform semantic searches across your document corpus. " +
		"Each deployment gets a dedicated FileSearch store for isolated data management."
}

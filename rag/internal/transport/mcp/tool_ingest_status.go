package mcp

import (
	"context"
	"encoding/json"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/business"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// IngestStatusTool implements the rag_ingest_status MCP tool
type IngestStatusTool struct {
	ingestManager *business.IngestManager
	logger        *zap.Logger
}

// NewIngestStatusTool creates a new ingest status tool
func NewIngestStatusTool(ingestManager *business.IngestManager, logger *zap.Logger) *IngestStatusTool {
	return &IngestStatusTool{
		ingestManager: ingestManager,
		logger:        logger.Named("ingest-status-tool"),
	}
}

// Definition returns the tool definition
func (t *IngestStatusTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_ingest_status",
		Description: "Get the status of a document ingestion job",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"job_id": map[string]interface{}{
					"type":        "string",
					"description": "Ingestion job ID",
				},
			},
			Required: []string{"job_id"},
		},
	}
}

// Handler returns the tool handler function
func (t *IngestStatusTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("handling rag_ingest_status", zap.Any("arguments", request.Params.Arguments))

		// Parse arguments
		var args struct {
			JobID string `json:"job_id"`
		}

		argsBytes, err := json.Marshal(request.Params.Arguments)
		if err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		// Validate
		if args.JobID == "" {
			return errorResult("job_id is required"), nil
		}

		// Parse job ID
		jobID, err := xid.FromString(args.JobID)
		if err != nil {
			return errorResult("invalid job_id format"), nil
		}

		// Get job status
		job, err := t.ingestManager.GetJobStatus(ctx, jobID)
		if err != nil {
			t.logger.Error("failed to get job status", zap.Error(err))
			return errorResult("failed to get job status: " + err.Error()), nil
		}

		// Build response
		response := map[string]interface{}{
			"job_id":   job.ID.String(),
			"status":   string(job.Status),
			"progress": job.Progress,
			"message":  job.Message,
		}

		responseBytes, _ := json.Marshal(response)

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: "text",
					Text: string(responseBytes),
				},
			},
		}, nil
	}
}

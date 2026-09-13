package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type PrivateIngestTool struct {
	storage storage.Manager
	cfg     *config.Config
	logger  *zap.Logger
}

func NewPrivateIngestTool(storage storage.Manager, cfg *config.Config, logger *zap.Logger) *PrivateIngestTool {
	return &PrivateIngestTool{storage: storage, cfg: cfg, logger: logger.Named("memory.private_ingest")}
}

func (t *PrivateIngestTool) Name() string {
	return "memory.private_ingest"
}

func (t *PrivateIngestTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.private_ingest",
		Description: "Sanitize a fact, entity, or document payload before persistence, then store an ingest receipt.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{"type": "string"},
				"kind": map[string]interface{}{
					"type": "string",
					"enum": []string{"fact", "entity", "document"},
				},
				"payload": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": true,
				},
				"privacy": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"mode": map[string]interface{}{
							"type": "string",
							"enum": []string{"redact", "hash", "drop_fields", "ephemeral"},
						},
						"field_paths": map[string]interface{}{
							"type":  "array",
							"items": map[string]interface{}{"type": "string"},
						},
						"tags": map[string]interface{}{
							"type":  "array",
							"items": map[string]interface{}{"type": "string"},
						},
						"store_raw": map[string]interface{}{
							"type":        "boolean",
							"description": "Accepted for compatibility; raw persistence remains disabled in V1.",
						},
					},
					"required": []string{"mode"},
				},
			},
			Required: []string{"kind", "payload", "privacy"},
		},
	}
}

func (t *PrivateIngestTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			TenantID string       `json:"tenant_id"`
			Kind     string       `json:"kind"`
			Payload  any          `json:"payload"`
			Privacy  privacyInput `json:"privacy"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		payloadMap, err := normalizePayloadObject(args.Payload)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		sanitizedPayload, redactedPaths, payloadHash, err := sanitizePrivatePayload(args.Kind, payloadMap, args.Privacy)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		targetIDs, err := t.dispatchSanitizedPayload(ctx, tenantID, args.Kind, sanitizedPayload)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		receiptID := ""
		err = t.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
			var err error
			receiptID, err = insertIngestReceipt(ctx, t.storage, q, tenantID, args.Kind, args.Privacy, redactedPaths, args.Kind, targetIDs, payloadHash)
			return err
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return structuredResult(map[string]any{
			"stored":         true,
			"kind":           args.Kind,
			"receipt_id":     receiptID,
			"target_ids":     targetIDs,
			"privacy_mode":   args.Privacy.Mode,
			"redacted_paths": redactedPaths,
			"raw_persisted":  false,
		}), nil
	}
}

func (t *PrivateIngestTool) dispatchSanitizedPayload(ctx context.Context, tenantID, kind string, payload map[string]any) ([]string, error) {
	payload["tenant_id"] = tenantID

	switch kind {
	case "fact":
		tool := NewAppendFactTool(t.storage, t.cfg, t.logger)
		result, err := tool.Handler()(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: tool.Name(), Arguments: payload},
		})
		if err != nil {
			return nil, err
		}
		if result == nil || result.IsError {
			return nil, fmt.Errorf("%s", extractToolError(result, "failed to persist fact"))
		}
		content, ok := result.StructuredContent.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unexpected fact response payload")
		}
		factID, _ := content["factId"].(string)
		if factID == "" {
			return nil, fmt.Errorf("missing factId from fact ingestion")
		}
		return []string{factID}, nil

	case "entity":
		var parsed struct {
			EntityType string `json:"entityType"`
			Items      []struct {
				ExternalID string `json:"externalId"`
			} `json:"items"`
		}
		if err := decodeMap(payload, &parsed); err != nil {
			return nil, err
		}
		if len(parsed.Items) != 1 {
			return nil, fmt.Errorf("entity payload must contain exactly one item in V1")
		}

		tool := NewUpsertEntitiesTool(t.storage, t.cfg, t.logger)
		result, err := tool.Handler()(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: tool.Name(), Arguments: payload},
		})
		if err != nil {
			return nil, err
		}
		if result == nil || result.IsError {
			return nil, fmt.Errorf("%s", extractToolError(result, "failed to persist entity"))
		}

		db, err := t.storage.OpenTenant(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		entityID, err := queryScalarString(ctx, db,
			rebindQuery(t.storage, "SELECT entity_id FROM entities WHERE entity_type = ? AND external_id = ? LIMIT 1"),
			parsed.EntityType,
			parsed.Items[0].ExternalID,
		)
		if err != nil {
			return nil, err
		}
		if entityID == "" {
			return nil, fmt.Errorf("entity persisted but entity_id could not be resolved")
		}
		return []string{entityID}, nil

	case "document":
		tool := NewAttachDocumentTool(t.storage, t.cfg, t.logger)
		result, err := tool.Handler()(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: tool.Name(), Arguments: payload},
		})
		if err != nil {
			return nil, err
		}
		if result == nil || result.IsError {
			return nil, fmt.Errorf("%s", extractToolError(result, "failed to persist document"))
		}
		content, ok := result.StructuredContent.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unexpected document response payload")
		}
		documentID, _ := content["documentId"].(string)
		if documentID == "" {
			return nil, fmt.Errorf("missing documentId from document ingestion")
		}
		return []string{documentID}, nil
	default:
		return nil, fmt.Errorf("kind must be one of fact, entity, document")
	}
}

func extractToolError(result *mcp.CallToolResult, fallback string) string {
	if result == nil {
		return fallback
	}
	if len(result.Content) == 0 {
		return fallback
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok || text.Text == "" {
		return fallback
	}
	return text.Text
}

func decodeMap(input map[string]any, target any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

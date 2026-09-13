package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// UpsertEntitiesTool implements memory.entities.upsert.
type UpsertEntitiesTool struct {
	storage storage.Manager
	cfg     *config.Config
	logger  *zap.Logger
}

func NewUpsertEntitiesTool(storage storage.Manager, cfg *config.Config, logger *zap.Logger) *UpsertEntitiesTool {
	return &UpsertEntitiesTool{storage: storage, cfg: cfg, logger: logger.Named("memory.entities.upsert")}
}

func (t *UpsertEntitiesTool) Name() string {
	return "memory.entities.upsert"
}

func (t *UpsertEntitiesTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.entities.upsert",
		Description: "Upsert generic entities with provenance",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"entityType": map[string]interface{}{
					"type":      "string",
					"minLength": 1,
					"maxLength": 128,
				},
				"items": map[string]interface{}{
					"type":     "array",
					"minItems": 1,
					"maxItems": 1000,
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"externalId": map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 256},
							"name":       map[string]interface{}{"type": "string"},
							"status":     map[string]interface{}{"type": "string"},
							"attributes": map[string]interface{}{"type": "object", "additionalProperties": true},
						},
						"required": []string{"externalId"},
					},
				},
				"source": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"system":    map[string]interface{}{"type": "string", "minLength": 1},
						"author":    map[string]interface{}{"type": "string"},
						"timestamp": map[string]interface{}{"type": "string", "description": "ISO-8601"},
						"traceId":   map[string]interface{}{"type": "string"},
						"raw":       map[string]interface{}{"type": "object", "additionalProperties": true},
					},
					"required": []string{"system", "timestamp"},
				},
				"writer": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"appId":     map[string]interface{}{"type": "string"},
						"actorId":   map[string]interface{}{"type": "string"},
						"actorType": map[string]interface{}{"type": "string", "enum": []string{"agent", "human", "system"}, "default": "agent"},
					},
					"required": []string{"appId", "actorId"},
				},
			},
			Required: []string{"entityType", "items", "source", "writer"},
		},
	}
}

func (t *UpsertEntitiesTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Info("tool called", zap.Any("arguments", request.Params.Arguments))

		var args struct {
			TenantID   string `json:"tenant_id"`
			EntityType string `json:"entityType"`
			Items      []struct {
				ExternalID string         `json:"externalId"`
				Name       string         `json:"name"`
				Status     string         `json:"status"`
				Attributes map[string]any `json:"attributes"`
			} `json:"items"`
			Source SourceInput `json:"source"`
			Writer WriterInput `json:"writer"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			t.logger.Warn("failed to parse arguments", zap.Error(err), zap.Any("raw_arguments", request.Params.Arguments))
			return errorResult("failed to parse arguments"), nil
		}

		t.logger.Debug("parsed arguments",
			zap.String("entity_type", args.EntityType),
			zap.Int("item_count", len(args.Items)),
		)

		if args.EntityType == "" || len(args.Items) == 0 {
			t.logger.Warn("missing required fields", zap.String("entity_type", args.EntityType), zap.Int("item_count", len(args.Items)))
			return errorResult("entityType and items are required"), nil
		}
		if args.Source.System == "" || args.Source.Timestamp == "" {
			t.logger.Warn("missing source fields", zap.String("system", args.Source.System), zap.String("timestamp", args.Source.Timestamp))
			return errorResult("source.system and source.timestamp are required"), nil
		}
		if args.Writer.AppID == "" || args.Writer.ActorID == "" {
			t.logger.Warn("missing writer fields", zap.String("app_id", args.Writer.AppID), zap.String("actor_id", args.Writer.ActorID))
			return errorResult("writer.appId and writer.actorId are required"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			t.logger.Warn("failed to resolve tenant ID", zap.Error(err))
			return errorResult(err.Error()), nil
		}

		t.logger.Debug("resolved tenant", zap.String("tenant_id", tenantID))

		timestamp, err := time.Parse(time.RFC3339, args.Source.Timestamp)
		if err != nil {
			t.logger.Warn("invalid source.timestamp format", zap.String("timestamp", args.Source.Timestamp), zap.Error(err))
			return errorResult("invalid source.timestamp"), nil
		}

		var created, updated int
		err = t.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
			sourceID, err := insertSource(ctx, t.storage, q, args.Source, timestamp)
			if err != nil {
				return err
			}
			writerID, err := ensureWriter(ctx, t.storage, q, args.Writer)
			if err != nil {
				return err
			}

			for _, item := range args.Items {
				if item.ExternalID == "" {
					return fmt.Errorf("externalId is required")
				}
				attributes := item.Attributes
				if attributes == nil {
					attributes = map[string]any{}
				}
				attrJSON, _ := json.Marshal(attributes)
				existingID, err := queryScalarString(ctx, q,
					rebindQuery(t.storage, "SELECT entity_id FROM entities WHERE entity_type = ? AND external_id = ?"),
					args.EntityType, item.ExternalID,
				)
				if err != nil {
					return err
				}
				if existingID == "" {
					entityID := xid.New().String()
					_, err = q.ExecContext(ctx,
						rebindQuery(t.storage, "INSERT INTO entities (entity_id, entity_type, external_id, name, status, attributes, last_source_id, last_writer_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"),
						entityID, args.EntityType, item.ExternalID, item.Name, item.Status, string(attrJSON), sourceID, writerID,
					)
					if err != nil {
						return err
					}
					created++
				} else {
					_, err = q.ExecContext(ctx,
						rebindQuery(t.storage, "UPDATE entities SET name = ?, status = ?, attributes = ?, updated_at = now(), last_source_id = ?, last_writer_id = ? WHERE entity_id = ?"),
						item.Name, item.Status, string(attrJSON), sourceID, writerID, existingID,
					)
					if err != nil {
						return err
					}
					updated++
				}
			}

			return nil
		})
		if err != nil {
			t.logger.Error("failed to upsert entities",
				zap.Error(err),
				zap.String("entity_type", args.EntityType),
				zap.String("tenant_id", tenantID),
				zap.Int("item_count", len(args.Items)),
			)
			return errorResult(err.Error()), nil
		}

		t.logger.Info("entities upserted successfully",
			zap.String("entity_type", args.EntityType),
			zap.Int("created", created),
			zap.Int("updated", updated),
		)

		resp := map[string]any{
			"upserted":   created + updated,
			"created":    created,
			"updated":    updated,
			"entityType": args.EntityType,
		}

		return structuredResult(resp), nil
	}
}

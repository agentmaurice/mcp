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

// AppendFactTool implements memory.facts.append.
type AppendFactTool struct {
	storage storage.Manager
	cfg     *config.Config
	logger  *zap.Logger
}

func NewAppendFactTool(storage storage.Manager, cfg *config.Config, logger *zap.Logger) *AppendFactTool {
	return &AppendFactTool{storage: storage, cfg: cfg, logger: logger.Named("memory.facts.append")}
}

func (t *AppendFactTool) Name() string {
	return "memory.facts.append"
}

func (t *AppendFactTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.facts.append",
		Description: "Append a business fact/event (append-only)",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"factType": map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 256},
				"subject": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"entityType": map[string]interface{}{"type": "string"},
						"externalId": map[string]interface{}{"type": "string"},
					},
				},
				"payload":      map[string]interface{}{"type": "object", "additionalProperties": true},
				"effectiveAt":  map[string]interface{}{"type": "string", "description": "ISO-8601"},
				"correlatesTo": map[string]interface{}{"type": "string"},
				"dedupeKey":    map[string]interface{}{"type": "string"},
				"source": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"system":    map[string]interface{}{"type": "string"},
						"author":    map[string]interface{}{"type": "string"},
						"timestamp": map[string]interface{}{"type": "string"},
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
			Required: []string{"factType", "payload", "effectiveAt", "source", "writer"},
		},
	}
}

func (t *AppendFactTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Info("tool called", zap.Any("arguments", request.Params.Arguments))

		var args struct {
			TenantID string `json:"tenant_id"`
			FactType string `json:"factType"`
			Subject  *struct {
				EntityType string `json:"entityType"`
				ExternalID string `json:"externalId"`
			} `json:"subject"`
			Payload      map[string]any `json:"payload"`
			EffectiveAt  string         `json:"effectiveAt"`
			CorrelatesTo string         `json:"correlatesTo"`
			DedupeKey    string         `json:"dedupeKey"`
			Source       SourceInput    `json:"source"`
			Writer       WriterInput    `json:"writer"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			t.logger.Warn("failed to parse arguments", zap.Error(err), zap.Any("raw_arguments", request.Params.Arguments))
			return errorResult("failed to parse arguments"), nil
		}

		t.logger.Debug("parsed arguments",
			zap.String("fact_type", args.FactType),
			zap.String("effective_at", args.EffectiveAt),
			zap.Any("subject", args.Subject),
			zap.String("dedupe_key", args.DedupeKey),
		)

		if args.FactType == "" || args.EffectiveAt == "" {
			t.logger.Warn("missing required fields", zap.String("fact_type", args.FactType), zap.String("effective_at", args.EffectiveAt))
			return errorResult("factType and effectiveAt are required"), nil
		}
		if args.Source.System == "" || args.Source.Timestamp == "" {
			t.logger.Warn("missing source fields", zap.String("system", args.Source.System), zap.String("timestamp", args.Source.Timestamp))
			return errorResult("source.system and source.timestamp are required"), nil
		}
		if args.Writer.AppID == "" || args.Writer.ActorID == "" {
			t.logger.Warn("missing writer fields", zap.String("app_id", args.Writer.AppID), zap.String("actor_id", args.Writer.ActorID))
			return errorResult("writer.appId and writer.actorId are required"), nil
		}

		effectiveAt, err := time.Parse(time.RFC3339, args.EffectiveAt)
		if err != nil {
			t.logger.Warn("invalid effectiveAt format", zap.String("effective_at", args.EffectiveAt), zap.Error(err))
			return errorResult("invalid effectiveAt"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			t.logger.Warn("failed to resolve tenant ID", zap.Error(err))
			return errorResult(err.Error()), nil
		}

		t.logger.Debug("resolved tenant", zap.String("tenant_id", tenantID))

		var factID string
		var deduped bool
		err = t.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
			sourceTime, err := time.Parse(time.RFC3339, args.Source.Timestamp)
			if err != nil {
				return fmt.Errorf("invalid source.timestamp")
			}
			sourceID, err := insertSource(ctx, t.storage, q, args.Source, sourceTime)
			if err != nil {
				return err
			}
			writerID, err := ensureWriter(ctx, t.storage, q, args.Writer)
			if err != nil {
				return err
			}

			if args.DedupeKey != "" {
				existing, err := queryScalarString(ctx, q, rebindQuery(t.storage, "SELECT fact_id FROM facts WHERE dedupe_key = ?"), args.DedupeKey)
				if err != nil {
					return err
				}
				if existing != "" {
					factID = existing
					deduped = true
					return nil
				}
			}

			var subjectEntityID, subjectRefJSON any
			if args.Subject != nil && args.Subject.EntityType != "" && args.Subject.ExternalID != "" {
				entityID, err := queryScalarString(ctx, q,
					rebindQuery(t.storage, "SELECT entity_id FROM entities WHERE entity_type = ? AND external_id = ?"),
					args.Subject.EntityType, args.Subject.ExternalID,
				)
				if err != nil {
					return err
				}
				if entityID != "" {
					subjectEntityID = entityID
				} else {
					refJSON, _ := json.Marshal(args.Subject)
					subjectRefJSON = string(refJSON)
				}
			}

			payloadJSON, _ := json.Marshal(args.Payload)
			factID = xid.New().String()
			_, err = q.ExecContext(ctx,
				rebindQuery(t.storage, "INSERT INTO facts (fact_id, fact_type, subject_entity_id, subject_ref, payload, effective_at, correlates_to, dedupe_key, source_id, writer_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"),
				factID,
				args.FactType,
				subjectEntityID,
				subjectRefJSON,
				string(payloadJSON),
				effectiveAt,
				args.CorrelatesTo,
				args.DedupeKey,
				sourceID,
				writerID,
			)
			return err
		})
		if err != nil {
			t.logger.Error("failed to append fact",
				zap.Error(err),
				zap.String("fact_type", args.FactType),
				zap.String("tenant_id", tenantID),
			)
			return errorResult(err.Error()), nil
		}

		t.logger.Info("fact appended successfully",
			zap.String("fact_id", factID),
			zap.String("fact_type", args.FactType),
			zap.Bool("deduped", deduped),
		)

		resp := map[string]any{"factId": factID, "ingested": !deduped}
		if deduped {
			resp["deduped"] = true
		}
		return structuredResult(resp), nil
	}
}

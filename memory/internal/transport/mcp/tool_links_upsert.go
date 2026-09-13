package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// LinksUpsertTool implements memory.links.upsert.
type LinksUpsertTool struct {
	storage storage.Manager
	cfg     *config.Config
	logger  *zap.Logger
}

func NewLinksUpsertTool(storage storage.Manager, cfg *config.Config, logger *zap.Logger) *LinksUpsertTool {
	return &LinksUpsertTool{storage: storage, cfg: cfg, logger: logger.Named("memory.links.upsert")}
}

func (t *LinksUpsertTool) Name() string {
	return "memory.links.upsert"
}

func (t *LinksUpsertTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.links.upsert",
		Description: "Upsert generic links between entities, facts, and documents",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"items": map[string]interface{}{
					"type":     "array",
					"minItems": 1,
					"maxItems": 2000,
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"linkType": map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 128},
							"from": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"kind": map[string]interface{}{"type": "string", "enum": []string{"entity", "fact", "document"}},
									"ref":  map[string]interface{}{"type": "string"},
								},
								"required": []string{"kind", "ref"},
							},
							"to": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"kind": map[string]interface{}{"type": "string", "enum": []string{"entity", "fact", "document"}},
									"ref":  map[string]interface{}{"type": "string"},
								},
								"required": []string{"kind", "ref"},
							},
							"attributes": map[string]interface{}{"type": "object", "additionalProperties": true},
						},
						"required": []string{"linkType", "from", "to"},
					},
				},
				"source": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"system":    map[string]interface{}{"type": "string"},
						"timestamp": map[string]interface{}{"type": "string"},
						"traceId":   map[string]interface{}{"type": "string"},
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
			Required: []string{"items", "source", "writer"},
		},
	}
}

func (t *LinksUpsertTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			TenantID string `json:"tenant_id"`
			Items    []struct {
				LinkType   string         `json:"linkType"`
				From       linkEndpoint   `json:"from"`
				To         linkEndpoint   `json:"to"`
				Attributes map[string]any `json:"attributes"`
			} `json:"items"`
			Source SourceInput `json:"source"`
			Writer WriterInput `json:"writer"`
		}

		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if len(args.Items) == 0 {
			return errorResult("items are required"), nil
		}
		if args.Source.System == "" || args.Source.Timestamp == "" {
			return errorResult("source.system and source.timestamp are required"), nil
		}
		if args.Writer.AppID == "" || args.Writer.ActorID == "" {
			return errorResult("writer.appId and writer.actorId are required"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		timestamp, err := time.Parse(time.RFC3339, args.Source.Timestamp)
		if err != nil {
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
				if item.LinkType == "" {
					return fmt.Errorf("linkType is required")
				}
				fromRef := strings.TrimSpace(item.From.Ref)
				toRef := strings.TrimSpace(item.To.Ref)
				if fromRef == "" || toRef == "" {
					return fmt.Errorf("from.ref and to.ref are required")
				}

				fromEntityID, fromFactID, fromDocID, err := resolveLinkEndpoint(ctx, t.storage, q, item.From)
				if err != nil {
					return err
				}
				toEntityID, toFactID, toDocID, err := resolveLinkEndpoint(ctx, t.storage, q, item.To)
				if err != nil {
					return err
				}

				attrsJSON := "{}"
				if item.Attributes != nil {
					data, _ := json.Marshal(item.Attributes)
					attrsJSON = string(data)
				}

				existingID, err := queryScalarString(ctx, q,
					rebindQuery(t.storage, "SELECT link_id FROM links WHERE link_type = ? AND from_kind = ? AND to_kind = ? AND from_ref = ? AND to_ref = ? AND is_deleted = false LIMIT 1"),
					item.LinkType, item.From.Kind, item.To.Kind, fromRef, toRef,
				)
				if err != nil {
					return err
				}

				if existingID == "" {
					linkID := xid.New().String()
					if _, err := q.ExecContext(ctx,
						rebindQuery(t.storage, "INSERT INTO links (link_id, link_type, from_kind, from_entity_id, from_fact_id, from_document_id, from_ref, to_kind, to_entity_id, to_fact_id, to_document_id, to_ref, attributes, created_at, source_id, writer_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"),
						linkID, item.LinkType, item.From.Kind, fromEntityID, fromFactID, fromDocID, fromRef,
						item.To.Kind, toEntityID, toFactID, toDocID, toRef, attrsJSON, time.Now().UTC(), sourceID, writerID); err != nil {
						return err
					}
					created++
				} else {
					if _, err := q.ExecContext(ctx,
						rebindQuery(t.storage, "UPDATE links SET attributes = ?, source_id = ?, writer_id = ? WHERE link_id = ?"),
						attrsJSON, sourceID, writerID, existingID); err != nil {
						return err
					}
					updated++
				}
			}

			return nil
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return structuredResult(map[string]any{"upserted": created + updated, "created": created, "updated": updated}), nil
	}
}

type linkEndpoint struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// resolveLinkEndpoint resolves entity/fact/document IDs from a link endpoint reference.
// Returns (entityID, factID, docID) as nullable any values.
func resolveLinkEndpoint(ctx context.Context, manager storage.Manager, q storage.Querier, endpoint linkEndpoint) (any, any, any, error) {
	kind := strings.ToLower(strings.TrimSpace(endpoint.Kind))
	ref := strings.TrimSpace(endpoint.Ref)
	if kind == "" || ref == "" {
		return nil, nil, nil, fmt.Errorf("invalid link endpoint")
	}

	switch kind {
	case "entity":
		parts := strings.SplitN(ref, "|", 2)
		if len(parts) != 2 {
			return nil, nil, nil, fmt.Errorf("invalid entity ref")
		}
		entityID, err := queryScalarString(ctx, q, rebindQuery(manager, "SELECT entity_id FROM entities WHERE entity_type = ? AND external_id = ?"), parts[0], parts[1])
		if err != nil {
			return nil, nil, nil, err
		}
		if entityID == "" {
			return nil, nil, nil, nil
		}
		return entityID, nil, nil, nil
	case "fact":
		factID, err := queryScalarString(ctx, q, rebindQuery(manager, "SELECT fact_id FROM facts WHERE fact_id = ?"), ref)
		if err != nil {
			return nil, nil, nil, err
		}
		if factID == "" {
			return nil, nil, nil, nil
		}
		return nil, factID, nil, nil
	case "document":
		docID, err := queryScalarString(ctx, q, rebindQuery(manager, "SELECT document_id FROM documents WHERE document_id = ?"), ref)
		if err != nil {
			return nil, nil, nil, err
		}
		if docID == "" {
			return nil, nil, nil, nil
		}
		return nil, nil, docID, nil
	default:
		return nil, nil, nil, fmt.Errorf("invalid link kind")
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/security"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

var errEntityResourceNotFound = errors.New("entity not found")

// redactResourceMap applies PII redaction to a map returned by resource handlers.
// This ensures resource responses get the same PII protection as query results.
func (s *Server) redactResourceMap(m map[string]any) {
	if len(s.cfg.Security.RedactionKeys) > 0 {
		security.RedactMapKeys(m, s.cfg.Security.RedactionKeys, s.cfg.Security.RedactValue)
	}
}

// registerResources registers MCP resources.
func (s *Server) registerResources(mcpServer *server.MCPServer) {
	// Register resource templates for dynamic resources
	s.addResourceTemplate(mcpServer,
		mcp.NewResourceTemplate(
			"memory://entities/{entity_type}",
			"Entities by type",
			mcp.WithTemplateDescription("List entities of a specific type from the memory kernel"),
			mcp.WithTemplateMIMEType("application/json"),
		),
		s.handleEntitiesResource,
	)

	s.addResourceTemplate(mcpServer,
		mcp.NewResourceTemplate(
			"memory://entity/{entity_id}",
			"Entity by ID",
			mcp.WithTemplateDescription("Get a specific entity by its ID"),
			mcp.WithTemplateMIMEType("application/json"),
		),
		s.handleEntityResource,
	)

	s.addResourceTemplate(mcpServer,
		mcp.NewResourceTemplate(
			"memory://facts/{fact_type}",
			"Facts by type",
			mcp.WithTemplateDescription("List facts of a specific type from the memory kernel"),
			mcp.WithTemplateMIMEType("application/json"),
		),
		s.handleFactsResource,
	)

	s.addResourceTemplate(mcpServer,
		mcp.NewResourceTemplate(
			"memory://documents",
			"Documents",
			mcp.WithTemplateDescription("List all registered documents"),
			mcp.WithTemplateMIMEType("application/json"),
		),
		s.handleDocumentsResource,
	)

	s.addResourceTemplate(mcpServer,
		mcp.NewResourceTemplate(
			"memory://views",
			"Available views",
			mcp.WithTemplateDescription("List all available kernel views and app projections"),
			mcp.WithTemplateMIMEType("application/json"),
		),
		s.handleViewsResource,
	)

	s.logger.Info("registered MCP resources", zap.Int("count", 5))
}

func (s *Server) addResourceTemplate(mcpServer *server.MCPServer, definition mcp.ResourceTemplate, handler server.ResourceTemplateHandlerFunc) {
	mcpServer.AddResourceTemplate(definition, handler)
	s.modernServer.AddResourceTemplate(toOfficialResourceTemplate(definition), adaptResourceHandler(server.ResourceHandlerFunc(handler)))
}

// handleEntitiesResource handles memory://entities/{entity_type} requests
func (s *Server) handleEntitiesResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	identity := shared.IdentityFromContext(ctx)
	tenantID := identity.TenantID
	if tenantID == "" {
		tenantID = s.cfg.Storage.DefaultTenantID
	}
	s.logger.Info("resource read requested", zap.String("resource", "entities"), zap.String("uri", request.Params.URI), zap.String("tenant", tenantID))

	db, err := s.storage.OpenTenant(ctx, tenantID)
	if err != nil {
		s.logger.Error("failed to open tenant database", zap.Error(err), zap.String("tenant", tenantID))
		return nil, fmt.Errorf("failed to open tenant: %w", err)
	}

	// Extract entity_type from URI (memory://entities/{entity_type})
	entityType := extractURIParam(request.Params.URI, "memory://entities/")

	var qr *storage.QueryResult
	if entityType == "" || entityType == "all" {
		qr, err = db.QueryContext(ctx, `
			SELECT entity_id, entity_type, external_id, name, status, attributes, created_at, updated_at
			FROM v_entities
			ORDER BY updated_at DESC
			LIMIT 100
		`)
	} else {
		qr, err = db.QueryContext(ctx, `
			SELECT entity_id, entity_type, external_id, name, status, attributes, created_at, updated_at
			FROM v_entities
			WHERE entity_type = ?
			ORDER BY updated_at DESC
			LIMIT 100
		`, entityType)
	}
	if err != nil {
		s.logger.Error("failed to query entities", zap.Error(err), zap.String("entity_type", entityType))
		return nil, fmt.Errorf("failed to query entities: %w", err)
	}

	var entities []map[string]any
	for _, row := range qr.Rows {
		entity := map[string]any{
			"entity_id":   row["entity_id"],
			"entity_type": row["entity_type"],
			"external_id": row["external_id"],
			"created_at":  row["created_at"],
			"updated_at":  row["updated_at"],
		}
		if v, ok := row["name"]; ok && v != nil {
			entity["name"] = v
		}
		if v, ok := row["status"]; ok && v != nil {
			entity["status"] = v
		}
		if v, ok := row["attributes"]; ok && v != nil {
			if s, ok := v.(string); ok {
				var attrs any
				if err := json.Unmarshal([]byte(s), &attrs); err == nil {
					entity["attributes"] = attrs
				}
			} else {
				entity["attributes"] = v
			}
		}
		s.redactResourceMap(entity)
		entities = append(entities, entity)
	}

	result := map[string]any{
		"entities": entities,
		"count":    len(entities),
	}
	if entityType != "" && entityType != "all" {
		result["entity_type"] = entityType
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleEntityResource handles memory://entity/{entity_id} requests
func (s *Server) handleEntityResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	identity := shared.IdentityFromContext(ctx)
	tenantID := identity.TenantID
	if tenantID == "" {
		tenantID = s.cfg.Storage.DefaultTenantID
	}
	s.logger.Info("resource read requested", zap.String("resource", "entity"), zap.String("uri", request.Params.URI), zap.String("tenant", tenantID))

	db, err := s.storage.OpenTenant(ctx, tenantID)
	if err != nil {
		s.logger.Error("failed to open tenant database", zap.Error(err), zap.String("tenant", tenantID))
		return nil, fmt.Errorf("failed to open tenant: %w", err)
	}

	entityID := extractURIParam(request.Params.URI, "memory://entity/")
	if entityID == "" {
		s.logger.Warn("entity_id is required but empty", zap.String("uri", request.Params.URI))
		return nil, fmt.Errorf("entity_id is required")
	}

	qr, err := db.QueryContext(ctx, `
		SELECT entity_id, entity_type, external_id, name, status, attributes, created_at, updated_at
		FROM v_entities
		WHERE entity_id = ?
	`, entityID)
	if err != nil {
		s.logger.Error("failed to query entity", zap.Error(err), zap.String("entity_id", entityID))
		return nil, fmt.Errorf("failed to query entity: %w", err)
	}
	if len(qr.Rows) == 0 {
		s.logger.Warn("entity not found", zap.String("entity_id", entityID))
		return nil, fmt.Errorf("%w: %s", errEntityResourceNotFound, entityID)
	}

	row := qr.Rows[0]
	entity := map[string]any{
		"entity_id":   row["entity_id"],
		"entity_type": row["entity_type"],
		"external_id": row["external_id"],
		"created_at":  row["created_at"],
		"updated_at":  row["updated_at"],
	}
	if v, ok := row["name"]; ok && v != nil {
		entity["name"] = v
	}
	if v, ok := row["status"]; ok && v != nil {
		entity["status"] = v
	}
	if v, ok := row["attributes"]; ok && v != nil {
		if s, ok := v.(string); ok {
			var attrs any
			if err := json.Unmarshal([]byte(s), &attrs); err == nil {
				entity["attributes"] = attrs
			}
		} else {
			entity["attributes"] = v
		}
	}

	s.redactResourceMap(entity)

	data, _ := json.MarshalIndent(entity, "", "  ")
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleFactsResource handles memory://facts/{fact_type} requests
func (s *Server) handleFactsResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	identity := shared.IdentityFromContext(ctx)
	tenantID := identity.TenantID
	if tenantID == "" {
		tenantID = s.cfg.Storage.DefaultTenantID
	}
	s.logger.Info("resource read requested", zap.String("resource", "facts"), zap.String("uri", request.Params.URI), zap.String("tenant", tenantID))

	db, err := s.storage.OpenTenant(ctx, tenantID)
	if err != nil {
		s.logger.Error("failed to open tenant database", zap.Error(err), zap.String("tenant", tenantID))
		return nil, fmt.Errorf("failed to open tenant: %w", err)
	}

	factType := extractURIParam(request.Params.URI, "memory://facts/")

	var qr *storage.QueryResult
	if factType == "" || factType == "all" {
		qr, err = db.QueryContext(ctx, `
			SELECT fact_id, fact_type, effective_at, ingested_at, payload, subject_entity_id, subject_name
			FROM v_facts_enriched
			ORDER BY effective_at DESC
			LIMIT 100
		`)
	} else {
		qr, err = db.QueryContext(ctx, `
			SELECT fact_id, fact_type, effective_at, ingested_at, payload, subject_entity_id, subject_name
			FROM v_facts_enriched
			WHERE fact_type = ?
			ORDER BY effective_at DESC
			LIMIT 100
		`, factType)
	}
	if err != nil {
		s.logger.Error("failed to query facts", zap.Error(err), zap.String("fact_type", factType))
		return nil, fmt.Errorf("failed to query facts: %w", err)
	}

	var facts []map[string]any
	for _, row := range qr.Rows {
		fact := map[string]any{
			"fact_id":      row["fact_id"],
			"fact_type":    row["fact_type"],
			"effective_at": row["effective_at"],
			"ingested_at":  row["ingested_at"],
		}
		if v, ok := row["payload"]; ok && v != nil {
			if s, ok := v.(string); ok {
				var p any
				if err := json.Unmarshal([]byte(s), &p); err == nil {
					fact["payload"] = p
				}
			} else {
				fact["payload"] = v
			}
		}
		if v, ok := row["subject_entity_id"]; ok && v != nil {
			fact["subject_entity_id"] = v
		}
		if v, ok := row["subject_name"]; ok && v != nil {
			fact["subject_name"] = v
		}
		s.redactResourceMap(fact)
		facts = append(facts, fact)
	}

	result := map[string]any{
		"facts": facts,
		"count": len(facts),
	}
	if factType != "" && factType != "all" {
		result["fact_type"] = factType
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleDocumentsResource handles memory://documents requests
func (s *Server) handleDocumentsResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	identity := shared.IdentityFromContext(ctx)
	tenantID := identity.TenantID
	if tenantID == "" {
		tenantID = s.cfg.Storage.DefaultTenantID
	}
	s.logger.Info("resource read requested", zap.String("resource", "documents"), zap.String("uri", request.Params.URI), zap.String("tenant", tenantID))

	db, err := s.storage.OpenTenant(ctx, tenantID)
	if err != nil {
		s.logger.Error("failed to open tenant database", zap.Error(err), zap.String("tenant", tenantID))
		return nil, fmt.Errorf("failed to open tenant: %w", err)
	}

	qr, err := db.QueryContext(ctx, `
		SELECT document_id, uri, mime_type, title, tags, metadata, created_at
		FROM v_documents
		ORDER BY created_at DESC
		LIMIT 100
	`)
	if err != nil {
		s.logger.Error("failed to query documents", zap.Error(err))
		return nil, fmt.Errorf("failed to query documents: %w", err)
	}

	var documents []map[string]any
	for _, row := range qr.Rows {
		doc := map[string]any{
			"document_id": row["document_id"],
			"uri":         row["uri"],
			"mime_type":   row["mime_type"],
			"created_at":  row["created_at"],
		}
		if v, ok := row["title"]; ok && v != nil {
			doc["title"] = v
		}
		if v, ok := row["tags"]; ok && v != nil {
			if s, ok := v.(string); ok {
				var t any
				if err := json.Unmarshal([]byte(s), &t); err == nil {
					doc["tags"] = t
				}
			} else {
				doc["tags"] = v
			}
		}
		if v, ok := row["metadata"]; ok && v != nil {
			if s, ok := v.(string); ok {
				var m any
				if err := json.Unmarshal([]byte(s), &m); err == nil {
					doc["metadata"] = m
				}
			} else {
				doc["metadata"] = v
			}
		}
		s.redactResourceMap(doc)
		documents = append(documents, doc)
	}

	result := map[string]any{
		"documents": documents,
		"count":     len(documents),
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// handleViewsResource handles memory://views requests
func (s *Server) handleViewsResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	identity := shared.IdentityFromContext(ctx)
	tenantID := identity.TenantID
	if tenantID == "" {
		tenantID = s.cfg.Storage.DefaultTenantID
	}
	s.logger.Info("resource read requested", zap.String("resource", "views"), zap.String("uri", request.Params.URI), zap.String("tenant", tenantID))

	db, err := s.storage.OpenTenant(ctx, tenantID)
	if err != nil {
		s.logger.Error("failed to open tenant database", zap.Error(err), zap.String("tenant", tenantID))
		return nil, fmt.Errorf("failed to open tenant: %w", err)
	}

	// Query for views - works for both DuckDB and PostgreSQL
	qr, err := db.QueryContext(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_type = 'VIEW'
		AND table_schema IN ('main', 'public')
		ORDER BY table_name
	`)
	if err != nil {
		s.logger.Error("failed to query views", zap.Error(err))
		return nil, fmt.Errorf("failed to query views: %w", err)
	}

	var kernelViews []string
	var appViews []string

	for _, row := range qr.Rows {
		viewName, _ := row["table_name"].(string)
		if viewName == "" {
			continue
		}
		if len(viewName) > 4 && viewName[:4] == "app_" {
			appViews = append(appViews, viewName)
		} else if len(viewName) > 2 && viewName[:2] == "v_" {
			kernelViews = append(kernelViews, viewName)
		}
	}

	result := map[string]any{
		"kernel_views": kernelViews,
		"app_views":    appViews,
		"total":        len(kernelViews) + len(appViews),
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

// extractURIParam extracts the parameter from a URI given a prefix
func extractURIParam(uri, prefix string) string {
	if len(uri) > len(prefix) {
		return uri[len(prefix):]
	}
	return ""
}

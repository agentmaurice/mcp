//go:build duckdb

package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

func TestLinksUpsert(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.Security.RequireViewsOnly = false
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	ctx := context.Background()
	tenantID := cfg.Storage.DefaultTenantID

	entityID := xid.New().String()
	docID := "doc-1"
	err = manager.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		if _, err := q.ExecContext(ctx,
			"INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			entityID, "invoice", "INV-1", "INV-1", time.Now().UTC(), time.Now().UTC()); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx,
			"INSERT INTO documents (document_id, uri, mime_type, created_at) VALUES (?, ?, ?, ?)",
			docID, "file:///tmp/doc.pdf", "application/pdf", time.Now().UTC()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	tool := NewLinksUpsertTool(manager, cfg, zap.NewNop())
	args := map[string]any{
		"tenant_id": tenantID,
		"items": []any{
			map[string]any{
				"linkType": "represents",
				"from":     map[string]any{"kind": "document", "ref": docID},
				"to":       map[string]any{"kind": "entity", "ref": "invoice|INV-1"},
			},
		},
		"source": map[string]any{"system": "test", "timestamp": time.Now().UTC().Format(time.RFC3339)},
		"writer": map[string]any{"appId": "billing", "actorId": "tester"},
	}
	res, err := tool.Handler()(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool.Name(), Arguments: args}})
	if err != nil {
		t.Fatalf("links.upsert failed: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("links.upsert error: %#v", res)
	}

	result, err := executeQuery(ctx, manager, cfg, zap.NewNop(), "SELECT link_type, from_any, to_any FROM v_links", nil, 10, 2000, tenantID)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) == 0 {
		t.Fatalf("expected link rows")
	}
}

func TestViewsCreateOrReplace(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.Security.RequireViewsOnly = false
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	ctx := context.Background()
	tenantID := cfg.Storage.DefaultTenantID

	err = manager.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		attrs := map[string]any{"amount": 42}
		attrJSON, _ := json.Marshal(attrs)
		if _, err := q.ExecContext(ctx,
			"INSERT INTO entities (entity_id, entity_type, external_id, name, attributes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			xid.New().String(), "invoice", "INV-2", "INV-2", string(attrJSON), time.Now().UTC(), time.Now().UTC()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	tool := NewViewsCreateTool(manager, cfg, zap.NewNop())
	args := map[string]any{
		"tenant_id": tenantID,
		"appId":     "billing",
		"viewName":  "app_billing__invoices",
		"sql":       "SELECT entity_id, external_id FROM v_entities WHERE entity_type = 'invoice'",
	}
	res, err := tool.Handler()(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool.Name(), Arguments: args}})
	if err != nil {
		t.Fatalf("views.create_or_replace failed: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("views.create_or_replace error: %#v", res)
	}

	result, err := executeQuery(ctx, manager, cfg, zap.NewNop(), "SELECT external_id FROM app_billing__invoices", nil, 10, 2000, tenantID)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) == 0 {
		t.Fatalf("expected rows from view")
	}
}

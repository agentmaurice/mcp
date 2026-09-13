//go:build postgres

package mcp

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

func TestPostgresIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MEMORY_POSTGRES_DSN"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("POSTGRES_DSN"))
	}
	if dsn == "" {
		t.Skip("POSTGRES_DSN or MEMORY_POSTGRES_DSN is not set")
	}

	tenantID := "itest_" + xid.New().String()
	cfg := postgresTestConfig(dsn, tenantID)

	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer func() {
		_ = manager.Close()
		dropSchema(t, dsn, cfg.Storage.PostgresSchemaPrefix, tenantID)
	}()

	ctx := context.Background()

	upsertTool := NewUpsertEntitiesTool(manager, cfg, zap.NewNop())
	appendTool := NewAppendFactTool(manager, cfg, zap.NewNop())
	docTool := NewAttachDocumentTool(manager, cfg, zap.NewNop())
	linksTool := NewLinksUpsertTool(manager, cfg, zap.NewNop())
	queryTool := NewQueryTool(manager, cfg, nil, zap.NewNop())
	viewsTool := NewViewsCreateTool(manager, cfg, zap.NewNop())
	capTool := NewCapabilitiesTool(manager, cfg, zap.NewNop())

	now := time.Now().UTC().Format(time.RFC3339)

	upsertArgs := map[string]any{
		"tenant_id":  tenantID,
		"entityType": "customer",
		"items": []any{
			map[string]any{
				"externalId": "CUST-100",
				"name":       "ACME",
				"attributes": map[string]any{"country": "FR"},
			},
		},
		"source": map[string]any{"system": "itest", "timestamp": now},
		"writer": map[string]any{"appId": "itest", "actorId": "tester"},
	}
	if res := callTool(ctx, t, upsertTool, upsertArgs); res.IsError {
		t.Fatalf("entities.upsert error: %#v", res)
	}

	appendArgs := map[string]any{
		"tenant_id":   tenantID,
		"factType":    "invoice_paid",
		"subject":     map[string]any{"entityType": "customer", "externalId": "CUST-100"},
		"payload":     map[string]any{"invoiceId": "INV-1", "amount": 42},
		"effectiveAt": now,
		"source":      map[string]any{"system": "itest", "timestamp": now},
		"writer":      map[string]any{"appId": "itest", "actorId": "tester"},
	}
	if res := callTool(ctx, t, appendTool, appendArgs); res.IsError {
		t.Fatalf("facts.append error: %#v", res)
	}

	docArgs := map[string]any{
		"tenant_id":  tenantID,
		"documentId": "doc-1",
		"uri":        "file:///tmp/doc.pdf",
		"mimeType":   "application/pdf",
		"source":     map[string]any{"system": "itest", "timestamp": now},
		"writer":     map[string]any{"appId": "itest", "actorId": "tester"},
	}
	if res := callTool(ctx, t, docTool, docArgs); res.IsError {
		t.Fatalf("documents.register error: %#v", res)
	}

	linksArgs := map[string]any{
		"tenant_id": tenantID,
		"items": []any{
			map[string]any{
				"linkType": "represents",
				"from":     map[string]any{"kind": "document", "ref": "doc-1"},
				"to":       map[string]any{"kind": "entity", "ref": "customer|CUST-100"},
			},
		},
		"source": map[string]any{"system": "itest", "timestamp": now},
		"writer": map[string]any{"appId": "itest", "actorId": "tester"},
	}
	if res := callTool(ctx, t, linksTool, linksArgs); res.IsError {
		t.Fatalf("links.upsert error: %#v", res)
	}

	queryArgs := map[string]any{
		"tenant_id": tenantID,
		"sql":       "SELECT name FROM v_entities WHERE entity_type = 'customer' LIMIT 5",
		"maxRows":   5,
	}
	queryRes := callTool(ctx, t, queryTool, queryArgs)
	if queryRes.IsError {
		t.Fatalf("memory.query error: %#v", queryRes)
	}
	payload, ok := queryRes.StructuredContent.(map[string]any)
	if !ok || payload["rows"] == nil {
		t.Fatalf("memory.query missing rows")
	}

	viewArgs := map[string]any{
		"tenant_id": tenantID,
		"appId":     "billing",
		"viewName":  "app_billing__customers",
		"sql":       "SELECT entity_id, external_id FROM v_entities WHERE entity_type = 'customer'",
	}
	if res := callTool(ctx, t, viewsTool, viewArgs); res.IsError {
		t.Fatalf("views.create_or_replace error: %#v", res)
	}

	capRes := callTool(ctx, t, capTool, map[string]any{})
	capPayload, ok := capRes.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing payload")
	}
	if backend, _ := capPayload["backend"].(string); backend != "postgres" {
		t.Fatalf("expected backend postgres, got %q", backend)
	}
}

func callTool(ctx context.Context, t *testing.T, tool Tool, args map[string]any) *mcp.CallToolResult {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool.Name(), Arguments: args}}
	res, err := tool.Handler()(ctx, req)
	if err != nil {
		t.Fatalf("tool %s error: %v", tool.Name(), err)
	}
	if res == nil {
		t.Fatalf("tool %s returned nil result", tool.Name())
	}
	return res
}

func postgresTestConfig(dsn string, tenantID string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Address:           ":0",
			BasePath:          "/mcp",
			KeepAlive:         false,
			KeepAliveInterval: 10,
		},
		Storage: config.StorageConfig{
			Backend:              "postgres",
			PostgresDSN:          dsn,
			PostgresSchemaPrefix: "test_",
			DefaultTenantID:      tenantID,
			ReadMaxOpenConns:     2,
			ReadMaxIdleConns:     1,
		},
		Query: config.QueryConfig{
			MaxRowsDefault:   500,
			MaxRowsLimit:     5000,
			TimeoutMsDefault: 2000,
			TimeoutMsMax:     10000,
			MaxResponseBytes: 2 * 1024 * 1024,
		},
		Security: config.SecurityConfig{
			Denylist:         []string{"insert", "update", "delete", "create", "drop", "alter", "copy", "attach", "detach", "pragma", "export", "import"},
			AllowedObjects:   []string{},
			AllowedColumns:   map[string][]string{},
			AllowedIndexes:   []string{},
			RequireViewsOnly: true,
			RedactionKeys:    []string{},
			RedactValue:      "***redacted***",
		},
	}
}

func dropSchema(t *testing.T, dsn, prefix, tenantID string) {
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Logf("failed to open admin db: %v", err)
		return
	}
	defer admin.Close()
	schema := buildSchemaName(prefix, tenantID)
	_, err = admin.Exec("DROP SCHEMA IF EXISTS " + quoteIdent(schema) + " CASCADE")
	if err != nil {
		t.Logf("failed to drop schema %s: %v", schema, err)
	}
}

var schemaRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func buildSchemaName(prefix string, tenantID string) string {
	name := strings.TrimSpace(tenantID)
	if name == "" {
		return ""
	}
	name = strings.ToLower(name)
	name = schemaRe.ReplaceAllString(name, "_")
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return name
	}
	return prefix + name
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

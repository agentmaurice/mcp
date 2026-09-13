//go:build duckdb

package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

func TestAppendFactDedupe(t *testing.T) {
	cfg := testConfig(t.TempDir())
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	tool := NewAppendFactTool(manager, cfg, zap.NewNop())

	ctx := context.Background()
	effective := time.Now().UTC().Format(time.RFC3339)
	sourceTime := effective

	args := map[string]any{
		"tenant_id": "test-tenant",
		"factType":  "billing.invoice_paid",
		"payload": map[string]any{
			"invoiceId": "INV-1",
			"amount":    12.3,
		},
		"effectiveAt": effective,
		"dedupeKey":   "dedupe-1",
		"source": map[string]any{
			"system":    "test",
			"timestamp": sourceTime,
		},
		"writer": map[string]any{
			"appId":     "billing",
			"actorId":   "tester",
			"actorType": "human",
		},
	}

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool.Name(), Arguments: args}}
	res, err := tool.Handler()(ctx, req)
	if err != nil {
		t.Fatalf("first append failed: %v", err)
	}
	if res == nil {
		t.Fatalf("nil result")
	}
	if res.IsError {
		t.Fatalf("tool error: %#v", res.Content)
	}
	payload, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("unexpected structuredContent: %T %#v", res.StructuredContent, res.StructuredContent)
	}
	factID, _ := payload["factId"].(string)
	if factID == "" {
		t.Fatalf("missing factId")
	}
	if ingested, _ := payload["ingested"].(bool); !ingested {
		t.Fatalf("expected ingested=true")
	}

	res2, err := tool.Handler()(ctx, req)
	if err != nil {
		t.Fatalf("second append failed: %v", err)
	}
	if res2 == nil {
		t.Fatalf("nil result (second)")
	}
	if res2.IsError {
		t.Fatalf("tool error (second): %#v", res2.Content)
	}
	payload2, ok := res2.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("unexpected structuredContent (second): %T %#v", res2.StructuredContent, res2.StructuredContent)
	}
	factID2, _ := payload2["factId"].(string)
	if factID2 != factID {
		t.Fatalf("expected same factId on dedupe")
	}
	if deduped, _ := payload2["deduped"].(bool); !deduped {
		t.Fatalf("expected deduped=true")
	}
}

func testConfig(baseDir string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Address:           ":0",
			BasePath:          "/mcp",
			KeepAlive:         false,
			KeepAliveInterval: 10,
		},
		Storage: config.StorageConfig{
			Backend:          "duckdb",
			BaseDir:          baseDir,
			TenantsDirName:   "tenants",
			DBFilename:       "memory.duckdb",
			DocumentsDirName: "documents",
			DefaultTenantID:  "test-tenant",
			WriteQueueSize:   8,
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

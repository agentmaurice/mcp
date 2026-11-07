//go:build duckdb

package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	mcp "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// Review case: memory.indexes.ensure refuses a single-column index, with an explicit
// error that points to the DuckDB defect. Two columns are accepted.
func TestIndexesEnsureRefusesSingleColumnIndex(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.Security.AllowedIndexes = []string{"app_billing__*"}
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	tenantID := cfg.Storage.DefaultTenantID
	err = manager.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		_, err := q.ExecContext(ctx, "CREATE TABLE app_billing__invoices (external_id VARCHAR, amount INTEGER)")
		return err
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	tool := NewIndexesEnsureTool(manager, cfg, zap.NewNop())
	call := func(columns []string, name string) *mcp.CallToolResult {
		t.Helper()
		res, err := tool.Handler()(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
			Name: tool.Name(),
			Arguments: map[string]any{
				"tenant_id": tenantID,
				"appId":     "billing",
				"items": []any{map[string]any{
					"indexName": name,
					"table":     "app_billing__invoices",
					"columns":   columns,
				}},
			},
		}})
		if err != nil {
			t.Fatalf("handler: %v", err)
		}
		return res
	}

	single := call([]string{"external_id"}, "app_billing__ix_single")
	if !single.IsError {
		t.Fatal("a single-column index must be refused")
	}
	text := single.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "single-column indexes are refused") || !strings.Contains(text, "DuckDB 1.4.1") {
		t.Fatalf("refusal must explain the DuckDB defect: %q", text)
	}

	composite := call([]string{"external_id", "amount"}, "app_billing__ix_pair")
	if composite.IsError {
		t.Fatalf("a two-column index must be accepted: %v", composite.Content)
	}
}

//go:build duckdb

package mcp

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// BenchmarkMemoryQuery measures one memory.query call through the tool handler, with a
// JSON logger at Info writing to io.Discard, as in a sidecar: it prices the log lines
// of the call (AGENT-1093 replaced the SQL and arguments by sizes) next to the query.
//
//	go test -tags duckdb -run '^$' -bench MemoryQuery ./internal/transport/mcp
func BenchmarkMemoryQuery(b *testing.B) {
	cfg := testConfig(b.TempDir())
	cfg.Security.RequireViewsOnly = false
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		b.Fatalf("init storage: %v", err)
	}
	b.Cleanup(func() { _ = manager.Close() })
	const tenant = "tenant-bench"
	err = manager.WithWriter(context.Background(), tenant, func(ctx context.Context, q storage.Querier) error {
		for i := 1; i <= 50; i++ {
			name := fmt.Sprintf("alice-%d", i)
			if _, err := q.ExecContext(ctx,
				"INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at) VALUES (?, 'customer', ?, ?, now(), now())",
				name, name, name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatalf("seed: %v", err)
	}
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(io.Discard), zapcore.InfoLevel))
	handler := NewQueryTool(manager, cfg, nil, logger).Handler()
	request := mcp.CallToolRequest{}
	request.Params.Name = "memory.query"
	request.Params.Arguments = map[string]any{"tenant_id": tenant, "sql": "SELECT name FROM entities WHERE name LIKE 'alice-%' ORDER BY name LIMIT 20"}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := handler(ctx, request); err != nil {
			b.Fatal(err)
		}
	}
}

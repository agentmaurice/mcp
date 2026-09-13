//go:build duckdb

package storage

import (
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage/duckdb"
	"go.uber.org/zap"
)

func init() {
	Register("duckdb", func(cfg config.StorageConfig, logger *zap.Logger) (Manager, error) {
		return duckdb.NewManager(cfg, logger), nil
	})
}

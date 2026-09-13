//go:build !duckdb

package storage

import (
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/duckhttp"
	"go.uber.org/zap"
)

func init() {
	Register("duckhttp", func(cfg config.StorageConfig, logger *zap.Logger) (Manager, error) {
		return duckhttp.NewManager(cfg, logger)
	})
}

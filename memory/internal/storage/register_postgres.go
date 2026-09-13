//go:build postgres

package storage

import (
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage/postgres"
	"go.uber.org/zap"
)

func init() {
	Register("postgres", func(cfg config.StorageConfig, logger *zap.Logger) (Manager, error) {
		return postgres.NewManager(cfg, logger)
	})
}

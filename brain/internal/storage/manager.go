package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/types"
	"go.uber.org/zap"
)

// QueryResult is an alias so callers can keep using storage.QueryResult.
type QueryResult = types.QueryResult

// Querier is an alias so callers can keep using storage.Querier.
type Querier = types.Querier

// Manager defines the storage backend contract.
type Manager interface {
	Backend() string
	Dialect() string
	OpenTenant(ctx context.Context, tenantID string) (Querier, error)
	WithWriter(ctx context.Context, tenantID string, fn func(context.Context, Querier) error) error
	TenantPath(tenantID string) string
	Close() error
}

// AtomicWriter is implemented by backends that can execute a serialized writer
// callback inside a database transaction.
type AtomicWriter interface {
	WithTransaction(ctx context.Context, tenantID string, fn func(context.Context, Querier) error) error
}

// WithAtomicWriter uses a backend transaction when available. The HTTP
// compatibility backend falls back to its serialized writer contract.
func WithAtomicWriter(ctx context.Context, manager Manager, tenantID string, fn func(context.Context, Querier) error) error {
	if transactional, ok := manager.(AtomicWriter); ok {
		return transactional.WithTransaction(ctx, tenantID, fn)
	}
	return manager.WithWriter(ctx, tenantID, fn)
}

// Factory creates a backend manager.
type Factory func(cfg config.StorageConfig, logger *zap.Logger) (Manager, error)

var factories = map[string]Factory{}

// Register registers a backend factory.
func Register(name string, factory Factory) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" || factory == nil {
		return
	}
	factories[key] = factory
}

// NewManager returns a backend manager for the configured storage backend.
func NewManager(cfg config.StorageConfig, logger *zap.Logger) (Manager, error) {
	backend := strings.ToLower(strings.TrimSpace(cfg.Backend))
	if backend == "" {
		backend = "duckdb"
	}
	factory, ok := factories[backend]
	if !ok {
		return nil, fmt.Errorf("unsupported storage backend: %s", backend)
	}
	return factory(cfg, logger)
}

// SupportedBackends returns the registered backend names.
func SupportedBackends() []string {
	out := make([]string, 0, len(factories))
	for name := range factories {
		out = append(out, name)
	}
	return out
}

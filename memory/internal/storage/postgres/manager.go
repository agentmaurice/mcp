package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	stypes "github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

type tenantDB struct {
	db     *sql.DB
	schema string
}

// Manager handles PostgreSQL connections and per-tenant schemas.
type Manager struct {
	cfg     config.StorageConfig
	logger  *zap.Logger
	mu      sync.Mutex
	adminDB *sql.DB
	tenants map[string]*tenantDB
}

// NewManager creates a new Postgres manager.
func NewManager(cfg config.StorageConfig, logger *zap.Logger) (*Manager, error) {
	if strings.TrimSpace(cfg.PostgresDSN) == "" {
		return nil, fmt.Errorf("postgres_dsn is required for postgres backend")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	adminDB, err := sql.Open("pgx", cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres admin db: %w", err)
	}
	if cfg.ReadMaxOpenConns > 0 {
		adminDB.SetMaxOpenConns(cfg.ReadMaxOpenConns)
	}
	if cfg.ReadMaxIdleConns > 0 {
		adminDB.SetMaxIdleConns(cfg.ReadMaxIdleConns)
	}

	return &Manager{
		cfg:     cfg,
		logger:  logger.Named("postgres"),
		adminDB: adminDB,
		tenants: make(map[string]*tenantDB),
	}, nil
}

// Close closes all tenant databases.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for tenant, td := range m.tenants {
		if td.db != nil {
			if err := td.db.Close(); err != nil {
				m.logger.Warn("failed to close tenant db", zap.String("tenant", tenant), zap.Error(err))
			}
		}
	}
	m.tenants = make(map[string]*tenantDB)
	if m.adminDB != nil {
		_ = m.adminDB.Close()
	}
	return nil
}

// Backend returns the backend name.
func (m *Manager) Backend() string {
	return "postgres"
}

// Dialect returns the SQL dialect name.
func (m *Manager) Dialect() string {
	return "postgres"
}

// TenantPath returns empty for postgres.
func (m *Manager) TenantPath(tenantID string) string {
	return ""
}

// OpenTenant returns a read-capable Querier for a tenant.
func (m *Manager) OpenTenant(ctx context.Context, tenantID string) (stypes.Querier, error) {
	td, err := m.ensureTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return newSQLQuerier(td.db), nil
}

// WithWriter executes a write using a dedicated connection (no serialization needed in Postgres).
func (m *Manager) WithWriter(ctx context.Context, tenantID string, fn func(context.Context, stypes.Querier) error) error {
	td, err := m.ensureTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := td.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	querier := newSQLConnQuerier(conn)
	return fn(ctx, querier)
}

func (m *Manager) ensureTenant(ctx context.Context, tenantID string) (*tenantDB, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tenantID == "" {
		return nil, fmt.Errorf("tenantID is required")
	}
	if td, ok := m.tenants[tenantID]; ok {
		return td, nil
	}

	schema := buildSchemaName(m.cfg.PostgresSchemaPrefix, tenantID)
	if schema == "" {
		return nil, fmt.Errorf("failed to resolve tenant schema")
	}

	if err := m.ensureSchema(ctx, schema); err != nil {
		return nil, err
	}

	db, err := openDBWithSchema(m.cfg.PostgresDSN, schema)
	if err != nil {
		return nil, err
	}
	if m.cfg.ReadMaxOpenConns > 0 {
		db.SetMaxOpenConns(m.cfg.ReadMaxOpenConns)
	}
	if m.cfg.ReadMaxIdleConns > 0 {
		db.SetMaxIdleConns(m.cfg.ReadMaxIdleConns)
	}

	if err := EnsureSchema(ctx, db, schema); err != nil {
		_ = db.Close()
		return nil, err
	}

	td := &tenantDB{db: db, schema: schema}
	m.tenants[tenantID] = td
	return td, nil
}

func (m *Manager) ensureSchema(ctx context.Context, schema string) error {
	stmt := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdent(schema))
	if _, err := m.adminDB.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("failed to create schema %s: %w", schema, err)
	}
	return nil
}

func openDBWithSchema(dsn string, schema string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse postgres dsn: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = make(map[string]string)
	}
	cfg.RuntimeParams["search_path"] = quoteIdent(schema)
	connStr := stdlib.RegisterConnConfig(cfg)
	return sql.Open("pgx", connStr)
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

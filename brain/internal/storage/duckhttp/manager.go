//go:build !duckdb

package duckhttp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	stypes "github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/types"
	duckhttpclient "github.com/agentmaurice/mcpchatui/mcp/shared/duckhttp"
)

type tenantState struct {
	attached bool
}

// Manager handles per-tenant DuckDB access via httpserver.
type Manager struct {
	cfg     config.StorageConfig
	logger  *zap.Logger
	client  *duckhttpclient.Client
	mu      sync.Mutex
	tenants map[string]*tenantState
}

// NewManager creates a new duckhttp Manager.
func NewManager(cfg config.StorageConfig, logger *zap.Logger) (*Manager, error) {
	if cfg.DuckDBHTTPURL == "" {
		return nil, fmt.Errorf("duckhttp: duckdb_http_url is required")
	}
	return &Manager{
		cfg:     cfg,
		logger:  logger.Named("duckhttp"),
		client:  duckhttpclient.NewClient(cfg.DuckDBHTTPURL),
		tenants: make(map[string]*tenantState),
	}, nil
}

func (m *Manager) Backend() string { return "duckhttp" }
func (m *Manager) Dialect() string { return "duckdb" }

func (m *Manager) TenantPath(tenantID string) string {
	return filepath.Join(m.cfg.BaseDir, m.cfg.TenantsDirName, tenantID, m.cfg.DBFilename)
}

// OpenTenant attaches the tenant database if needed and returns a Querier.
func (m *Manager) OpenTenant(ctx context.Context, tenantID string) (stypes.Querier, error) {
	if err := m.ensureTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	return &tenantQuerier{
		client: m.client,
		alias:  tenantAlias(tenantID),
	}, nil
}

// WithWriter serializes writes via the HTTP client.
func (m *Manager) WithWriter(ctx context.Context, tenantID string, fn func(context.Context, stypes.Querier) error) error {
	q, err := m.OpenTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	return fn(ctx, q)
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.tenants {
		_, _ = m.client.Exec(context.Background(), fmt.Sprintf("DETACH %s", tenantAlias(id)))
	}
	m.tenants = make(map[string]*tenantState)
	return nil
}

func (m *Manager) ensureTenant(ctx context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tenantID == "" {
		return fmt.Errorf("tenantID is required")
	}
	if _, ok := m.tenants[tenantID]; ok {
		return nil
	}

	path := m.TenantPath(tenantID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create tenant directory: %w", err)
	}

	alias := tenantAlias(tenantID)
	attachSQL := fmt.Sprintf("ATTACH '%s' AS %s", path, alias)
	if _, err := m.client.Exec(ctx, attachSQL); err != nil {
		return fmt.Errorf("failed to attach tenant db: %w", err)
	}

	if err := m.ensureSchema(ctx, alias); err != nil {
		return fmt.Errorf("schema migration failed: %w", err)
	}

	m.tenants[tenantID] = &tenantState{attached: true}
	m.logger.Info("tenant attached", zap.String("tenant", tenantID), zap.String("path", path))
	return nil
}

func (m *Manager) ensureSchema(ctx context.Context, alias string) error {
	migrations := []string{
		// Sources table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.sources (
			id              TEXT PRIMARY KEY,
			tenant_id       TEXT NOT NULL,
			source_type     TEXT NOT NULL,
			source_uri      TEXT NOT NULL,
			display_name    TEXT,
			config          JSON,
			last_indexed_at TIMESTAMP,
			status          TEXT DEFAULT 'pending',
			error_message   TEXT,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`, alias),

		// Documents table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.documents (
			id              TEXT PRIMARY KEY,
			tenant_id       TEXT NOT NULL,
			source_id       TEXT NOT NULL,
			file_path       TEXT NOT NULL,
			title           TEXT,
			doc_type        TEXT NOT NULL,
			language        TEXT,
			content_hash    TEXT NOT NULL,
			metadata        JSON,
			size_bytes      BIGINT,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`, alias),

		// Chunks table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.chunks (
			id              TEXT PRIMARY KEY,
			tenant_id       TEXT NOT NULL,
			document_id     TEXT NOT NULL,
			chunk_index     INTEGER NOT NULL,
			content         TEXT NOT NULL,
			summary         TEXT,
			context         TEXT,
			chunk_type      TEXT NOT NULL,
			symbol_name     TEXT,
			start_line      INTEGER,
			end_line        INTEGER,
			metadata        JSON,
			embedding       FLOAT[],
			embedding_model TEXT,
			bm25_content    TEXT,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`, alias),

		// Index jobs table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.index_jobs (
			id              TEXT PRIMARY KEY,
			tenant_id       TEXT NOT NULL,
			source_id       TEXT NOT NULL,
			status          TEXT DEFAULT 'pending',
			progress        DOUBLE DEFAULT 0.0,
			total_files     INTEGER DEFAULT 0,
			processed_files INTEGER DEFAULT 0,
			total_chunks    INTEGER DEFAULT 0,
			error_message   TEXT,
			started_at      TIMESTAMP,
			completed_at    TIMESTAMP,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`, alias),

		// Indexes
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_sources_tenant ON %s.sources(tenant_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_documents_tenant ON %s.documents(tenant_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_documents_source ON %s.documents(source_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_documents_hash ON %s.documents(tenant_id, source_id, content_hash)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_chunks_tenant ON %s.chunks(tenant_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_chunks_document ON %s.chunks(document_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_chunks_type ON %s.chunks(tenant_id, chunk_type)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_chunks_symbol ON %s.chunks(tenant_id, symbol_name)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_index_jobs_tenant ON %s.index_jobs(tenant_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_index_jobs_source ON %s.index_jobs(source_id)`, alias),
	}

	for _, migration := range migrations {
		if _, err := m.client.Exec(ctx, migration); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, migration)
		}
	}
	return nil
}

func tenantAlias(tenantID string) string {
	return "t_" + tenantID
}

// tenantQuerier wraps duckhttpclient.Client with tenant-scoped queries.
type tenantQuerier struct {
	client *duckhttpclient.Client
	alias  string
}

// Alias returns the DuckDB database alias for this tenant.
func (q *tenantQuerier) Alias() string {
	return q.alias
}

func (q *tenantQuerier) QueryContext(ctx context.Context, query string, args ...any) (*stypes.QueryResult, error) {
	result, err := q.client.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &stypes.QueryResult{Columns: result.Columns, Rows: result.Rows}, nil
}

func (q *tenantQuerier) ExecContext(ctx context.Context, query string, args ...any) (int64, error) {
	return q.client.Exec(ctx, query, args...)
}

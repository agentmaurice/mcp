//go:build !duckdb

package duckhttp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	stypes "github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage/types"
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
		// schema_version table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT now(),
			description VARCHAR
		)`, alias),

		// sources table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.sources (
			source_id VARCHAR PRIMARY KEY,
			system VARCHAR NOT NULL,
			author VARCHAR,
			timestamp TIMESTAMP NOT NULL,
			trace_id VARCHAR,
			raw JSON,
			created_at TIMESTAMP NOT NULL DEFAULT now()
		)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_sources_system ON %s.sources(system)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_sources_trace ON %s.sources(trace_id)`, alias),

		// entities table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.entities (
			entity_id VARCHAR PRIMARY KEY,
			entity_type VARCHAR NOT NULL,
			external_id VARCHAR NOT NULL,
			name VARCHAR,
			attributes JSON,
			status VARCHAR,
			created_at TIMESTAMP NOT NULL DEFAULT now(),
			updated_at TIMESTAMP NOT NULL DEFAULT now(),
			last_source_id VARCHAR,
			CONSTRAINT uq_entity_external UNIQUE(entity_type, external_id)
		)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_entities_type ON %s.entities(entity_type)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_entities_name ON %s.entities(name)`, alias),

		// facts table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.facts (
			fact_id VARCHAR PRIMARY KEY,
			fact_type VARCHAR NOT NULL,
			subject_entity_id VARCHAR,
			subject_ref JSON,
			payload JSON NOT NULL,
			effective_at TIMESTAMP NOT NULL,
			ingested_at TIMESTAMP NOT NULL DEFAULT now(),
			correlates_to VARCHAR,
			source_id VARCHAR,
			is_deleted BOOLEAN NOT NULL DEFAULT false
		)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_facts_type ON %s.facts(fact_type)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_facts_effective ON %s.facts(effective_at)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_facts_subject ON %s.facts(subject_entity_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_facts_correlates ON %s.facts(correlates_to)`, alias),

		// documents table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.documents (
			document_id VARCHAR PRIMARY KEY,
			uri VARCHAR NOT NULL,
			mime_type VARCHAR NOT NULL,
			title VARCHAR,
			tags JSON,
			metadata JSON,
			created_at TIMESTAMP NOT NULL DEFAULT now(),
			source_id VARCHAR
		)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_documents_mime ON %s.documents(mime_type)`, alias),

		// document_links table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.document_links (
			link_id VARCHAR PRIMARY KEY,
			document_id VARCHAR NOT NULL,
			entity_id VARCHAR,
			fact_id VARCHAR,
			relationship VARCHAR,
			created_at TIMESTAMP NOT NULL DEFAULT now()
		)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_doclinks_doc ON %s.document_links(document_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_doclinks_ent ON %s.document_links(entity_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_doclinks_fact ON %s.document_links(fact_id)`, alias),

		// pii_policy table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.pii_policy (
			policy_id VARCHAR PRIMARY KEY,
			column_path VARCHAR NOT NULL,
			action VARCHAR NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT now()
		)`, alias),

		// Views (migration 1)
		fmt.Sprintf(`CREATE VIEW IF NOT EXISTS %s.v_customers AS
			SELECT
				entity_id AS customer_id,
				external_id AS customer_external_id,
				name,
				attributes,
				status,
				created_at,
				updated_at
			FROM %s.entities
			WHERE entity_type = 'customer'`, alias, alias),

		fmt.Sprintf(`CREATE VIEW IF NOT EXISTS %s.v_employees AS
			SELECT
				entity_id AS employee_id,
				external_id AS employee_external_id,
				name,
				attributes,
				status,
				created_at,
				updated_at
			FROM %s.entities
			WHERE entity_type = 'employee'`, alias, alias),

		fmt.Sprintf(`CREATE VIEW IF NOT EXISTS %s.v_facts_enriched AS
			SELECT
				f.fact_id,
				f.fact_type,
				f.effective_at,
				f.ingested_at,
				f.payload,
				f.correlates_to,
				f.is_deleted,
				e.entity_id AS subject_entity_id,
				e.entity_type AS subject_entity_type,
				e.external_id AS subject_external_id,
				e.name AS subject_name,
				s.system AS source_system,
				s.author AS source_author,
				s.timestamp AS source_timestamp,
				s.trace_id AS source_trace_id
			FROM %s.facts f
			LEFT JOIN %s.entities e ON e.entity_id = f.subject_entity_id
			LEFT JOIN %s.sources s ON s.source_id = f.source_id`, alias, alias, alias, alias),

		fmt.Sprintf(`CREATE VIEW IF NOT EXISTS %s.v_documents AS
			SELECT
				d.document_id,
				d.uri,
				d.mime_type,
				d.title,
				d.tags,
				d.metadata,
				d.created_at,
				s.system AS source_system,
				s.timestamp AS source_timestamp
			FROM %s.documents d
			LEFT JOIN %s.sources s ON s.source_id = d.source_id`, alias, alias, alias),

		// Migration 2: writers table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.writers (
			writer_id VARCHAR PRIMARY KEY,
			app_id VARCHAR NOT NULL,
			actor_id VARCHAR NOT NULL,
			actor_type VARCHAR NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT now()
		)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_writers_app ON %s.writers(app_id)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_writers_actor ON %s.writers(actor_id)`, alias),

		// links table
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.links (
			link_id VARCHAR PRIMARY KEY,
			link_type VARCHAR NOT NULL,
			from_kind VARCHAR NOT NULL,
			from_entity_id VARCHAR,
			from_fact_id VARCHAR,
			from_document_id VARCHAR,
			from_ref VARCHAR,
			to_kind VARCHAR NOT NULL,
			to_entity_id VARCHAR,
			to_fact_id VARCHAR,
			to_document_id VARCHAR,
			to_ref VARCHAR,
			attributes JSON,
			created_at TIMESTAMP NOT NULL DEFAULT now(),
			source_id VARCHAR,
			writer_id VARCHAR,
			is_deleted BOOLEAN NOT NULL DEFAULT false
		)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_links_type ON %s.links(link_type)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_links_from ON %s.links(from_kind)`, alias),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_links_to ON %s.links(to_kind)`, alias),
	}

	for _, migration := range migrations {
		if _, err := m.client.Exec(ctx, migration); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, migration)
		}
	}

	// Ensure migration 2 columns exist on entities table
	if err := m.ensureColumn(ctx, alias, "entities", "status", "VARCHAR"); err != nil {
		return err
	}
	if err := m.ensureColumn(ctx, alias, "entities", "last_writer_id", "VARCHAR"); err != nil {
		return err
	}
	if err := m.ensureColumn(ctx, alias, "entities", "is_deleted", "BOOLEAN DEFAULT false"); err != nil {
		return err
	}
	if err := m.ensureIndex(ctx, alias, "idx_entities_updated", "entities(updated_at)"); err != nil {
		return err
	}

	// Ensure migration 2 columns exist on facts table
	if err := m.ensureColumn(ctx, alias, "facts", "dedupe_key", "VARCHAR"); err != nil {
		return err
	}
	if err := m.ensureColumn(ctx, alias, "facts", "writer_id", "VARCHAR"); err != nil {
		return err
	}
	if err := m.ensureColumn(ctx, alias, "facts", "is_deleted", "BOOLEAN DEFAULT false"); err != nil {
		return err
	}
	if err := m.ensureIndex(ctx, alias, "idx_facts_dedupe", "facts(dedupe_key)"); err != nil {
		return err
	}

	// Ensure migration 2 columns exist on documents table
	if err := m.ensureColumn(ctx, alias, "documents", "metadata", "JSON"); err != nil {
		return err
	}
	if err := m.ensureColumn(ctx, alias, "documents", "writer_id", "VARCHAR"); err != nil {
		return err
	}
	if err := m.ensureColumn(ctx, alias, "documents", "is_deleted", "BOOLEAN DEFAULT false"); err != nil {
		return err
	}
	if err := m.ensureIndex(ctx, alias, "idx_documents_created", "documents(created_at)"); err != nil {
		return err
	}

	// Update views for migration 2
	viewStatements := []string{
		fmt.Sprintf(`CREATE OR REPLACE VIEW %s.v_entities AS
			SELECT
				entity_id,
				entity_type,
				external_id,
				name,
				status,
				attributes,
				created_at,
				updated_at
			FROM %s.entities
			WHERE is_deleted = false`, alias, alias),
		fmt.Sprintf(`CREATE OR REPLACE VIEW %s.v_facts_enriched AS
			SELECT
				f.fact_id,
				f.fact_type,
				f.effective_at,
				f.ingested_at,
				f.payload,
				f.correlates_to,
				f.dedupe_key,
				e.entity_id AS subject_entity_id,
				e.entity_type AS subject_entity_type,
				e.external_id AS subject_external_id,
				e.name AS subject_name,
				s.system AS source_system,
				s.author AS source_author,
				s.timestamp AS source_timestamp,
				s.trace_id AS source_trace_id,
				w.app_id AS writer_app_id,
				w.actor_id AS writer_actor_id,
				w.actor_type AS writer_actor_type
			FROM %s.facts f
			LEFT JOIN %s.entities e ON e.entity_id = f.subject_entity_id
			LEFT JOIN %s.sources s ON s.source_id = f.source_id
			LEFT JOIN %s.writers w ON w.writer_id = f.writer_id
			WHERE f.is_deleted = false`, alias, alias, alias, alias, alias),
		fmt.Sprintf(`CREATE OR REPLACE VIEW %s.v_documents AS
			SELECT
				d.document_id,
				d.uri,
				d.mime_type,
				d.title,
				d.tags,
				d.metadata,
				d.created_at,
				s.system AS source_system,
				s.timestamp AS source_timestamp,
				w.app_id AS writer_app_id,
				w.actor_id AS writer_actor_id,
				w.actor_type AS writer_actor_type
			FROM %s.documents d
			LEFT JOIN %s.sources s ON s.source_id = d.source_id
			LEFT JOIN %s.writers w ON w.writer_id = d.writer_id
			WHERE d.is_deleted = false`, alias, alias, alias, alias),
		fmt.Sprintf(`CREATE OR REPLACE VIEW %s.v_links AS
			SELECT
				link_id,
				link_type,
				from_kind,
				COALESCE(from_entity_id, from_fact_id, from_document_id, from_ref) AS from_any,
				to_kind,
				COALESCE(to_entity_id, to_fact_id, to_document_id, to_ref) AS to_any,
				attributes,
				created_at
			FROM %s.links
			WHERE is_deleted = false`, alias, alias),
	}

	for _, stmt := range viewStatements {
		if _, err := m.client.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("failed to apply views: %w", err)
		}
	}

	return nil
}

func (m *Manager) ensureColumn(ctx context.Context, alias, table, column, columnType string) error {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema='%s' AND table_name = '%s' AND column_name = '%s'`,
		alias, table, column)
	result, err := m.client.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to check column %s.%s: %w", table, column, err)
	}
	if len(result.Rows) > 0 {
		if v, ok := result.Rows[0]["COUNT(*)"].(float64); ok && v > 0 {
			return nil
		}
	}
	if _, err := m.client.Exec(ctx, fmt.Sprintf("ALTER TABLE %s.%s ADD COLUMN %s %s", alias, table, column, columnType)); err != nil {
		return fmt.Errorf("failed to add column %s.%s: %w", table, column, err)
	}
	return nil
}

func (m *Manager) ensureIndex(ctx context.Context, alias, indexName, expr string) error {
	if _, err := m.client.Exec(ctx, fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s.%s", indexName, alias, expr)); err != nil {
		return fmt.Errorf("failed to ensure index %s: %w", indexName, err)
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

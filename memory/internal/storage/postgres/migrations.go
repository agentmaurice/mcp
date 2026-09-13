package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// EnsureSchema ensures schema exists and migrations are applied.
func EnsureSchema(ctx context.Context, db *sql.DB, schema string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s", quoteIdent(schema))); err != nil {
		return fmt.Errorf("failed to set search_path: %w", err)
	}

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		description TEXT
	);`); err != nil {
		return fmt.Errorf("failed to create schema_version: %w", err)
	}

	var version sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_version").Scan(&version); err != nil {
		return fmt.Errorf("failed to read schema version: %w", err)
	}

	current := int64(0)
	if version.Valid {
		current = version.Int64
	}

	if current < 1 {
		if err := applyMigration1(ctx, conn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_version (version, description) VALUES (1, 'initial schema')"); err != nil {
			return fmt.Errorf("failed to insert schema version: %w", err)
		}
	}

	if current < 2 {
		if err := applyMigration2(ctx, conn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_version (version, description) VALUES (2, 'kernel schema v2')"); err != nil {
			return fmt.Errorf("failed to insert schema version: %w", err)
		}
	}

	if current < 3 {
		if err := applyMigration3(ctx, conn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_version (version, description) VALUES (3, 'private ingest receipts and observations view')"); err != nil {
			return fmt.Errorf("failed to insert schema version: %w", err)
		}
	}

	return nil
}

func applyMigration1(ctx context.Context, conn *sql.Conn) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sources (
			source_id TEXT PRIMARY KEY,
			system TEXT NOT NULL,
			author TEXT,
			timestamp TIMESTAMPTZ NOT NULL,
			trace_id TEXT,
			raw JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sources_system ON sources(system);`,
		`CREATE INDEX IF NOT EXISTS idx_sources_trace ON sources(trace_id);`,

		`CREATE TABLE IF NOT EXISTS entities (
			entity_id TEXT PRIMARY KEY,
			entity_type TEXT NOT NULL,
			external_id TEXT NOT NULL,
			name TEXT,
			attributes JSONB,
			status TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_source_id TEXT,
			CONSTRAINT uq_entity_external UNIQUE(entity_type, external_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(entity_type);`,
		`CREATE INDEX IF NOT EXISTS idx_entities_name ON entities(name);`,

		`CREATE TABLE IF NOT EXISTS facts (
			fact_id TEXT PRIMARY KEY,
			fact_type TEXT NOT NULL,
			subject_entity_id TEXT,
			subject_ref JSONB,
			payload JSONB NOT NULL,
			effective_at TIMESTAMPTZ NOT NULL,
			ingested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			correlates_to TEXT,
			source_id TEXT,
			is_deleted BOOLEAN NOT NULL DEFAULT false
		);`,
		`CREATE INDEX IF NOT EXISTS idx_facts_type ON facts(fact_type);`,
		`CREATE INDEX IF NOT EXISTS idx_facts_effective ON facts(effective_at);`,
		`CREATE INDEX IF NOT EXISTS idx_facts_subject ON facts(subject_entity_id);`,
		`CREATE INDEX IF NOT EXISTS idx_facts_correlates ON facts(correlates_to);`,

		`CREATE TABLE IF NOT EXISTS documents (
			document_id TEXT PRIMARY KEY,
			uri TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			title TEXT,
			tags JSONB,
			metadata JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			source_id TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_documents_mime ON documents(mime_type);`,

		`CREATE TABLE IF NOT EXISTS document_links (
			link_id TEXT PRIMARY KEY,
			document_id TEXT NOT NULL,
			entity_id TEXT,
			fact_id TEXT,
			relationship TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_doclinks_doc ON document_links(document_id);`,
		`CREATE INDEX IF NOT EXISTS idx_doclinks_ent ON document_links(entity_id);`,
		`CREATE INDEX IF NOT EXISTS idx_doclinks_fact ON document_links(fact_id);`,

		`CREATE TABLE IF NOT EXISTS pii_policy (
			policy_id TEXT PRIMARY KEY,
			column_path TEXT NOT NULL,
			action TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,

		`CREATE OR REPLACE VIEW v_customers AS
			SELECT
				entity_id AS customer_id,
				external_id AS customer_external_id,
				name,
				attributes,
				status,
				created_at,
				updated_at
			FROM entities
			WHERE entity_type = 'customer';`,

		`CREATE OR REPLACE VIEW v_employees AS
			SELECT
				entity_id AS employee_id,
				external_id AS employee_external_id,
				name,
				attributes,
				status,
				created_at,
				updated_at
			FROM entities
			WHERE entity_type = 'employee';`,

		`CREATE OR REPLACE VIEW v_facts_enriched AS
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
			FROM facts f
			LEFT JOIN entities e ON e.entity_id = f.subject_entity_id
			LEFT JOIN sources s ON s.source_id = f.source_id;`,

		`CREATE OR REPLACE VIEW v_documents AS
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
			FROM documents d
			LEFT JOIN sources s ON s.source_id = d.source_id;`,
	}

	for _, stmt := range statements {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to apply migration: %w", err)
		}
	}

	return nil
}

func applyMigration2(ctx context.Context, conn *sql.Conn) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS writers (
			writer_id TEXT PRIMARY KEY,
			app_id TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			actor_type TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_writers_app ON writers(app_id);`,
		`CREATE INDEX IF NOT EXISTS idx_writers_actor ON writers(actor_id);`,

		`CREATE TABLE IF NOT EXISTS links (
			link_id TEXT PRIMARY KEY,
			link_type TEXT NOT NULL,
			from_kind TEXT NOT NULL,
			from_entity_id TEXT,
			from_fact_id TEXT,
			from_document_id TEXT,
			from_ref TEXT,
			to_kind TEXT NOT NULL,
			to_entity_id TEXT,
			to_fact_id TEXT,
			to_document_id TEXT,
			to_ref TEXT,
			attributes JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			source_id TEXT,
			writer_id TEXT,
			is_deleted BOOLEAN NOT NULL DEFAULT false
		);`,
		`CREATE INDEX IF NOT EXISTS idx_links_type ON links(link_type);`,
		`CREATE INDEX IF NOT EXISTS idx_links_from ON links(from_kind);`,
		`CREATE INDEX IF NOT EXISTS idx_links_to ON links(to_kind);`,
	}

	for _, stmt := range statements {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to apply migration2: %w", err)
		}
	}

	// entities columns
	if err := ensureColumn(ctx, conn, "entities", "status", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "entities", "last_writer_id", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "entities", "is_deleted", "BOOLEAN DEFAULT false"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, conn, "idx_entities_updated", "entities(updated_at)"); err != nil {
		return err
	}

	// facts columns
	if err := ensureColumn(ctx, conn, "facts", "dedupe_key", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "facts", "writer_id", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "facts", "is_deleted", "BOOLEAN DEFAULT false"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, conn, "idx_facts_dedupe", "facts(dedupe_key)"); err != nil {
		return err
	}

	// documents columns
	if err := ensureColumn(ctx, conn, "documents", "metadata", "JSONB"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "documents", "writer_id", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, conn, "documents", "is_deleted", "BOOLEAN DEFAULT false"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, conn, "idx_documents_created", "documents(created_at)"); err != nil {
		return err
	}

	// Views (kernel stable)
	viewStatements := []string{
		`CREATE OR REPLACE VIEW v_entities AS
			SELECT
				entity_id,
				entity_type,
				external_id,
				name,
				status,
				attributes,
				created_at,
				updated_at
			FROM entities
			WHERE is_deleted = false;`,
		`CREATE OR REPLACE VIEW v_facts_enriched AS
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
			FROM facts f
			LEFT JOIN entities e ON e.entity_id = f.subject_entity_id
			LEFT JOIN sources s ON s.source_id = f.source_id
			LEFT JOIN writers w ON w.writer_id = f.writer_id
			WHERE f.is_deleted = false;`,
		`CREATE OR REPLACE VIEW v_documents AS
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
			FROM documents d
			LEFT JOIN sources s ON s.source_id = d.source_id
			LEFT JOIN writers w ON w.writer_id = d.writer_id
			WHERE d.is_deleted = false;`,
		`CREATE OR REPLACE VIEW v_links AS
			SELECT
				link_id,
				link_type,
				from_kind,
				COALESCE(from_entity_id, from_fact_id, from_document_id, from_ref) AS from_any,
				to_kind,
				COALESCE(to_entity_id, to_fact_id, to_document_id, to_ref) AS to_any,
				attributes,
				created_at
			FROM links
			WHERE is_deleted = false;`,
	}
	for _, stmt := range viewStatements {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to apply views: %w", err)
		}
	}

	return nil
}

func applyMigration3(ctx context.Context, conn *sql.Conn) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ingest_receipts (
			receipt_id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			privacy_mode TEXT NOT NULL,
			privacy_tags JSONB,
			redacted_paths JSONB,
			target_type TEXT NOT NULL,
			target_ids JSONB,
			payload_hash TEXT NOT NULL,
			raw_persisted BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_ingest_receipts_tenant ON ingest_receipts(tenant_id);`,
		`CREATE INDEX IF NOT EXISTS idx_ingest_receipts_kind ON ingest_receipts(kind);`,
		`CREATE OR REPLACE VIEW v_observations AS
			SELECT
				f.fact_id,
				f.fact_type,
				f.effective_at,
				f.payload->>'title' AS title,
				f.payload->>'summary' AS summary,
				f.payload->>'confidence' AS confidence,
				f.payload->'labels' AS labels,
				e.entity_id AS subject_entity_id,
				e.name AS subject_name,
				s.system AS source_system
			FROM facts f
			LEFT JOIN entities e ON e.entity_id = f.subject_entity_id
			LEFT JOIN sources s ON s.source_id = f.source_id
			WHERE f.is_deleted = false
			  AND f.fact_type LIKE 'observation.%';`,
	}

	for _, stmt := range statements {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to apply migration3: %w", err)
		}
	}

	return nil
}

func ensureColumn(ctx context.Context, conn *sql.Conn, table string, column string, columnType string) error {
	var count int
	err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
		table, column,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check column %s.%s: %w", table, column, err)
	}
	if count > 0 {
		return nil
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, columnType)); err != nil {
		return fmt.Errorf("failed to add column %s.%s: %w", table, column, err)
	}
	return nil
}

func ensureIndex(ctx context.Context, conn *sql.Conn, indexName string, expr string) error {
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s", indexName, expr)); err != nil {
		return fmt.Errorf("failed to ensure index %s: %w", indexName, err)
	}
	return nil
}

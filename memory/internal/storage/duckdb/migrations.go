//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
)

// EnsureSchema ensures schema exists and migrations are applied.
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT now(),
		description VARCHAR
	);`); err != nil {
		return fmt.Errorf("failed to create schema_version: %w", err)
	}

	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_version").Scan(&version); err != nil {
		return fmt.Errorf("failed to read schema version: %w", err)
	}

	current := int64(0)
	if version.Valid {
		current = version.Int64
	}

	if current < 1 {
		if err := applyMigration1(ctx, db); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO schema_version (version, description) VALUES (1, 'initial schema')"); err != nil {
			return fmt.Errorf("failed to insert schema version: %w", err)
		}
	}

	if current < 2 {
		if err := applyMigration2(ctx, db); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO schema_version (version, description) VALUES (2, 'kernel schema v2')"); err != nil {
			return fmt.Errorf("failed to insert schema version: %w", err)
		}
	}

	if current < 3 {
		if err := applyMigration3(ctx, db); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO schema_version (version, description) VALUES (3, 'private ingest receipts and observations view')"); err != nil {
			return fmt.Errorf("failed to insert schema version: %w", err)
		}
	}

	return nil
}

func applyMigration1(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sources (
			source_id VARCHAR PRIMARY KEY,
			system VARCHAR NOT NULL,
			author VARCHAR,
			timestamp TIMESTAMP NOT NULL,
			trace_id VARCHAR,
			raw JSON,
			created_at TIMESTAMP NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sources_system ON sources(system);`,
		`CREATE INDEX IF NOT EXISTS idx_sources_trace ON sources(trace_id);`,

		`CREATE TABLE IF NOT EXISTS entities (
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
		);`,
		`CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(entity_type);`,
		`CREATE INDEX IF NOT EXISTS idx_entities_name ON entities(name);`,

		`CREATE TABLE IF NOT EXISTS facts (
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
		);`,
		`CREATE INDEX IF NOT EXISTS idx_facts_type ON facts(fact_type);`,
		`CREATE INDEX IF NOT EXISTS idx_facts_effective ON facts(effective_at);`,
		`CREATE INDEX IF NOT EXISTS idx_facts_subject ON facts(subject_entity_id);`,
		`CREATE INDEX IF NOT EXISTS idx_facts_correlates ON facts(correlates_to);`,

		`CREATE TABLE IF NOT EXISTS documents (
			document_id VARCHAR PRIMARY KEY,
			uri VARCHAR NOT NULL,
			mime_type VARCHAR NOT NULL,
			title VARCHAR,
			tags JSON,
			metadata JSON,
			created_at TIMESTAMP NOT NULL DEFAULT now(),
			source_id VARCHAR
		);`,
		`CREATE INDEX IF NOT EXISTS idx_documents_mime ON documents(mime_type);`,

		`CREATE TABLE IF NOT EXISTS document_links (
			link_id VARCHAR PRIMARY KEY,
			document_id VARCHAR NOT NULL,
			entity_id VARCHAR,
			fact_id VARCHAR,
			relationship VARCHAR,
			created_at TIMESTAMP NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_doclinks_doc ON document_links(document_id);`,
		`CREATE INDEX IF NOT EXISTS idx_doclinks_ent ON document_links(entity_id);`,
		`CREATE INDEX IF NOT EXISTS idx_doclinks_fact ON document_links(fact_id);`,

		`CREATE TABLE IF NOT EXISTS pii_policy (
			policy_id VARCHAR PRIMARY KEY,
			column_path VARCHAR NOT NULL,
			action VARCHAR NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT now()
		);`,

		`CREATE VIEW IF NOT EXISTS v_customers AS
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

		`CREATE VIEW IF NOT EXISTS v_employees AS
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

		`CREATE VIEW IF NOT EXISTS v_facts_enriched AS
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

		`CREATE VIEW IF NOT EXISTS v_documents AS
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
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to apply migration: %w", err)
		}
	}

	return nil
}

func applyMigration2(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS writers (
			writer_id VARCHAR PRIMARY KEY,
			app_id VARCHAR NOT NULL,
			actor_id VARCHAR NOT NULL,
			actor_type VARCHAR NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_writers_app ON writers(app_id);`,
		`CREATE INDEX IF NOT EXISTS idx_writers_actor ON writers(actor_id);`,

		`CREATE TABLE IF NOT EXISTS links (
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
		);`,
		`CREATE INDEX IF NOT EXISTS idx_links_type ON links(link_type);`,
		`CREATE INDEX IF NOT EXISTS idx_links_from ON links(from_kind);`,
		`CREATE INDEX IF NOT EXISTS idx_links_to ON links(to_kind);`,
	}

	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to apply migration2: %w", err)
		}
	}

	// entities columns
	if err := ensureColumn(ctx, db, "entities", "status", "VARCHAR"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "entities", "last_writer_id", "VARCHAR"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "entities", "is_deleted", "BOOLEAN DEFAULT false"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, db, "idx_entities_updated", "entities(updated_at)"); err != nil {
		return err
	}

	// facts columns
	if err := ensureColumn(ctx, db, "facts", "dedupe_key", "VARCHAR"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "facts", "writer_id", "VARCHAR"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "facts", "is_deleted", "BOOLEAN DEFAULT false"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, db, "idx_facts_dedupe", "facts(dedupe_key)"); err != nil {
		return err
	}

	// documents columns
	if err := ensureColumn(ctx, db, "documents", "metadata", "JSON"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "documents", "writer_id", "VARCHAR"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "documents", "is_deleted", "BOOLEAN DEFAULT false"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, db, "idx_documents_created", "documents(created_at)"); err != nil {
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
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to apply views: %w", err)
		}
	}

	return nil
}

func applyMigration3(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ingest_receipts (
			receipt_id VARCHAR PRIMARY KEY,
			tenant_id VARCHAR NOT NULL,
			kind VARCHAR NOT NULL,
			privacy_mode VARCHAR NOT NULL,
			privacy_tags JSON,
			redacted_paths JSON,
			target_type VARCHAR NOT NULL,
			target_ids JSON,
			payload_hash VARCHAR NOT NULL,
			raw_persisted BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMP NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_ingest_receipts_tenant ON ingest_receipts(tenant_id);`,
		`CREATE INDEX IF NOT EXISTS idx_ingest_receipts_kind ON ingest_receipts(kind);`,
		`CREATE OR REPLACE VIEW v_observations AS
			SELECT
				f.fact_id,
				f.fact_type,
				f.effective_at,
				json_extract_string(f.payload, '$.title') AS title,
				json_extract_string(f.payload, '$.summary') AS summary,
				json_extract_string(f.payload, '$.confidence') AS confidence,
				json_extract(f.payload, '$.labels') AS labels,
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
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to apply migration3: %w", err)
		}
	}

	return nil
}

func ensureColumn(ctx context.Context, db *sql.DB, table string, column string, columnType string) error {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='main' AND table_name = ? AND column_name = ?`,
		table, column,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check column %s.%s: %w", table, column, err)
	}
	if count > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, columnType)); err != nil {
		return fmt.Errorf("failed to add column %s.%s: %w", table, column, err)
	}
	return nil
}

func ensureIndex(ctx context.Context, db *sql.DB, indexName string, expr string) error {
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s", indexName, expr)); err != nil {
		return fmt.Errorf("failed to ensure index %s: %w", indexName, err)
	}
	return nil
}

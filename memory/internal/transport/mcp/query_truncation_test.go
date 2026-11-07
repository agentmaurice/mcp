//go:build duckdb

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

func newMemoryQueryEnv(t *testing.T) (*config.Config, storage.Manager) {
	t.Helper()
	cfg := testConfig(t.TempDir())
	cfg.Security.RequireViewsOnly = false
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return cfg, manager
}

// seedEntities inserts n entities named prefix-1..prefix-n into tenantID.
func seedEntities(t *testing.T, manager storage.Manager, tenantID, prefix string, n int) {
	t.Helper()
	err := manager.WithWriter(context.Background(), tenantID, func(ctx context.Context, q storage.Querier) error {
		for i := 1; i <= n; i++ {
			name := fmt.Sprintf("%s-%d", prefix, i)
			if _, err := q.ExecContext(ctx,
				"INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
				xid.New().String(), "customer", name, name, time.Now().UTC(), time.Now().UTC()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed %s: %v", tenantID, err)
	}
}

func TestSingleRowQueryIsNotReportedAsTruncated(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	const tenant = "tenant-one-row"
	seedEntities(t, manager, tenant, "solo", 1)

	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT name FROM entities", nil, 500, 2000, tenant)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Truncated {
		t.Fatal("a one-row result must not be reported as truncated")
	}
	if tenantEmptyFlag(result) || len(result.Warnings) != 0 {
		t.Fatalf("tenant with data must not warn: empty=%v warnings=%v", tenantEmptyFlag(result), result.Warnings)
	}
}

func TestRowCapReportsTruncationAndDropsOnlyTheExtraRow(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	const tenant = "tenant-cap"
	seedEntities(t, manager, tenant, "cap", 3)

	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT name FROM entities", nil, 2, 2000, tenant)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 2 || !result.Truncated {
		t.Fatalf("cap of 2 over 3 rows: rows=%d truncated=%v, want 2 and true", len(result.Rows), result.Truncated)
	}
	if result.QueryStats.RowCount != 2 {
		t.Fatalf("rowCount = %d, want 2", result.QueryStats.RowCount)
	}
}

func TestExactlyCapRowsIsNotTruncated(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	const tenant = "tenant-exact"
	seedEntities(t, manager, tenant, "exact", 2)

	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT name FROM entities", nil, 2, 2000, tenant)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 2 || result.Truncated {
		t.Fatalf("2 rows under a cap of 2: rows=%d truncated=%v, want 2 and false", len(result.Rows), result.Truncated)
	}
}

func TestExplicitLimitIsNotTruncation(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	const tenant = "tenant-limit"
	seedEntities(t, manager, tenant, "lim", 3)

	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT name FROM entities LIMIT 2", nil, 10, 2000, tenant)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 2 || result.Truncated {
		t.Fatalf("explicit LIMIT 2: rows=%d truncated=%v, want 2 and false", len(result.Rows), result.Truncated)
	}
}

func TestEmptyTenantWarnsWithoutExposingOtherTenants(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	seedEntities(t, manager, "tenant-plein", "plein", 1)

	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT name FROM entities", nil, 500, 2000, "tenant-vide")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Fatalf("empty tenant returned %d rows", len(result.Rows))
	}
	if !tenantEmptyFlag(result) {
		t.Fatal("tenantEmpty must be true for a tenant without entity, document or fact")
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "no entities, documents or facts") {
		t.Fatalf("empty tenant warning missing: %v", result.Warnings)
	}
	if strings.Contains(result.Warnings[0], "tenant-plein") {
		t.Fatalf("warning leaks another tenant: %q", result.Warnings[0])
	}
}

// Review case: an aggregate over an empty tenant returns one row (total = 0). The
// tenant has no data, so the response is flagged whatever the number of rows.
func TestAggregateOnEmptyTenantIsFlagged(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT COUNT(*) AS total FROM entities", nil, 500, 2000, "tenant-agregat")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("aggregate must return one row, got %d", len(result.Rows))
	}
	if !tenantEmptyFlag(result) {
		t.Fatal("tenant_empty must be true for a tenant without entity, document or fact")
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != emptyTenantWarning {
		t.Fatalf("warnings = %v, want [%q]", result.Warnings, emptyTenantWarning)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(payload), `"tenant_empty":true`) {
		t.Fatalf("metadata field tenant_empty missing from %s", payload)
	}
}

// A tenant with data gets no warning, even for an aggregate.
func TestAggregateOnPopulatedTenantHasNoWarning(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	const tenant = "tenant-plein-agregat"
	seedEntities(t, manager, tenant, "agg", 2)
	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT COUNT(*) AS total FROM entities", nil, 500, 2000, tenant)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if tenantEmptyFlag(result) || len(result.Warnings) != 0 {
		t.Fatalf("populated tenant must not be flagged: tenant_empty=%v warnings=%v", tenantEmptyFlag(result), result.Warnings)
	}
}

// The zero-row warning describes the tenant store, not the result.
func TestEmptyTenantWarningDescribesStoreOnly(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT name FROM entities", nil, 500, 2000, "tenant-vide-seul")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != emptyTenantWarning {
		t.Fatalf("expected the empty-tenant warning, got %v", result.Warnings)
	}
}

// tenantEmptyFlag reads tenant_empty as a bool; null (check failed) reads as false.
func tenantEmptyFlag(result *shared.QueryResult) bool {
	return result.TenantEmpty != nil && *result.TenantEmpty
}

// checkFailingManager fails the tenant emptiness check only: the query itself succeeds.
type checkFailingManager struct{ storage.Manager }

func (m checkFailingManager) OpenTenant(ctx context.Context, tenantID string) (storage.Querier, error) {
	q, err := m.Manager.OpenTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return checkFailingQuerier{q}, nil
}

type checkFailingQuerier struct{ storage.Querier }

func (q checkFailingQuerier) QueryContext(ctx context.Context, query string, args ...any) (*storage.QueryResult, error) {
	if strings.Contains(query, "EXISTS (SELECT 1 FROM entities") {
		return nil, context.DeadlineExceeded
	}
	return q.Querier.QueryContext(ctx, query, args...)
}

// Review case: the main query succeeds, the emptiness check fails. The failure must
// not be swallowed: tenant_empty is null and the warning says the check failed.
func TestFailedEmptinessCheckIsReported(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	const tenant = "tenant-check-echec"
	seedEntities(t, manager, tenant, "chk", 2)
	result, err := executeQuery(context.Background(), checkFailingManager{manager}, cfg, zap.NewNop(), "SELECT name FROM entities", nil, 500, 2000, tenant)
	if err != nil {
		t.Fatalf("the main query must still succeed: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	if result.TenantEmpty != nil {
		t.Fatalf("tenant_empty must be unknown (nil) when the check fails, got %v", *result.TenantEmpty)
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "tenant emptiness check failed") && strings.Contains(w, "context deadline exceeded") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warning about the failed check missing: %v", result.Warnings)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(payload), `"tenant_empty":null`) {
		t.Fatalf("tenant_empty must serialize as null: %s", payload)
	}
}

// Review case: a tenant whose entity, document and fact are all soft-deleted is empty.
// The emptiness check must not count deleted rows.
func TestTenantWithOnlyDeletedRowsIsEmpty(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	const tenant = "tenant-supprime"
	err := manager.WithWriter(context.Background(), tenant, func(ctx context.Context, q storage.Querier) error {
		now := time.Now().UTC()
		if _, err := q.ExecContext(ctx,
			"INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at, is_deleted) VALUES (?, 'customer', 'ext-del', 'supprimé', ?, ?, true)",
			"e-del", now, now); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx,
			"INSERT INTO documents (document_id, uri, mime_type, title, created_at, is_deleted) VALUES ('d-del', 'u-del', 'text/markdown', 'supprimé', ?, true)",
			now); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx,
			"INSERT INTO facts (fact_id, fact_type, subject_entity_id, payload, effective_at, ingested_at, source_id, is_deleted) VALUES ('f-del', 'decision', 'e-del', '{}', ?, ?, 's', true)",
			now, now)
		return err
	})
	if err != nil {
		t.Fatalf("seed deleted rows: %v", err)
	}
	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT COUNT(*) AS total FROM entities", nil, 500, 2000, tenant)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if !tenantEmptyFlag(result) {
		t.Fatalf("a tenant whose rows are all soft-deleted must be flagged empty: tenant_empty=%v", result.TenantEmpty)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != emptyTenantWarning {
		t.Fatalf("warnings = %v, want [%q]", result.Warnings, emptyTenantWarning)
	}
}

// Review case: an entity whose is_deleted is NULL is invisible, as in the schema views
// (WHERE is_deleted = false). With no other data, the tenant is reported empty, and the
// view does not return the row.
func TestNullDeletedFlagIsInvisibleLikeViews(t *testing.T) {
	cfg, manager := newMemoryQueryEnv(t)
	const tenant = "tenant-null-flag"
	err := manager.WithWriter(context.Background(), tenant, func(ctx context.Context, q storage.Querier) error {
		now := time.Now().UTC()
		_, err := q.ExecContext(ctx,
			"INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at, is_deleted) VALUES ('e-null', 'customer', 'ext-null', 'invisible', ?, ?, NULL)",
			now, now)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT name FROM v_entities", nil, 500, 2000, tenant)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Fatalf("the view must not return a NULL is_deleted row, got %d", len(result.Rows))
	}
	if !tenantEmptyFlag(result) || len(result.Warnings) != 1 || result.Warnings[0] != emptyTenantWarning {
		t.Fatalf("a tenant whose only row is invisible must be flagged empty: tenant_empty=%v warnings=%v", result.TenantEmpty, result.Warnings)
	}
}

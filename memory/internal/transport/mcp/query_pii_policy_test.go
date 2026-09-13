//go:build duckdb

package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

func TestPIIPolicyRedaction(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.Security.RedactionKeys = []string{}
	cfg.Security.RequireViewsOnly = true

	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	ctx := context.Background()
	tenantID := cfg.Storage.DefaultTenantID

	err = manager.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		if _, err := q.ExecContext(ctx,
			"INSERT INTO pii_policy (policy_id, column_path, action) VALUES (?, ?, ?)",
			xid.New().String(), "entities.attributes.email", "redact"); err != nil {
			return err
		}
		attrs := map[string]any{"email": "contact@acme.fr", "phone": "123"}
		attrJSON, _ := json.Marshal(attrs)
		if _, err := q.ExecContext(ctx,
			"INSERT INTO entities (entity_id, entity_type, external_id, name, attributes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			xid.New().String(), "customer", "CUST-1", "ACME", string(attrJSON), time.Now().UTC(), time.Now().UTC()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	result, err := executeQuery(ctx, manager, cfg, zap.NewNop(), "SELECT attributes FROM v_entities", nil, 10, 2000, tenantID)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) == 0 {
		t.Fatalf("expected rows")
	}
	var obj map[string]any
	switch v := result.Rows[0][0].(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &obj); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
	case map[string]any:
		obj = v
	default:
		t.Fatalf("unexpected attributes type: %T", v)
	}
	if obj["email"] != cfg.Security.RedactValue {
		t.Fatalf("expected email redacted")
	}
}

func TestPolicyColumnPathTwoSegments(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.Security.RedactionKeys = []string{}
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	ctx := context.Background()
	tenantID := cfg.Storage.DefaultTenantID

	err = manager.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		if _, err := q.ExecContext(ctx,
			"INSERT INTO pii_policy (policy_id, column_path, action) VALUES (?, ?, ?)",
			xid.New().String(), "entities.name", "drop"); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx,
			"INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			xid.New().String(), "customer", "CUST-2", "Secret", time.Now().UTC(), time.Now().UTC()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	result, err := executeQuery(ctx, manager, cfg, zap.NewNop(), "SELECT name FROM v_entities", nil, 10, 2000, tenantID)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) == 0 {
		t.Fatalf("expected rows")
	}
	if result.Rows[0][0] != nil {
		t.Fatalf("expected name to be dropped")
	}
}

func TestPolicyNestedPath(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.Security.RedactionKeys = []string{}
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	ctx := context.Background()
	tenantID := cfg.Storage.DefaultTenantID

	err = manager.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
		if _, err := q.ExecContext(ctx,
			"INSERT INTO pii_policy (policy_id, column_path, action) VALUES (?, ?, ?)",
			xid.New().String(), "entities.attributes.contact.email", "hash"); err != nil {
			return err
		}
		attrs := map[string]any{"contact": map[string]any{"email": "contact@acme.fr"}}
		attrJSON, _ := json.Marshal(attrs)
		if _, err := q.ExecContext(ctx,
			"INSERT INTO entities (entity_id, entity_type, external_id, name, attributes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			xid.New().String(), "customer", "CUST-3", "ACME", string(attrJSON), time.Now().UTC(), time.Now().UTC()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	result, err := executeQuery(ctx, manager, cfg, zap.NewNop(), "SELECT attributes FROM v_entities", nil, 10, 2000, tenantID)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) == 0 {
		t.Fatalf("expected rows")
	}
	var obj map[string]any
	switch v := result.Rows[0][0].(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &obj); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
	case map[string]any:
		obj = v
	default:
		t.Fatalf("unexpected attributes type: %T", v)
	}
	contact, _ := obj["contact"].(map[string]any)
	if contact == nil || contact["email"] == "contact@acme.fr" {
		t.Fatalf("expected nested email to be hashed")
	}
}

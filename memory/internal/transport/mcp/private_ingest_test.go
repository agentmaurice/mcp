//go:build duckdb

package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

func TestPrivateIngestSanitizesFactAndStoresReceipt(t *testing.T) {
	cfg := testConfig(t.TempDir())
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}

	tool := NewPrivateIngestTool(manager, cfg, zap.NewNop())
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: tool.Name(),
		Arguments: map[string]any{
			"tenant_id": "test-tenant",
			"kind":      "fact",
			"payload": map[string]any{
				"factType": "observation.decision",
				"payload": map[string]any{
					"title":   "Auth backend choice",
					"summary": "Switch to memory-backed auth cache",
					"email":   "admin@example.com",
				},
				"effectiveAt": now,
				"source": map[string]any{
					"system":    "test",
					"timestamp": now,
				},
				"writer": map[string]any{
					"appId":   "brain",
					"actorId": "tester",
				},
			},
			"privacy": map[string]any{
				"mode":        "redact",
				"field_paths": []string{"payload.email"},
				"tags":        []string{"pii"},
			},
		},
	}}

	res, err := tool.Handler()(ctx, req)
	if err != nil {
		t.Fatalf("private ingest failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("private ingest returned error: %#v", res.Content)
	}

	payload, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload type: %T", res.StructuredContent)
	}
	receiptID, _ := payload["receipt_id"].(string)
	if receiptID == "" {
		t.Fatalf("expected receipt_id")
	}

	db, err := manager.OpenTenant(ctx, "test-tenant")
	if err != nil {
		t.Fatalf("failed to open tenant: %v", err)
	}

	qr, err := db.QueryContext(ctx, "SELECT payload FROM facts WHERE fact_type = ? LIMIT 1", "observation.decision")
	if err != nil {
		t.Fatalf("failed to query facts: %v", err)
	}
	if len(qr.Rows) != 1 {
		t.Fatalf("expected one fact row, got %d", len(qr.Rows))
	}
	factPayload := parseJSONValue(qr.Rows[0]["payload"])
	payloadMap, ok := factPayload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected fact payload type: %T", factPayload)
	}
	if payloadMap["email"] != "[REDACTED]" {
		t.Fatalf("expected redacted email, got %#v", payloadMap["email"])
	}

	receipts, err := db.QueryContext(ctx, "SELECT privacy_mode, raw_persisted FROM ingest_receipts WHERE receipt_id = ? LIMIT 1", receiptID)
	if err != nil {
		t.Fatalf("failed to query receipts: %v", err)
	}
	if len(receipts.Rows) != 1 {
		t.Fatalf("expected one receipt row, got %d", len(receipts.Rows))
	}
	if receipts.Rows[0]["privacy_mode"] != "redact" {
		t.Fatalf("unexpected privacy mode: %#v", receipts.Rows[0]["privacy_mode"])
	}
	if receipts.Rows[0]["raw_persisted"] != false {
		t.Fatalf("expected raw_persisted=false, got %#v", receipts.Rows[0]["raw_persisted"])
	}
}

func TestObservationsSearchReturnsObservationFacts(t *testing.T) {
	cfg := testConfig(t.TempDir())
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}

	appendTool := NewAppendFactTool(manager, cfg, zap.NewNop())
	searchTool := NewObservationsSearchTool(manager, cfg, nil, zap.NewNop())
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	appendReq := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: appendTool.Name(),
		Arguments: map[string]any{
			"tenant_id": "test-tenant",
			"factType":  "observation.gotcha",
			"payload": map[string]any{
				"title":   "DuckDB extension loading",
				"summary": "vss must be installed before vector queries",
				"labels":  []string{"duckdb", "vector"},
			},
			"effectiveAt": now,
			"source": map[string]any{
				"system":    "test",
				"timestamp": now,
			},
			"writer": map[string]any{
				"appId":   "brain",
				"actorId": "tester",
			},
		},
	}}

	appendRes, err := appendTool.Handler()(ctx, appendReq)
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}
	if appendRes.IsError {
		t.Fatalf("append returned error: %#v", appendRes.Content)
	}

	searchReq := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: searchTool.Name(),
		Arguments: map[string]any{
			"tenant_id": "test-tenant",
			"query":     "extension loading",
			"limit":     5,
		},
	}}

	searchRes, err := searchTool.Handler()(ctx, searchReq)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("search returned error: %#v", searchRes.Content)
	}

	payload, ok := searchRes.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload type: %T", searchRes.StructuredContent)
	}
	items, ok := payload["items"].([]map[string]any)
	if !ok {
		rawItems, ok := payload["items"].([]any)
		if !ok {
			t.Fatalf("unexpected items type: %T", payload["items"])
		}
		if len(rawItems) == 0 {
			t.Fatalf("expected at least one observation")
		}
		first, ok := rawItems[0].(map[string]any)
		if !ok {
			t.Fatalf("unexpected observation type: %T", rawItems[0])
		}
		if !strings.Contains(first["summary"].(string), "vector queries") {
			t.Fatalf("unexpected summary: %#v", first["summary"])
		}
		return
	}
	if len(items) == 0 {
		t.Fatalf("expected at least one observation")
	}
}

func TestSanitizePrivatePayloadHashAndDropFields(t *testing.T) {
	payload := map[string]any{
		"attributes": map[string]any{
			"ssn":   "123-45-6789",
			"email": "person@example.com",
		},
	}

	sanitized, paths, _, err := sanitizePrivatePayload("entity", payload, privacyInput{
		Mode:       "hash",
		FieldPaths: []string{"attributes.ssn"},
	})
	if err != nil {
		t.Fatalf("sanitize hash failed: %v", err)
	}
	attrs := sanitized["attributes"].(map[string]any)
	if attrs["ssn"] == "123-45-6789" {
		t.Fatalf("expected hashed ssn")
	}
	if len(paths) != 1 || paths[0] != "attributes.ssn" {
		t.Fatalf("unexpected redacted paths: %#v", paths)
	}

	sanitized, _, _, err = sanitizePrivatePayload("entity", payload, privacyInput{
		Mode:       "drop_fields",
		FieldPaths: []string{"attributes.email"},
	})
	if err != nil {
		t.Fatalf("sanitize drop failed: %v", err)
	}
	attrs = sanitized["attributes"].(map[string]any)
	if _, ok := attrs["email"]; ok {
		t.Fatalf("expected email to be dropped")
	}
}

func TestPrivateIngestEphemeralEntityStoresMinimalAttributes(t *testing.T) {
	cfg := testConfig(t.TempDir())
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}

	tool := NewPrivateIngestTool(manager, cfg, zap.NewNop())
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: tool.Name(),
		Arguments: map[string]any{
			"tenant_id": "test-tenant",
			"kind":      "entity",
			"payload": map[string]any{
				"entityType": "customer",
				"items": []any{
					map[string]any{
						"externalId": "CUST-EPH",
						"name":       "Secret Customer",
						"attributes": map[string]any{"email": "secret@example.com"},
					},
				},
				"source": map[string]any{
					"system":    "test",
					"timestamp": now,
				},
				"writer": map[string]any{
					"appId":   "memory",
					"actorId": "tester",
				},
			},
			"privacy": map[string]any{
				"mode": "ephemeral",
			},
		},
	}}

	res, err := tool.Handler()(ctx, req)
	if err != nil {
		t.Fatalf("private ingest failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("private ingest returned error: %#v", res.Content)
	}

	db, err := manager.OpenTenant(ctx, "test-tenant")
	if err != nil {
		t.Fatalf("failed to open tenant: %v", err)
	}

	qr, err := db.QueryContext(ctx, "SELECT name, attributes FROM entities WHERE entity_type = ? AND external_id = ? LIMIT 1", "customer", "CUST-EPH")
	if err != nil {
		t.Fatalf("failed to query entity: %v", err)
	}
	if len(qr.Rows) != 1 {
		t.Fatalf("expected one entity row, got %d", len(qr.Rows))
	}
	if name := qr.Rows[0]["name"]; name != "" {
		t.Fatalf("expected empty name for ephemeral entity, got %#v", name)
	}
	attrs := parseJSONValue(qr.Rows[0]["attributes"]).(map[string]any)
	if attrs["_ephemeral"] != true {
		t.Fatalf("expected _ephemeral marker, got %#v", attrs)
	}
}

func TestPrivateIngestRejectsMultipleEntityItems(t *testing.T) {
	cfg := testConfig(t.TempDir())
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}

	tool := NewPrivateIngestTool(manager, cfg, zap.NewNop())
	now := time.Now().UTC().Format(time.RFC3339)
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: tool.Name(),
		Arguments: map[string]any{
			"tenant_id": "test-tenant",
			"kind":      "entity",
			"payload": map[string]any{
				"entityType": "customer",
				"items": []any{
					map[string]any{"externalId": "A"},
					map[string]any{"externalId": "B"},
				},
				"source": map[string]any{"system": "test", "timestamp": now},
				"writer": map[string]any{"appId": "memory", "actorId": "tester"},
			},
			"privacy": map[string]any{"mode": "redact"},
		},
	}}

	res, err := tool.Handler()(context.Background(), req)
	if err != nil {
		t.Fatalf("private ingest failed: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected multiple item validation error")
	}
}

func TestObservationsSearchFiltersFactTypes(t *testing.T) {
	cfg := testConfig(t.TempDir())
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}

	appendTool := NewAppendFactTool(manager, cfg, zap.NewNop())
	searchTool := NewObservationsSearchTool(manager, cfg, nil, zap.NewNop())
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	for _, factType := range []string{"observation.gotcha", "observation.decision"} {
		res, err := appendTool.Handler()(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
			Name: appendTool.Name(),
			Arguments: map[string]any{
				"tenant_id": "test-tenant",
				"factType":  factType,
				"payload": map[string]any{
					"title":   factType,
					"summary": "summary for " + factType,
				},
				"effectiveAt": now,
				"source":      map[string]any{"system": "test", "timestamp": now},
				"writer":      map[string]any{"appId": "brain", "actorId": "tester"},
			},
		}})
		if err != nil {
			t.Fatalf("append failed: %v", err)
		}
		if res.IsError {
			t.Fatalf("append returned error: %#v", res.Content)
		}
	}

	res, err := searchTool.Handler()(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: searchTool.Name(),
		Arguments: map[string]any{
			"tenant_id":  "test-tenant",
			"fact_types": []string{"observation.decision"},
			"limit":      10,
		},
	}})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("search returned error: %#v", res.Content)
	}

	payload := res.StructuredContent.(map[string]any)
	var item map[string]any
	switch items := payload["items"].(type) {
	case []map[string]any:
		if len(items) != 1 {
			t.Fatalf("expected one filtered observation, got %d", len(items))
		}
		item = items[0]
	case []any:
		if len(items) != 1 {
			t.Fatalf("expected one filtered observation, got %d", len(items))
		}
		item = items[0].(map[string]any)
	default:
		t.Fatalf("unexpected items type: %T", payload["items"])
	}
	if item["fact_type"] != "observation.decision" {
		t.Fatalf("unexpected filtered fact type: %#v", item["fact_type"])
	}
}

//go:build duckdb

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Markers that must never reach a log above Debug: they sit in the SQL text, in a
// named parameter and in the arguments of every tool.
const (
	sqlMarker   = "MARKER_SQL_7f3a"
	paramMarker = "MARKER_PARAM_91c2"
	argMarker   = "MARKER_ARG_55de"
)

func logsContaining(logs *observer.ObservedLogs, marker string, minLevel zapcore.Level) []string {
	var hits []string
	for _, entry := range logs.All() {
		if entry.Level < minLevel {
			continue
		}
		encoded, _ := json.Marshal(entry.ContextMap())
		if strings.Contains(entry.Message, marker) || strings.Contains(string(encoded), marker) {
			hits = append(hits, entry.Level.String()+" "+entry.Message)
		}
	}
	return hits
}

// callEveryLoggingPath runs memory.query (success, validation failure, forbidden table,
// execution failure, bad arguments), memory.facts.append, memory.entities.upsert and
// memory.views.create_or_replace with the markers in their payloads.
func callEveryLoggingPath(t *testing.T, logger *zap.Logger) {
	t.Helper()
	cfg, manager := newMemoryQueryEnv(t)
	cfg.Security.RequireViewsOnly = true
	const tenant = "tenant-logs"
	seedEntities(t, manager, tenant, "alice", 2)
	ctx := context.Background()
	call := func(tool interface {
		Handler() ToolHandler
	}, name string, args map[string]any) {
		request := mcp.CallToolRequest{}
		request.Params.Name = name
		request.Params.Arguments = args
		if _, err := tool.Handler()(ctx, request); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	query := NewQueryTool(manager, cfg, nil, logger)
	call(query, "memory.query", map[string]any{"tenant_id": tenant, "sql": "SELECT name, '" + sqlMarker + "' AS m FROM v_customers WHERE name = @who", "params": map[string]any{"who": paramMarker}})
	call(query, "memory.query", map[string]any{"tenant_id": tenant, "sql": "DELETE FROM entities WHERE name = '" + sqlMarker + "'"})
	call(query, "memory.query", map[string]any{"tenant_id": tenant, "sql": "SELECT '" + sqlMarker + "' FROM entities"})
	call(query, "memory.query", map[string]any{"tenant_id": tenant, "sql": "SELECT '" + sqlMarker + "' FROM v_customers WHERE nonexistent_column = 1"})
	// A DuckDB conversion error quotes the offending value: only its class may be logged.
	call(query, "memory.query", map[string]any{"tenant_id": tenant, "sql": "SELECT CAST('" + sqlMarker + "' AS INTEGER) AS m FROM v_customers"})
	call(query, "memory.query", map[string]any{"tenant_id": tenant, "sql": 42, "extra": argMarker})

	src := map[string]any{"system": "test", "timestamp": "2026-10-23T10:00:00Z"}
	wr := map[string]any{"appId": "app", "actorId": "actor"}
	call(NewAppendFactTool(manager, cfg, logger), "memory.facts.append", map[string]any{
		"tenant_id": tenant, "factType": "decision", "effectiveAt": "2026-10-23T10:00:00Z",
		"dedupeKey": "password=" + argMarker, "payload": map[string]any{"text": argMarker}, "subject": map[string]any{"externalId": argMarker, "entityType": "customer"},
		"source": src, "writer": wr})
	call(NewAppendFactTool(manager, cfg, logger), "memory.facts.append", map[string]any{"tenant_id": tenant, "factType": 7, "payload": argMarker})
	// A date that does not parse: the parser's error quotes it.
	call(NewAppendFactTool(manager, cfg, logger), "memory.facts.append", map[string]any{
		"tenant_id": tenant, "factType": "decision", "effectiveAt": "password=" + argMarker, "payload": map[string]any{"a": 1},
		"source": src, "writer": wr})
	call(NewUpsertEntitiesTool(manager, cfg, logger), "memory.entities.upsert", map[string]any{
		"tenant_id": tenant, "entityType": "customer", "items": []map[string]any{{"externalId": "x1", "name": argMarker}},
		"source": src, "writer": wr})
	call(NewUpsertEntitiesTool(manager, cfg, logger), "memory.entities.upsert", map[string]any{"tenant_id": tenant, "entityType": 9, "items": argMarker})
	views := NewViewsCreateTool(manager, cfg, logger)
	call(views, "memory.views.create_or_replace", map[string]any{"tenant_id": tenant, "appId": "app", "viewName": "app_v_logs", "sql": "SELECT '" + sqlMarker + "' AS m FROM v_customers"})
	call(views, "memory.views.create_or_replace", map[string]any{"tenant_id": tenant, "appId": "app", "viewName": "app_v_bad", "sql": "SELECT '" + sqlMarker + "' FROM entities"})
	call(views, "memory.views.create_or_replace", map[string]any{"tenant_id": tenant, "appId": 3, "viewName": argMarker})
}

// At Info, no log of memory.query or of the write tools holds the SQL, a parameter or an
// argument: only sizes and technical identifiers (AGENT-1093, rule of AGENT-1081).
func TestNoPayloadInLogsAboveDebug(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel) // records every level, to check each entry's level
	callEveryLoggingPath(t, zap.New(core))
	if logs.Len() == 0 {
		t.Fatal("no log recorded")
	}
	for _, marker := range []string{sqlMarker, paramMarker, argMarker} {
		if hits := logsContaining(logs, marker, zapcore.InfoLevel); len(hits) != 0 {
			t.Errorf("marker %s reached a log at Info or above: %v", marker, hits)
		}
	}
	var sizes bool
	for _, entry := range logs.All() {
		if entry.Level == zapcore.InfoLevel && entry.Message == "tool called" {
			ctx := entry.ContextMap()
			if _, ok := ctx["arguments_bytes"]; !ok || ctx["tool"] == nil {
				t.Errorf("Info line %q lacks the tool name or the size of the arguments: %v", entry.Message, ctx)
			}
			sizes = true
		}
	}
	if !sizes {
		t.Fatal("no \"tool called\" line recorded")
	}
}

// At Debug the content is available for diagnosis, masked and bounded.
func TestPayloadIsAvailableAtDebug(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	callEveryLoggingPath(t, zap.New(core))
	if len(logsContaining(logs, sqlMarker, zapcore.DebugLevel)) == 0 {
		t.Fatal("the SQL text is not available at Debug")
	}
}

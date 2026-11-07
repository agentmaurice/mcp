package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/logging"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/security"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/agentmaurice/mcpchatui/mcp/shared/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
)

// sqlValidationCache caches parsed SQL validation results to avoid re-parsing identical statements.
var sqlValidationCache = security.NewValidationCache(512)

func executeQuery(ctx context.Context, storage storage.Manager, cfg *config.Config, logger *zap.Logger, sqlText string, params map[string]any, maxRows int, timeoutMs int, tenantID string) (*shared.QueryResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	logging.Debug(logger, "executeQuery sql", "sql", sqlText, zap.String("tenant_id", tenantID))
	logging.DebugValue(logger, "executeQuery params", "params", params)
	logger.Debug("executeQuery called",
		zap.String("tenant_id", tenantID),
		logging.Size("sql", sqlText),
		logging.SizeOf("params", params),
		zap.Int("maxRows", maxRows),
		zap.Int("timeoutMs", timeoutMs),
	)

	if len(params) > 0 && storage != nil && strings.ToLower(storage.Dialect()) == "postgres" {
		logger.Warn("query parameters not supported for postgres backend", logging.SizeOf("params", params))
		return nil, fmt.Errorf("query parameters are not supported for postgres backend")
	}

	if maxRows <= 0 {
		maxRows = cfg.Query.MaxRowsDefault
	}
	if maxRows > cfg.Query.MaxRowsLimit {
		maxRows = cfg.Query.MaxRowsLimit
	}

	if timeoutMs <= 0 {
		timeoutMs = cfg.Query.TimeoutMsDefault
	}
	if timeoutMs > cfg.Query.TimeoutMsMax {
		timeoutMs = cfg.Query.TimeoutMsMax
	}

	// memory.query.validate covers the guardrails up to the allowed objects;
	// a rejected query ends it in error.
	_, validateSpan := telemetry.Tracer().Start(ctx, "memory.query.validate")
	validated := false
	defer func() {
		if !validated {
			validateSpan.SetStatus(codes.Error, "query rejected")
			validateSpan.End()
		}
	}()

	if err := security.ValidateReadOnly(sqlText, cfg.Security.Denylist); err != nil {
		logger.Warn("query validation failed: read-only check", logging.ErrorClass(err), logging.Size("sql", sqlText))
		return nil, err
	}

	rules := security.PortableSQLRules{
		AllowSelectStar:    !cfg.Query.DisallowSelectStar,
		AllowJSONOperators: false,
	}

	var info *security.SQLInfo
	if cached, ok := sqlValidationCache.Get(sqlText, rules); ok {
		validateSpan.SetAttributes(attribute.Bool("memory.query.validation_cached", true))
		if cached.Err != nil {
			return nil, cached.Err
		}
		info = cached.Info
	} else {
		var err error
		info, err = security.ValidatePortableSQL(sqlText, rules)
		sqlValidationCache.Put(sqlText, rules, security.ValidationResult{Info: info, Err: err})
		if err != nil {
			logger.Warn("query validation failed: portable SQL check", logging.ErrorClass(err), logging.Size("sql", sqlText))
			return nil, err
		}
	}

	sqlText = strings.TrimSpace(sqlText)
	// Ask for one row beyond the cap. A row past the cap is the only proof that
	// rows were dropped; EnsureLimit's "modified" flag only says a LIMIT was added
	// or lowered, which is not a truncation.
	sqlText, _ = security.EnsureLimit(sqlText, maxRows+1)

	tables := info.Tables
	if len(tables) == 0 {
		tables = security.ExtractTableNames(sqlText)
	}
	if cfg.Security.RequireViewsOnly || len(cfg.Security.AllowedObjects) > 0 {
		for _, table := range tables {
			lower := strings.ToLower(table)
			if cfg.Security.RequireViewsOnly && !(strings.HasPrefix(lower, "v_") || strings.HasPrefix(lower, "app_")) {
				logger.Warn("table not allowed (views only)", logging.Text("table", table), logging.Size("sql", sqlText))
				return nil, fmt.Errorf("table %s is not allowed (views only)", table)
			}
			if !security.IsAllowedObject(table, cfg.Security.AllowedObjects) {
				logger.Warn("object not allowed", logging.Text("table", table), zap.Strings("allowed", cfg.Security.AllowedObjects))
				return nil, fmt.Errorf("object %s is not allowed", table)
			}
		}
	}
	validated = true
	validateSpan.End()

	if tenantID == "" {
		identity := shared.IdentityFromContext(ctx)
		tenantID = identity.TenantID
	}
	if tenantID == "" {
		tenantID = cfg.Storage.DefaultTenantID
	}

	logger.Debug("opening tenant database", zap.String("tenant_id", tenantID))
	db, err := storage.OpenTenant(ctx, tenantID)
	if err != nil {
		logger.Error("failed to open tenant database", logging.ErrorClass(err), zap.String("tenant_id", tenantID))
		return nil, err
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	args := make([]any, 0, len(params))
	for key, val := range params {
		args = append(args, sql.Named(key, val))
	}

	logging.Debug(logger, "executing SQL query", "sql", sqlText)
	logger.Debug("executing SQL query",
		logging.Size("sql", sqlText),
		zap.Int("timeout_ms", timeoutMs),
		zap.Int("param_count", len(args)),
	)

	start := time.Now()
	qr, err := db.QueryContext(ctxTimeout, sqlText, args...)
	if err != nil {
		logger.Error("SQL query execution failed",
			logging.ErrorClass(err),
			logging.Size("sql", sqlText),
			zap.String("tenant_id", tenantID),
			zap.Duration("elapsed", time.Since(start)),
		)
		logging.DebugError(logger, "SQL query execution error text", err)
		return nil, err
	}

	columns := qr.Columns
	colMeta := make([]shared.Column, len(columns))
	for i, name := range columns {
		colMeta[i] = shared.Column{Name: name}
	}

	var resultRows [][]any
	for _, row := range qr.Rows {
		values := make([]any, len(columns))
		for i, col := range columns {
			values[i] = normalizeValue(row[col])
		}
		resultRows = append(resultRows, values)
	}
	rowCapReached := false
	if len(resultRows) > maxRows {
		resultRows = resultRows[:maxRows]
		rowCapReached = true
	}

	logger.Debug("query executed successfully",
		zap.Int("row_count", len(resultRows)),
		zap.Int("column_count", len(columns)),
		zap.Duration("elapsed", time.Since(start)),
	)

	if len(cfg.Security.AllowedColumns) > 0 {
		colMeta, resultRows = applyAllowedColumns(colMeta, resultRows, cfg.Security.AllowedColumns, tables, logger)
	}

	policies := fetchPolicyRules(ctx, db, logger)
	if len(policies) > 0 {
		security.ApplyPolicyRules(colMeta, resultRows, policies, cfg.Security.RedactValue)
	}

	if len(cfg.Security.RedactionKeys) > 0 {
		security.RedactResult(colMeta, resultRows, cfg.Security.RedactionKeys, cfg.Security.RedactValue)
	}

	result := &shared.QueryResult{
		Columns:   colMeta,
		Rows:      resultRows,
		Truncated: rowCapReached,
		QueryStats: shared.QueryStats{
			ElapsedMs: time.Since(start).Milliseconds(),
			RowCount:  len(resultRows),
		},
	}
	// Any query on a tenant without entity, document or fact is flagged, whatever the
	// number of rows: an aggregate such as COUNT(*) returns one row that reflects the
	// empty tenant.
	// A failed check is not a guess: tenant_empty becomes null (unknown) and the
	// warning says why.
	switch empty, checkErr := tenantDataCheck(ctx, db); {
	case checkErr != nil:
		result.TenantEmpty = nil
		result.Warnings = append(result.Warnings, fmt.Sprintf("tenant emptiness check failed: %v", checkErr))
	case empty:
		flag := true
		result.TenantEmpty = &flag
		result.Warnings = append(result.Warnings, emptyTenantWarning)
	default:
		flag := false
		result.TenantEmpty = &flag
	}

	if cfg.Query.MaxResponseBytes > 0 {
		if truncated := enforceResponseSize(result, cfg.Query.MaxResponseBytes); truncated {
			result.Truncated = true
		}
	}

	return result, nil
}

// emptyTenantWarning is the warning for a tenant without entity, document or fact.
const emptyTenantWarning = "the tenant has no entities, documents or facts ; the query result reflects an empty tenant"

// tenantDataCheck reports whether the tenant has no entity, document or fact. The
// check only reads the tenant store, so it never reveals other tenants. Any failure
// (cancelled context, SQL error, unexpected shape) is returned: the caller must not
// guess.
func tenantDataCheck(ctx context.Context, db storage.Querier) (bool, error) {
	// Soft-deleted rows do not count, and a NULL flag is invisible too, as in the schema
	// views (WHERE is_deleted = false): a tenant whose rows are all deleted or NULL is empty.
	qr, err := db.QueryContext(ctx, `SELECT (EXISTS (SELECT 1 FROM entities WHERE is_deleted = false) OR EXISTS (SELECT 1 FROM documents WHERE is_deleted = false) OR EXISTS (SELECT 1 FROM facts WHERE is_deleted = false)) AS has_data`)
	if err != nil {
		return false, err
	}
	if qr == nil || len(qr.Rows) == 0 {
		return false, fmt.Errorf("no row returned")
	}
	hasData, ok := qr.Rows[0]["has_data"].(bool)
	if !ok {
		return false, fmt.Errorf("unexpected has_data value %T", qr.Rows[0]["has_data"])
	}
	return !hasData, nil
}

func enforceResponseSize(result *shared.QueryResult, maxBytes int) bool {
	if result == nil || maxBytes <= 0 {
		return false
	}
	data, err := json.Marshal(result)
	if err != nil {
		return false
	}
	if len(data) <= maxBytes {
		return false
	}

	truncated := false
	for len(result.Rows) > 0 {
		result.Rows = result.Rows[:len(result.Rows)-1]
		truncated = true
		data, err = json.Marshal(result)
		if err != nil {
			return truncated
		}
		if len(data) <= maxBytes {
			return truncated
		}
	}
	return true
}

func normalizeValue(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case []byte:
		return string(v)
	case time.Time:
		return v.UTC().Format(time.RFC3339)
	default:
		return v
	}
}

func applyAllowedColumns(columns []shared.Column, rows [][]any, allowed map[string][]string, tables []string, logger *zap.Logger) ([]shared.Column, [][]any) {
	if len(allowed) == 0 {
		return columns, rows
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	allowedList, ok := allowed["*"]
	if !ok && len(tables) == 1 {
		normalized := normalizeTableName(tables[0])
		if list, found := allowed[normalized]; found {
			allowedList = list
			ok = true
		} else if list, found := allowed[strings.ToLower(normalized)]; found {
			allowedList = list
			ok = true
		}
	}
	if !ok {
		logger.Warn("allowed_columns configured but no matching table or wildcard", zap.Strings("tables", tables))
		return columns, rows
	}

	if len(allowedList) == 0 {
		return []shared.Column{}, [][]any{}
	}

	allowSet := make(map[string]struct{}, len(allowedList))
	for _, col := range allowedList {
		allowSet[strings.ToLower(strings.TrimSpace(col))] = struct{}{}
	}

	var keptIdx []int
	var keptCols []shared.Column
	for i, col := range columns {
		if _, ok := allowSet[strings.ToLower(col.Name)]; ok {
			keptIdx = append(keptIdx, i)
			keptCols = append(keptCols, col)
		}
	}

	if len(keptIdx) == 0 {
		return []shared.Column{}, [][]any{}
	}

	filteredRows := make([][]any, 0, len(rows))
	for _, row := range rows {
		newRow := make([]any, 0, len(keptIdx))
		for _, idx := range keptIdx {
			if idx < len(row) {
				newRow = append(newRow, row[idx])
			} else {
				newRow = append(newRow, nil)
			}
		}
		filteredRows = append(filteredRows, newRow)
	}

	return keptCols, filteredRows
}

func normalizeTableName(name string) string {
	if name == "" {
		return name
	}
	parts := strings.Split(name, ".")
	return parts[len(parts)-1]
}

func fetchPolicyRules(ctx context.Context, db storage.Querier, logger *zap.Logger) []security.PolicyRule {
	if db == nil {
		return nil
	}
	qr, err := db.QueryContext(ctx, "SELECT column_path, action FROM pii_policy")
	if err != nil {
		if logger != nil {
			logger.Debug("pii_policy query failed", logging.ErrorClass(err))
		}
		return nil
	}

	var rules []security.PolicyRule
	for _, row := range qr.Rows {
		path, _ := row["column_path"].(string)
		action, _ := row["action"].(string)
		if path == "" || action == "" {
			continue
		}
		if rule, ok := security.ParsePolicyRule(path, action); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

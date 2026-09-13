package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/security"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
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

	logger.Debug("executeQuery called",
		zap.String("tenant_id", tenantID),
		zap.String("sql", sqlText),
		zap.Any("params", params),
		zap.Int("maxRows", maxRows),
		zap.Int("timeoutMs", timeoutMs),
	)

	if len(params) > 0 && storage != nil && strings.ToLower(storage.Dialect()) == "postgres" {
		logger.Warn("query parameters not supported for postgres backend", zap.Any("params", params))
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

	if err := security.ValidateReadOnly(sqlText, cfg.Security.Denylist); err != nil {
		logger.Warn("query validation failed: read-only check", zap.Error(err), zap.String("sql", sqlText))
		return nil, err
	}

	rules := security.PortableSQLRules{
		AllowSelectStar:    !cfg.Query.DisallowSelectStar,
		AllowJSONOperators: false,
	}

	var info *security.SQLInfo
	if cached, ok := sqlValidationCache.Get(sqlText, rules); ok {
		if cached.Err != nil {
			return nil, cached.Err
		}
		info = cached.Info
	} else {
		var err error
		info, err = security.ValidatePortableSQL(sqlText, rules)
		sqlValidationCache.Put(sqlText, rules, security.ValidationResult{Info: info, Err: err})
		if err != nil {
			logger.Warn("query validation failed: portable SQL check", zap.Error(err), zap.String("sql", sqlText))
			return nil, err
		}
	}

	sqlText = strings.TrimSpace(sqlText)
	sqlText, limitModified := security.EnsureLimit(sqlText, maxRows)

	tables := info.Tables
	if len(tables) == 0 {
		tables = security.ExtractTableNames(sqlText)
	}
	if cfg.Security.RequireViewsOnly || len(cfg.Security.AllowedObjects) > 0 {
		for _, table := range tables {
			lower := strings.ToLower(table)
			if cfg.Security.RequireViewsOnly && !(strings.HasPrefix(lower, "v_") || strings.HasPrefix(lower, "app_")) {
				logger.Warn("table not allowed (views only)", zap.String("table", table), zap.String("sql", sqlText))
				return nil, fmt.Errorf("table %s is not allowed (views only)", table)
			}
			if !security.IsAllowedObject(table, cfg.Security.AllowedObjects) {
				logger.Warn("object not allowed", zap.String("table", table), zap.Strings("allowed", cfg.Security.AllowedObjects))
				return nil, fmt.Errorf("object %s is not allowed", table)
			}
		}
	}

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
		logger.Error("failed to open tenant database", zap.Error(err), zap.String("tenant_id", tenantID))
		return nil, err
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	args := make([]any, 0, len(params))
	for key, val := range params {
		args = append(args, sql.Named(key, val))
	}

	logger.Debug("executing SQL query",
		zap.String("sql", sqlText),
		zap.Int("timeout_ms", timeoutMs),
		zap.Int("param_count", len(args)),
	)

	start := time.Now()
	qr, err := db.QueryContext(ctxTimeout, sqlText, args...)
	if err != nil {
		logger.Error("SQL query execution failed",
			zap.Error(err),
			zap.String("sql", sqlText),
			zap.String("tenant_id", tenantID),
			zap.Duration("elapsed", time.Since(start)),
		)
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
		Truncated: limitModified,
		QueryStats: shared.QueryStats{
			ElapsedMs: time.Since(start).Milliseconds(),
			RowCount:  len(resultRows),
		},
	}

	if cfg.Query.MaxResponseBytes > 0 {
		if truncated := enforceResponseSize(result, cfg.Query.MaxResponseBytes); truncated {
			result.Truncated = true
		}
	}

	return result, nil
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
			logger.Debug("pii_policy query failed", zap.Error(err))
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

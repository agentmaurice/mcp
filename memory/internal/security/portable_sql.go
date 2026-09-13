// Package security provides SQL validation, access control, and PII redaction.
//
// # SQL Parser Limitations
//
// This package uses github.com/xwb1989/sqlparser, which is a MySQL-dialect parser.
// As a result, some valid DuckDB/PostgreSQL syntax is rejected at parse time:
//
//   - CAST(x AS VARCHAR): MySQL uses CHAR instead of VARCHAR in CAST expressions.
//     Workaround: use CAST(x AS CHAR) or the try_cast() function.
//   - ARRAY types and constructors: DuckDB-specific array syntax is not supported.
//   - STRUCT types: DuckDB struct syntax is not parseable.
//   - FILTER clause: SELECT count(*) FILTER (WHERE x > 0) is not supported.
//   - QUALIFY clause: DuckDB window-function filtering is blocked at the text level.
//   - PIVOT/UNPIVOT: DuckDB-specific table transformations are not parseable.
//   - COLUMNS expression: SELECT COLUMNS('pattern') is not parseable.
//   - POSITIONAL JOIN: Not supported by the MySQL parser.
//   - Lambda expressions: list_transform(l, x -> x+1) is not parseable.
//
// These limitations are acceptable for the portable SQL subset because:
//  1. The restricted surface area reduces attack vectors.
//  2. Most analytical queries work fine with the allowed functions.
//  3. Complex transformations should be pre-materialized as views via memory.views.create_or_replace.
package security

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/xwb1989/sqlparser"
)

// PortableSQLRules defines allowed SQL surface.
type PortableSQLRules struct {
	AllowSelectStar    bool
	AllowJSONOperators bool
}

// SQLInfo holds extracted metadata from SQL validation.
type SQLInfo struct {
	Tables []string
}

var forbiddenFragments = []string{
	"information_schema",
	"pg_catalog",
	"pragma",
}

var forbiddenJSONOps = []string{
	"#>",
	"#>>",
	"@>",
	"jsonb_",
}

// ValidatePortableSQL validates a portable SQL subset and returns SQL metadata.
func ValidatePortableSQL(sqlText string, rules PortableSQLRules) (*SQLInfo, error) {
	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		return nil, fmt.Errorf("sql is required")
	}

	lower := strings.ToLower(trimmed)
	if wordMatch(`\bwith\b`, lower) {
		return nil, fmt.Errorf("CTE queries are not allowed")
	}
	if wordMatch(`\bover\b`, lower) {
		return nil, fmt.Errorf("window functions are not allowed")
	}
	for _, fragment := range forbiddenFragments {
		if strings.Contains(lower, fragment) {
			return nil, fmt.Errorf("forbidden reference: %s", fragment)
		}
	}
	if !rules.AllowJSONOperators {
		for _, fragment := range forbiddenJSONOps {
			if strings.Contains(lower, fragment) {
				return nil, fmt.Errorf("json operators are not allowed")
			}
		}
	}

	stmt, err := sqlparser.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid sql: %w", err)
	}

	if err := validateStatement(stmt, rules); err != nil {
		return nil, err
	}

	info := &SQLInfo{}
	seenTables := map[string]struct{}{}

	walkErr := sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		switch n := node.(type) {
		case *sqlparser.Subquery:
			return false, fmt.Errorf("subqueries are not allowed")
		case *sqlparser.Union:
			return false, fmt.Errorf("union queries are not allowed")
		case *sqlparser.JoinTableExpr:
			if !isAllowedJoin(n.Join) {
				return false, fmt.Errorf("join type not allowed")
			}
		case *sqlparser.FuncExpr:
			if !isAllowedFunc(n.Name.String()) {
				return false, fmt.Errorf("function not allowed: %s", n.Name.String())
			}
		case *sqlparser.AliasedTableExpr:
			switch expr := n.Expr.(type) {
			case sqlparser.TableName:
				name, err := extractTableName(expr)
				if err != nil {
					return false, err
				}
				if name != "" {
					if _, ok := seenTables[name]; !ok {
						seenTables[name] = struct{}{}
						info.Tables = append(info.Tables, name)
					}
				}
			case *sqlparser.Subquery:
				return false, fmt.Errorf("subqueries are not allowed")
			}
		case sqlparser.TableName:
			name, err := extractTableName(n)
			if err != nil {
				return false, err
			}
			if name != "" {
				if _, ok := seenTables[name]; !ok {
					seenTables[name] = struct{}{}
					info.Tables = append(info.Tables, name)
				}
			}
		}
		return true, nil
	}, stmt)
	if walkErr != nil {
		return nil, walkErr
	}

	return info, nil
}

func validateStatement(stmt sqlparser.Statement, rules PortableSQLRules) error {
	switch s := stmt.(type) {
	case *sqlparser.Select:
		if strings.TrimSpace(s.Lock) != "" {
			return fmt.Errorf("locking clauses are not allowed")
		}
		if !rules.AllowSelectStar && hasSelectStar(s.SelectExprs) {
			return fmt.Errorf("select star is not allowed")
		}
		return nil
	case *sqlparser.ParenSelect:
		return validateStatement(s.Select, rules)
	case *sqlparser.Union:
		return fmt.Errorf("union queries are not allowed")
	default:
		return fmt.Errorf("only SELECT statements are allowed")
	}
}

func hasSelectStar(exprs sqlparser.SelectExprs) bool {
	for _, expr := range exprs {
		if _, ok := expr.(*sqlparser.StarExpr); ok {
			return true
		}
	}
	return false
}

// allowedFunctions is the set of SQL functions permitted in portable queries.
// Organized by category for clarity.
var allowedFunctions = map[string]struct{}{
	// Aggregates
	"count": {}, "sum": {}, "avg": {}, "min": {}, "max": {},
	"count_star": {}, "group_concat": {}, "string_agg": {},

	// String functions
	"lower": {}, "upper": {}, "trim": {}, "ltrim": {}, "rtrim": {},
	"length": {}, "char_length": {}, "concat": {}, "concat_ws": {},
	"substring": {}, "substr": {}, "replace": {}, "left": {}, "right": {},
	"lpad": {}, "rpad": {}, "reverse": {}, "repeat": {},
	"starts_with": {}, "ends_with": {}, "contains": {},
	"position": {}, "instr": {}, "split_part": {},

	// Numeric functions
	"abs": {}, "ceil": {}, "ceiling": {}, "floor": {}, "round": {},
	"trunc": {}, "truncate": {}, "mod": {}, "power": {}, "sqrt": {},
	"sign": {}, "greatest": {}, "least": {},

	// Date/time functions
	"now": {}, "current_date": {}, "current_timestamp": {},
	"date_trunc": {}, "date_part": {}, "date_diff": {}, "datediff": {},
	"extract": {}, "age": {}, "date_add": {}, "date_sub": {},
	"strftime": {}, "epoch": {}, "epoch_ms": {},
	"year": {}, "month": {}, "day": {}, "hour": {}, "minute": {}, "second": {},
	"make_date": {}, "make_timestamp": {},

	// Type conversion
	"cast": {}, "try_cast": {}, "typeof": {},

	// Null handling
	"coalesce": {}, "nullif": {}, "ifnull": {},

	// Conditional
	"if": {}, "case": {},

	// List/Array (DuckDB)
	"list_value": {}, "list_aggregate": {}, "list_sort": {},
	"list_distinct": {}, "list_unique": {}, "len": {}, "array_length": {},
	"unnest": {}, "generate_series": {},

	// JSON (read-only accessors)
	"json_extract_string": {}, "json_extract": {},
	"json_type": {}, "json_array_length": {}, "json_keys": {},
	"json_valid": {},
}

func isAllowedFunc(name string) bool {
	_, ok := allowedFunctions[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func isAllowedJoin(join string) bool {
	j := strings.ToLower(strings.TrimSpace(join))
	switch j {
	case "", "join", "inner join", "left join", "left outer join":
		return true
	default:
		return false
	}
}

func extractTableName(name sqlparser.TableName) (string, error) {
	if !name.Qualifier.IsEmpty() {
		return "", fmt.Errorf("schema-qualified tables are not allowed")
	}
	return name.Name.String(), nil
}

func wordMatch(pattern string, value string) bool {
	re := regexp.MustCompile(pattern)
	return re.MatchString(value)
}

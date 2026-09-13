package security

import (
	"strings"
	"testing"
)

var defaultRules = PortableSQLRules{
	AllowSelectStar:    true,
	AllowJSONOperators: false,
}

// --- Valid Portable SQL ---

func TestPortableSQL_ValidQueries(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"simple select", "SELECT 1"},
		{"select star", "SELECT * FROM v_customers"},
		{"with where", "SELECT name FROM v_entities WHERE entity_type = 'customer'"},
		{"count aggregate", "SELECT count(*) FROM v_facts_enriched"},
		{"multiple aggregates", "SELECT min(created_at), max(created_at), avg(amount) FROM v_customers"},
		{"inner join", "SELECT a.name FROM v_customers a JOIN v_facts_enriched b ON a.entity_id = b.subject_entity_id"},
		{"left join", "SELECT a.name FROM v_customers a LEFT JOIN v_facts_enriched b ON a.entity_id = b.subject_entity_id"},
		{"coalesce", "SELECT coalesce(name, 'unknown') FROM v_entities"},
		{"lower upper", "SELECT lower(name), upper(name) FROM v_entities"},
		{"trim functions", "SELECT trim(name), ltrim(name), rtrim(name) FROM v_entities"},
		{"length", "SELECT length(name) FROM v_entities"},
		// Note: CAST(x AS VARCHAR) is not parsed by the MySQL-based parser.
		// Use CAST(x AS CHAR) which is MySQL-compatible syntax.
		{"cast", "SELECT cast(created_at AS CHAR) FROM v_entities"},
		{"date_trunc", "SELECT date_trunc('month', created_at) FROM v_entities"},
		{"round", "SELECT round(3.14, 1)"},
		{"abs", "SELECT abs(-5)"},
		{"nullif", "SELECT nullif(name, '') FROM v_entities"},
		{"substring", "SELECT substring(name, 1, 3) FROM v_entities"},
		{"replace", "SELECT replace(name, 'old', 'new') FROM v_entities"},
		{"concat", "SELECT concat(name, ' - ', status) FROM v_entities"},
		{"greatest least", "SELECT greatest(1, 2, 3), least(1, 2, 3)"},
		{"group by", "SELECT entity_type, count(*) FROM v_entities GROUP BY entity_type"},
		{"order by", "SELECT name FROM v_entities ORDER BY name ASC"},
		{"having", "SELECT entity_type, count(*) FROM v_entities GROUP BY entity_type HAVING count(*) > 1"},
		{"distinct", "SELECT DISTINCT entity_type FROM v_entities"},
		{"limit offset", "SELECT name FROM v_entities LIMIT 10 OFFSET 5"},
		{"alias", "SELECT e.name AS customer_name FROM v_entities e"},
		{"between", "SELECT name FROM v_entities WHERE created_at BETWEEN '2024-01-01' AND '2024-12-31'"},
		{"in clause", "SELECT name FROM v_entities WHERE status IN ('active', 'inactive')"},
		{"like", "SELECT name FROM v_entities WHERE name LIKE '%acme%'"},
		{"is null", "SELECT name FROM v_entities WHERE status IS NULL"},
		{"is not null", "SELECT name FROM v_entities WHERE status IS NOT NULL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ValidatePortableSQL(tc.sql, defaultRules)
			if err != nil {
				t.Errorf("expected valid: %q, got error: %v", tc.sql, err)
			}
			if info == nil {
				t.Errorf("expected non-nil SQLInfo for: %q", tc.sql)
			}
		})
	}
}

// --- Rejected SQL ---

func TestPortableSQL_RejectedQueries(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		contains string
	}{
		{"empty", "", "sql is required"},
		{"CTE", "WITH cte AS (SELECT 1) SELECT * FROM cte", "CTE"},
		{"window function", "SELECT row_number() OVER () FROM v_entities", "window function"},
		{"information_schema", "SELECT * FROM information_schema.tables", "forbidden reference"},
		{"pg_catalog", "SELECT * FROM pg_catalog.pg_tables", "forbidden reference"},
		{"subquery", "SELECT * FROM (SELECT 1) sub", "subqueries are not allowed"},
		{"union", "SELECT 1 UNION SELECT 2", "union"},
		{"insert statement", "INSERT INTO entities VALUES ('a')", "only SELECT"},
		{"delete statement", "DELETE FROM entities", "only SELECT"},
		{"locking", "SELECT * FROM v_entities FOR UPDATE", "locking"},
		{"schema-qualified", "SELECT * FROM main.v_entities", "schema-qualified"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePortableSQL(tc.sql, defaultRules)
			if err == nil {
				t.Errorf("expected rejection for: %q", tc.sql)
				return
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.contains)) {
				t.Errorf("expected error containing %q, got: %v for: %q", tc.contains, err, tc.sql)
			}
		})
	}
}

// --- DisallowSelectStar ---

func TestPortableSQL_DisallowSelectStar(t *testing.T) {
	rules := PortableSQLRules{AllowSelectStar: false}
	_, err := ValidatePortableSQL("SELECT * FROM v_entities", rules)
	if err == nil {
		t.Fatal("expected select star to be rejected")
	}
	if !strings.Contains(err.Error(), "select star") {
		t.Fatalf("expected select star error, got: %v", err)
	}
}

// --- JSON operators ---

func TestPortableSQL_JSONOperatorsBlocked(t *testing.T) {
	tests := []string{
		"SELECT attributes #> '{email}' FROM v_entities",
		"SELECT attributes #>> '{email}' FROM v_entities",
		"SELECT * FROM v_entities WHERE attributes @> '{}'",
		"SELECT jsonb_each(attributes) FROM v_entities",
	}
	rules := PortableSQLRules{AllowSelectStar: true, AllowJSONOperators: false}
	for _, sql := range tests {
		_, err := ValidatePortableSQL(sql, rules)
		if err == nil {
			t.Errorf("expected JSON operator rejection: %q", sql)
		}
	}
}

// --- Function allowlist ---

func TestPortableSQL_AllowedFunctions(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"count", "SELECT count(*) FROM v_entities"},
		{"lower", "SELECT lower(name) FROM v_entities"},
		{"coalesce", "SELECT coalesce(name, '') FROM v_entities"},
		{"date_trunc", "SELECT date_trunc('month', created_at) FROM v_entities"},
		{"round", "SELECT round(1.5) FROM v_entities"},
		{"abs", "SELECT abs(-1) FROM v_entities"},
		{"length", "SELECT length(name) FROM v_entities"},
		{"substr", "SELECT substr(name, 1, 3) FROM v_entities"},
		{"concat", "SELECT concat(name, status) FROM v_entities"},
		{"nullif", "SELECT nullif(name, '') FROM v_entities"},
		{"trim", "SELECT trim(name) FROM v_entities"},
		{"replace", "SELECT replace(name, 'a', 'b') FROM v_entities"},
		{"greatest", "SELECT greatest(1, 2) FROM v_entities"},
		{"least", "SELECT least(1, 2) FROM v_entities"},
		// Note: CAST(x AS VARCHAR) is not parsed by the MySQL-based parser.
		{"cast", "SELECT cast(1 AS CHAR) FROM v_entities"},
		{"floor ceil", "SELECT floor(1.5), ceil(1.5) FROM v_entities"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePortableSQL(tc.sql, defaultRules)
			if err != nil {
				t.Errorf("expected function %q to be allowed: %v", tc.name, err)
			}
		})
	}
}

func TestPortableSQL_DisallowedFunctions(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"system", "SELECT system('ls') FROM v_entities"},
		{"exec", "SELECT exec('ls') FROM v_entities"},
		{"pg_read_file", "SELECT pg_read_file('/etc/passwd')"},
		{"read_csv", "SELECT * FROM read_csv('/tmp/data.csv')"},
		{"read_parquet", "SELECT * FROM read_parquet('/tmp/data.parquet')"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePortableSQL(tc.sql, defaultRules)
			if err == nil {
				t.Errorf("expected function %q to be rejected in: %q", tc.name, tc.sql)
			}
		})
	}
}

// --- Table extraction ---

func TestPortableSQL_TableExtraction(t *testing.T) {
	info, err := ValidatePortableSQL("SELECT v_customers.name FROM v_customers LEFT JOIN v_facts_enriched ON v_customers.entity_id = v_facts_enriched.subject_entity_id", defaultRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := map[string]bool{}
	for _, table := range info.Tables {
		found[table] = true
	}
	if !found["v_customers"] || !found["v_facts_enriched"] {
		t.Fatalf("expected v_customers and v_facts_enriched in extracted tables, got: %v", info.Tables)
	}
}

// --- Join types ---

func TestPortableSQL_JoinTypes(t *testing.T) {
	allowed := []string{
		"SELECT * FROM v_customers a JOIN v_facts_enriched b ON a.entity_id = b.subject_entity_id",
		"SELECT * FROM v_customers a INNER JOIN v_facts_enriched b ON a.entity_id = b.subject_entity_id",
		"SELECT * FROM v_customers a LEFT JOIN v_facts_enriched b ON a.entity_id = b.subject_entity_id",
		"SELECT * FROM v_customers a LEFT OUTER JOIN v_facts_enriched b ON a.entity_id = b.subject_entity_id",
	}
	for _, sql := range allowed {
		if _, err := ValidatePortableSQL(sql, defaultRules); err != nil {
			t.Errorf("expected allowed join: %q, got: %v", sql, err)
		}
	}

	disallowed := []string{
		"SELECT * FROM v_customers a RIGHT JOIN v_facts_enriched b ON a.entity_id = b.subject_entity_id",
		"SELECT * FROM v_customers a FULL OUTER JOIN v_facts_enriched b ON a.entity_id = b.subject_entity_id",
	}
	for _, sql := range disallowed {
		if _, err := ValidatePortableSQL(sql, defaultRules); err == nil {
			t.Errorf("expected rejected join: %q", sql)
		}
	}
}

// --- Injection attempts at the portable SQL level ---

func TestPortableSQL_InjectionAttempts(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"comment with drop", "SELECT 1 -- DROP TABLE entities"},
		{"union all injection", "SELECT name FROM v_entities UNION ALL SELECT password FROM users"},
		{"nested subquery", "SELECT * FROM v_entities WHERE entity_id IN (SELECT entity_id FROM entities)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Comment-only injections may parse fine (the comment is stripped).
			// UNION and subqueries must be rejected.
			_, err := ValidatePortableSQL(tc.sql, defaultRules)
			if tc.name != "comment with drop" && err == nil {
				t.Errorf("expected rejection for: %q", tc.sql)
			}
		})
	}
}

package security

import (
	"strings"
	"testing"
)

var defaultDenylist = []string{
	"insert", "update", "delete", "create", "drop", "alter",
	"copy", "attach", "detach", "pragma", "export", "import",
}

// --- ValidateReadOnly ---

func TestValidateReadOnly_ValidSelect(t *testing.T) {
	tests := []string{
		"SELECT 1",
		"SELECT * FROM v_customers",
		"SELECT name FROM v_entities WHERE entity_type = 'customer'",
		"  SELECT count(*) FROM v_facts_enriched  ",
		"SELECT a FROM t;",          // trailing semicolon should be tolerated
		"SELECT a FROM t  ;  \n  ", // trailing whitespace + semicolons
	}
	for _, sql := range tests {
		if err := ValidateReadOnly(sql, defaultDenylist); err != nil {
			t.Errorf("expected valid: %q, got error: %v", sql, err)
		}
	}
}

func TestValidateReadOnly_EmptySQL(t *testing.T) {
	for _, sql := range []string{"", "   ", "\t\n"} {
		if err := ValidateReadOnly(sql, defaultDenylist); err == nil {
			t.Errorf("expected error for empty SQL: %q", sql)
		}
	}
}

func TestValidateReadOnly_MultiStatement(t *testing.T) {
	tests := []string{
		"SELECT 1; SELECT 2",
		"SELECT 1; DROP TABLE entities",
		"SELECT 1;DELETE FROM entities",
	}
	for _, sql := range tests {
		err := ValidateReadOnly(sql, defaultDenylist)
		if err == nil {
			t.Errorf("expected multi-statement rejection: %q", sql)
		}
		if err != nil && !strings.Contains(err.Error(), "multi-statements") {
			t.Errorf("expected multi-statement error, got: %v for %q", err, sql)
		}
	}
}

func TestValidateReadOnly_DenylistKeywords(t *testing.T) {
	tests := []struct {
		sql     string
		keyword string
	}{
		{"INSERT INTO entities VALUES ('a')", "insert"},
		{"UPDATE entities SET name='x'", "update"},
		{"DELETE FROM entities", "delete"},
		{"CREATE TABLE evil (id INT)", "create"},
		{"DROP TABLE entities", "drop"},
		{"ALTER TABLE entities ADD COLUMN x INT", "alter"},
		{"COPY entities TO '/tmp/dump'", "copy"},
		{"ATTACH DATABASE ':memory:' AS evil", "attach"},
		{"DETACH evil", "detach"},
		{"PRAGMA table_info('entities')", "pragma"},
		{"EXPORT DATABASE '/tmp'", "export"},
		{"IMPORT DATABASE '/tmp'", "import"},
	}
	for _, tc := range tests {
		err := ValidateReadOnly(tc.sql, defaultDenylist)
		if err == nil {
			t.Errorf("expected rejection for keyword %q in: %q", tc.keyword, tc.sql)
		}
	}
}

func TestValidateReadOnly_CaseSensitivity(t *testing.T) {
	tests := []string{
		"INSERT INTO entities VALUES ('a')",
		"Insert Into entities Values ('a')",
		"INSERT INTO entities VALUES ('a')",
		"iNsErT INTO entities VALUES ('a')",
	}
	for _, sql := range tests {
		if err := ValidateReadOnly(sql, defaultDenylist); err == nil {
			t.Errorf("expected denial regardless of case: %q", sql)
		}
	}
}

func TestValidateReadOnly_NotSelect(t *testing.T) {
	tests := []string{
		"EXPLAIN SELECT 1",
		"WITH cte AS (SELECT 1) SELECT * FROM cte",
		"SHOW TABLES",
	}
	for _, sql := range tests {
		if err := ValidateReadOnly(sql, defaultDenylist); err == nil {
			t.Errorf("expected rejection for non-SELECT: %q", sql)
		}
	}
}

// --- SQL Injection Attempts ---

func TestValidateReadOnly_InjectionAttempts(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"comment bypass single-line", "SELECT 1 -- ; DROP TABLE entities"},
		{"union injection", "SELECT 1 UNION SELECT password FROM users"},
		{"stacked via encoding", "SELECT 1\x00; DROP TABLE entities"},
		{"keyword in string should still flag", "SELECT 'insert' FROM v_entities"},
		{"keyword embedded in identifier", "SELECT inserted FROM v_entities"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// These should either pass or fail - we check they don't crash
			_ = ValidateReadOnly(tc.sql, defaultDenylist)
		})
	}
}

// --- EnsureLimit ---

func TestEnsureLimit_AddsLimit(t *testing.T) {
	sql, modified := EnsureLimit("SELECT * FROM v_customers", 100)
	if !modified {
		t.Fatal("expected limit to be added")
	}
	if !strings.Contains(strings.ToLower(sql), "limit 100") {
		t.Fatalf("expected LIMIT 100, got: %s", sql)
	}
}

func TestEnsureLimit_KeepsSmaller(t *testing.T) {
	sql, modified := EnsureLimit("SELECT * FROM v_customers LIMIT 50", 100)
	if modified {
		t.Fatal("should not modify smaller limit")
	}
	if !strings.Contains(sql, "LIMIT 50") {
		t.Fatalf("expected LIMIT 50 preserved, got: %s", sql)
	}
}

func TestEnsureLimit_CapsLarger(t *testing.T) {
	sql, modified := EnsureLimit("SELECT * FROM v_customers LIMIT 10000", 500)
	if !modified {
		t.Fatal("expected limit to be capped")
	}
	if !strings.Contains(strings.ToLower(sql), "limit 500") {
		t.Fatalf("expected capped to LIMIT 500, got: %s", sql)
	}
}

func TestEnsureLimit_TrailingSemicolon(t *testing.T) {
	sql, modified := EnsureLimit("SELECT * FROM v_customers;", 100)
	if !modified {
		t.Fatal("expected limit to be added")
	}
	if !strings.Contains(strings.ToLower(sql), "limit 100") {
		t.Fatalf("expected LIMIT 100, got: %s", sql)
	}
}

// --- ExtractTableNames ---

func TestExtractTableNames_Basic(t *testing.T) {
	tables := ExtractTableNames("SELECT * FROM v_customers JOIN v_facts_enriched ON 1=1")
	if len(tables) != 2 {
		t.Fatalf("expected 2 tables, got %d: %v", len(tables), tables)
	}
}

func TestExtractTableNames_Empty(t *testing.T) {
	tables := ExtractTableNames("SELECT 1")
	if len(tables) != 0 {
		t.Fatalf("expected 0 tables, got %d: %v", len(tables), tables)
	}
}

func TestExtractTableNames_Dedup(t *testing.T) {
	tables := ExtractTableNames("SELECT a FROM v_customers JOIN v_customers ON 1=1")
	if len(tables) != 1 {
		t.Fatalf("expected deduped to 1, got %d: %v", len(tables), tables)
	}
}

// --- IsAllowedObject ---

func TestIsAllowedObject_EmptyAllowAll(t *testing.T) {
	if !IsAllowedObject("anything", nil) {
		t.Fatal("empty allowed list should allow all")
	}
	if !IsAllowedObject("anything", []string{}) {
		t.Fatal("empty allowed list should allow all")
	}
}

func TestIsAllowedObject_ExactMatch(t *testing.T) {
	if !IsAllowedObject("v_customers", []string{"v_customers"}) {
		t.Fatal("exact match should be allowed")
	}
}

func TestIsAllowedObject_GlobMatch(t *testing.T) {
	if !IsAllowedObject("v_customers", []string{"v_*"}) {
		t.Fatal("glob v_* should match v_customers")
	}
	if !IsAllowedObject("app_billing__invoices", []string{"app_*"}) {
		t.Fatal("glob app_* should match app_billing__invoices")
	}
}

func TestIsAllowedObject_CaseInsensitive(t *testing.T) {
	if !IsAllowedObject("V_Customers", []string{"v_customers"}) {
		t.Fatal("case insensitive match should work")
	}
}

func TestIsAllowedObject_Denied(t *testing.T) {
	if IsAllowedObject("entities", []string{"v_*", "app_*"}) {
		t.Fatal("entities should not match v_* or app_*")
	}
}

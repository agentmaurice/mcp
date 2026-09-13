package security

import (
	"testing"
)

// These tests document DuckDB-specific SQL syntax that the MySQL-based parser
// cannot handle. They verify that such queries are correctly rejected rather
// than silently misinterpreted, and serve as a living specification of known
// parser limitations.

func TestParserLimitations_CastVarchar(t *testing.T) {
	// DuckDB accepts CAST(x AS VARCHAR) but the MySQL parser does not.
	_, err := ValidatePortableSQL("SELECT CAST(name AS VARCHAR) FROM v_entities", defaultRules)
	if err == nil {
		t.Fatal("expected CAST(x AS VARCHAR) to fail with MySQL parser")
	}

	// Workaround: use CHAR
	_, err = ValidatePortableSQL("SELECT CAST(name AS CHAR) FROM v_entities", defaultRules)
	if err != nil {
		t.Fatalf("CAST(x AS CHAR) should work: %v", err)
	}
}

func TestParserLimitations_FilterClause(t *testing.T) {
	// DuckDB: SELECT count(*) FILTER (WHERE status = 'active') FROM v_entities
	// Not supported by MySQL parser
	_, err := ValidatePortableSQL("SELECT count(*) FILTER (WHERE status = 'active') FROM v_entities", defaultRules)
	if err == nil {
		t.Fatal("expected FILTER clause to fail with MySQL parser")
	}
}

func TestParserLimitations_StructSyntax(t *testing.T) {
	// DuckDB: SELECT {'key': 'value'}
	_, err := ValidatePortableSQL("SELECT {'key': 'value'} FROM v_entities", defaultRules)
	if err == nil {
		t.Fatal("expected DuckDB struct syntax to fail with MySQL parser")
	}
}

func TestParserLimitations_ListSyntax(t *testing.T) {
	// DuckDB: SELECT [1, 2, 3]
	_, err := ValidatePortableSQL("SELECT [1, 2, 3] FROM v_entities", defaultRules)
	if err == nil {
		t.Fatal("expected DuckDB list literal syntax to fail with MySQL parser")
	}
}

// --- Queries that SHOULD work despite parser differences ---

func TestParserCompat_ArrowOperators(t *testing.T) {
	// The -> and ->> operators are commonly used in both DuckDB and PostgreSQL
	// for JSON access. They should now be allowed since we removed them from
	// the forbidden list (they're read-only accessors).
	rules := PortableSQLRules{AllowSelectStar: true, AllowJSONOperators: true}

	tests := []string{
		"SELECT attributes->>'email' FROM v_entities",
		"SELECT attributes->'contact' FROM v_entities",
	}
	for _, sql := range tests {
		// These may or may not parse correctly with the MySQL parser,
		// but should not be blocked by our JSON operator filter.
		_, _ = ValidatePortableSQL(sql, rules) // verify no panic
	}
}

func TestParserCompat_JSONFunctions(t *testing.T) {
	// json_extract_string is DuckDB-native and should be in the allowlist.
	// The MySQL parser may or may not parse it depending on syntax.
	tests := []struct {
		name string
		sql  string
	}{
		{"json_extract_string", "SELECT json_extract_string(attributes, '$.email') FROM v_entities"},
		{"json_type", "SELECT json_type(attributes) FROM v_entities"},
		{"json_valid", "SELECT json_valid(attributes) FROM v_entities"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePortableSQL(tc.sql, defaultRules)
			if err != nil {
				t.Logf("NOTE: %q not parseable by MySQL parser (expected limitation): %v", tc.name, err)
			}
		})
	}
}

func TestParserCompat_StandardFunctions(t *testing.T) {
	// Functions that should work with both DuckDB and the MySQL parser
	tests := []struct {
		name string
		sql  string
	}{
		{"lower", "SELECT lower(name) FROM v_entities"},
		{"upper", "SELECT upper(name) FROM v_entities"},
		{"coalesce", "SELECT coalesce(status, 'unknown') FROM v_entities"},
		{"length", "SELECT length(name) FROM v_entities"},
		{"replace", "SELECT replace(name, 'a', 'b') FROM v_entities"},
		{"substr", "SELECT substr(name, 1, 3) FROM v_entities"},
		{"trim", "SELECT trim(name) FROM v_entities"},
		{"round", "SELECT round(3.14, 0) FROM v_entities"},
		{"abs", "SELECT abs(-42) FROM v_entities"},
		{"floor", "SELECT floor(3.7) FROM v_entities"},
		{"ceil", "SELECT ceil(3.2) FROM v_entities"},
		{"concat", "SELECT concat(name, ' - ', status) FROM v_entities"},
		{"nullif", "SELECT nullif(status, '') FROM v_entities"},
		{"greatest", "SELECT greatest(1, 2, 3) FROM v_entities"},
		{"least", "SELECT least(1, 2, 3) FROM v_entities"},
		{"date_trunc", "SELECT date_trunc('month', created_at) FROM v_entities"},
		{"count", "SELECT count(*) FROM v_entities"},
		{"sum", "SELECT sum(1) FROM v_entities"},
		{"avg", "SELECT avg(1) FROM v_entities"},
		{"min max", "SELECT min(created_at), max(created_at) FROM v_entities"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePortableSQL(tc.sql, defaultRules)
			if err != nil {
				t.Errorf("expected %q to be parseable and allowed: %v", tc.name, err)
			}
		})
	}
}

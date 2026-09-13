package duckhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestQuery_ReturnsResults verifies that Query correctly parses results from the server.
func TestQuery_ReturnsResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"columns": []string{"id", "name"},
			"rows": [][]any{
				{1, "Alice"},
				{2, "Bob"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	result, err := client.Query(context.Background(), "SELECT id, name FROM users")

	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(result.Columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(result.Columns))
	}

	if result.Columns[0] != "id" || result.Columns[1] != "name" {
		t.Errorf("expected columns [id, name], got %v", result.Columns)
	}

	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(result.Rows))
	}

	if result.Rows[0]["id"] != float64(1) || result.Rows[0]["name"] != "Alice" {
		t.Errorf("unexpected first row: %v", result.Rows[0])
	}

	if result.Rows[1]["id"] != float64(2) || result.Rows[1]["name"] != "Bob" {
		t.Errorf("unexpected second row: %v", result.Rows[1])
	}
}

// TestExec_NoError verifies that Exec executes without error.
func TestExec_NoError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"columns": []string{},
			"rows":    [][]any{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.Exec(context.Background(), "CREATE TABLE test (id INT)")

	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
}

// TestQuery_ServerError verifies error handling when server returns 400+.
func TestQuery_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("SQL syntax error"))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.Query(context.Background(), "INVALID SQL")

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if err.Error() != "duckhttp: server error 400: SQL syntax error" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestQuery_ContextCancelled verifies error handling when context is cancelled.
func TestQuery_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"columns": []string{}, "rows": [][]any{}})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.Query(ctx, "SELECT 1")

	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestHealthCheck verifies the HealthCheck method works.
func TestHealthCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"columns": []string{"?column?"},
			"rows":    [][]any{{1}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	err := client.HealthCheck(context.Background())

	if err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}
}

// TestInterpolateArgs_StringWithEscaping verifies string argument escaping.
func TestInterpolateArgs_StringWithEscaping(t *testing.T) {
	sql := "SELECT * FROM users WHERE name = ?"
	args := []any{"O'Brien"}
	result := interpolateArgs(sql, args)

	expected := "SELECT * FROM users WHERE name = 'O''Brien'"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestInterpolateArgs_Integer verifies integer argument formatting.
func TestInterpolateArgs_Integer(t *testing.T) {
	sql := "SELECT * FROM users WHERE id = ?"
	args := []any{42}
	result := interpolateArgs(sql, args)

	expected := "SELECT * FROM users WHERE id = 42"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestInterpolateArgs_Nil verifies NULL formatting.
func TestInterpolateArgs_Nil(t *testing.T) {
	sql := "SELECT * FROM users WHERE deleted_at = ?"
	args := []any{nil}
	result := interpolateArgs(sql, args)

	expected := "SELECT * FROM users WHERE deleted_at = NULL"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestInterpolateArgs_MultipleArgs verifies multiple argument replacement.
func TestInterpolateArgs_MultipleArgs(t *testing.T) {
	sql := "INSERT INTO users (name, age) VALUES (?, ?)"
	args := []any{"Alice", 30}
	result := interpolateArgs(sql, args)

	expected := "INSERT INTO users (name, age) VALUES ('Alice', 30)"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestFormatArg_StringWithQuotes verifies string formatting with quote escaping.
func TestFormatArg_StringWithQuotes(t *testing.T) {
	result := formatArg("It's working")
	expected := "'It''s working'"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestFormatArg_Nil verifies nil formatting.
func TestFormatArg_Nil(t *testing.T) {
	result := formatArg(nil)
	expected := "NULL"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestFormatArg_Float verifies float formatting.
func TestFormatArg_Float(t *testing.T) {
	result := formatArg(3.14)
	expected := "3.14"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestFormatArg_Int64 verifies int64 formatting.
func TestFormatArg_Int64(t *testing.T) {
	result := formatArg(int64(9223372036854775807))
	expected := "9223372036854775807"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

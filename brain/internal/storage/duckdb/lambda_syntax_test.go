//go:build duckdb

package duckdb

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
)

// The manager rejects the single-arrow lambda, as DuckDB 2.0 does by default, and
// accepts the "lambda x: ..." form that works on 1.4 and 2.x.
func TestConnectionRejectsSingleArrowLambda(t *testing.T) {
	db, err := openDuckDB("", connectionSettings(config.StorageConfig{}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	var got string
	if err := db.QueryRowContext(ctx, "SELECT CAST(list_transform([1, 2, 3], lambda x: x + 1) AS VARCHAR)").Scan(&got); err != nil {
		t.Fatalf("lambda x: form must work: %v", err)
	}
	if got != "[2, 3, 4]" {
		t.Fatalf("lambda result = %q, want [2, 3, 4]", got)
	}
	err = db.QueryRowContext(ctx, "SELECT CAST(list_transform([1, 2, 3], x -> x + 1) AS VARCHAR)").Scan(&got)
	if err == nil {
		t.Fatal("single-arrow lambda must be rejected")
	}
	if !strings.Contains(err.Error(), "lambda") {
		t.Fatalf("unexpected error for single-arrow lambda: %v", err)
	}
}

// arrowLambda matches a lambda parameter list followed by the single arrow, whatever
// the body is and wherever the line breaks fall: "list_transform(l, x -> 'ok')",
// "(x, i) -> ...", "x\n    -> x + 1". "->>" is not matched. The pattern also flags the
// JSON operator after a comma or parenthesis (", doc -> 'k'"): brain uses none, and
// a false alarm there is safe, a missed lambda is not.
var arrowLambda = regexp.MustCompile(`(?im)(?:[,(]|^)\s*\(?\s*[a-z_]\w*(?:\s*,\s*[a-z_]\w*)*\s*\)?\s*->(?:[^>]|$)`)

// commentOrString matches, left to right, a SQL string literal (kept as is, so that
// "'--'" is not read as a comment) or a comment: /* ... */, "--" to the end of the
// line, "//" to the end of the line (but not "://"). Comments are blanked and keep
// their line breaks so numbering holds.
//
// The scan is a safety net for SQL that no test executes; the guard that matters is
// the connection setting, which makes DuckDB itself reject the arrow in every query a
// test runs. It is not a Go or SQL lexer: an apostrophe inside a Go string can hide a
// following lambda.
var commentOrString = regexp.MustCompile(`(?s)'(?:[^']|'')*'|/\*.*?\*/|--[^\n]*|(?:^|[^:])//[^\n]*`)

// arrowLambdaLines returns the 1-based numbers of the lines holding the arrow of a
// single-arrow lambda.
func arrowLambdaLines(content string) []int {
	stripped := commentOrString.ReplaceAllStringFunc(content, func(m string) string {
		if strings.HasPrefix(m, "'") {
			return m
		}
		return " " + strings.Repeat("\n", strings.Count(m, "\n"))
	})
	var found []int
	for _, m := range arrowLambda.FindAllStringIndex(stripped, -1) {
		arrow := strings.LastIndex(stripped[:m[1]], "->")
		found = append(found, 1+strings.Count(stripped[:arrow], "\n"))
	}
	return found
}

func TestArrowLambdaDetection(t *testing.T) {
	flagged := []string{
		"SELECT list_transform([1, 2], x -> 'ok');",
		"SELECT list_transform([1, 2], x -> x + 1);",
		"SELECT list_transform([1, 2], X -> x);",
		"SELECT list_reduce(l, (acc, x) -> acc + x);",
		"SELECT list_filter(l,\n\t\tx ->\n\t\t\tx > 1);",
		"SELECT list_transform(l,\n\t\tx -> rtrim(x));",
		"SELECT list_transform(l, x\t->x);",
		"SELECT list_transform([1, 2], x\n    -> x + 1);",
		"SELECT list_transform([1, 2], /* paramètre */ x -> x + 1);",
		"SELECT list_transform([1, 2], /* a\n b */ x -> x + 1);",
		"SELECT list_transform([1, 2], x -- comment\n -> x + 1);",
		"SELECT list_transform([1, 2], x // comment\n -> x + 1);",
		"SELECT list_transform([1, 2], x /* c */ -- d\n /* e */ -> x + 1);",
		"SELECT list_transform(['--'], x -> upper(x));",
		"SELECT list_transform(['//', 'a''--'], x -> upper(x));",
		"SELECT list_transform([1, 2],\n    x\n    ->\n    x + 1);",
		"SELECT list_reduce(l, (acc,\n x)\n -> acc + x);",
	}
	for _, src := range flagged {
		if len(arrowLambdaLines(src)) == 0 {
			t.Errorf("single-arrow lambda not detected in %q", src)
		}
	}
	clean := []string{
		"SELECT list_transform([1, 2], lambda x: x + 1);",
		"SELECT list_filter(l, lambda x: x > 1);",
		"SELECT doc ->> 'k' FROM t;",
		"// list_transform(l, x -> x + 1) is the old form",
		"-- list_transform(l, x -> x + 1)",
		"return a-b > 1",
	}
	for _, src := range clean {
		if got := arrowLambdaLines(src); len(got) != 0 {
			t.Errorf("false alarm on %q: lines %v", src, got)
		}
	}
}

// TestNoSingleArrowLambdaInSource covers SQL that no test executes: no Go or SQL
// file of the module may contain a single-arrow lambda.
func TestNoSingleArrowLambdaInSource(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".sql") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range arrowLambdaLines(string(data)) {
			t.Errorf("%s:%d: single-arrow lambda, use \"lambda x: ...\"", path, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
}

package security

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var (
	limitRegex = regexp.MustCompile(`(?i)\blimit\s+(\d+)`)
	fromJoinRe = regexp.MustCompile(`(?i)\b(from|join)\s+([a-zA-Z0-9_\.]+)`)
)

// ValidateReadOnly checks the SQL for disallowed statements and multi-statements.
func ValidateReadOnly(sql string, denylist []string) error {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return errors.New("sql is required")
	}
	// Remove trailing semicolon(s) before checking for multi-statements
	trimmed = strings.TrimRight(trimmed, "; \t\n\r")
	if strings.Contains(trimmed, ";") {
		return errors.New("multi-statements are not allowed")
	}
	lower := strings.ToLower(trimmed)
	for _, word := range denylist {
		if word == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(word)) {
			return fmt.Errorf("statement contains forbidden keyword: %s", word)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(lower), "select") {
		return errors.New("only SELECT statements are allowed")
	}
	return nil
}

// EnsureLimit enforces a maximum LIMIT and returns the updated SQL and whether it was modified.
func EnsureLimit(sql string, maxRows int) (string, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(sql, ";"))
	matches := limitRegex.FindAllStringSubmatch(trimmed, -1)
	if len(matches) == 0 {
		return fmt.Sprintf("%s LIMIT %d", trimmed, maxRows), true
	}
	last := matches[len(matches)-1]
	if len(last) < 2 {
		return trimmed, false
	}
	val, err := strconv.Atoi(last[1])
	if err != nil {
		return fmt.Sprintf("%s LIMIT %d", trimmed, maxRows), true
	}
	if val <= maxRows {
		return trimmed, false
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS limited_sub LIMIT %d", trimmed, maxRows), true
}

// ExtractTableNames returns simple table/view identifiers from FROM/JOIN clauses.
func ExtractTableNames(sql string) []string {
	matches := fromJoinRe.FindAllStringSubmatch(sql, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		name := strings.Trim(match[2], "\"")
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// IsAllowedObject returns true if object name matches allowed patterns. Empty list means allow all.
func IsAllowedObject(name string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	lower := strings.ToLower(name)
	for _, pattern := range allowed {
		p := strings.ToLower(strings.TrimSpace(pattern))
		if p == "" {
			continue
		}
		if ok, _ := path.Match(p, lower); ok {
			return true
		}
		if p == lower {
			return true
		}
	}
	return false
}

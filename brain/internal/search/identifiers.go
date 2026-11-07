package search

import (
	"strings"
	"unicode"
)

// ExtractIdentifiers returns the tokens of query that look like identifiers:
// a digit with a letter or a separator (AGENT-530, MEM-G3, 2026.10.19-3015), or
// an underscore with a letter (calcul_taux_marge). Plain words, camelCase names
// (AgentMaurice, ExecContext) and bare numbers are not identifiers: the full-text
// index already matches them as whole tokens, and a common product name in camelCase
// would otherwise fill the verbatim tier. Each identifier is returned once, in query order.
func ExtractIdentifiers(query string) []string {
	tokens := strings.FieldsFunc(query, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.')
	})
	var out []string
	seen := make(map[string]struct{})
	for _, raw := range tokens {
		token := strings.Trim(raw, "-.")
		if len([]rune(token)) < 2 || !IsIdentifier(token) {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

// IsIdentifier reports whether token is an identifier as defined by ExtractIdentifiers.
func IsIdentifier(token string) bool {
	var hasLetter, hasDigit, hasSeparator bool
	for _, r := range token {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		case r == '-' || r == '.' || r == '_':
			hasSeparator = true
		}
	}
	if hasDigit && (hasLetter || hasSeparator) {
		return true
	}
	return strings.ContainsRune(token, '_') && hasLetter
}

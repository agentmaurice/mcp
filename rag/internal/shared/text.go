package shared

import (
	"strings"
	"unicode/utf8"
)

// SanitizeUTF8 ensures the string contains only valid UTF-8 characters.
// Invalid byte sequences are replaced with the Unicode replacement character (U+FFFD).
func SanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}

	// Build a new string with only valid UTF-8 runes
	var builder strings.Builder
	builder.Grow(len(s))

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid byte - replace with replacement character
			builder.WriteRune('\uFFFD')
			i++
		} else {
			builder.WriteRune(r)
			i += size
		}
	}

	return builder.String()
}

// SanitizeUTF8Clean ensures the string contains only valid UTF-8 characters.
// Invalid byte sequences are removed entirely (no replacement character).
func SanitizeUTF8Clean(s string) string {
	if utf8.ValidString(s) {
		return s
	}

	// Build a new string with only valid UTF-8 runes
	var builder strings.Builder
	builder.Grow(len(s))

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid byte - skip it
			i++
		} else {
			builder.WriteRune(r)
			i += size
		}
	}

	return builder.String()
}

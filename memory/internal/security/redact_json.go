package security

import (
	"strings"
)

// RedactMapKeys traverses a map recursively and redacts values whose keys
// match the provided key set (case-insensitive). This is useful for applying
// PII redaction to arbitrary JSON structures (e.g., resource handler results)
// that don't go through the columnar RedactResult pipeline.
func RedactMapKeys(obj map[string]any, keys []string, redactValue any) map[string]any {
	if len(obj) == 0 || len(keys) == 0 {
		return obj
	}
	keySet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k != "" {
			keySet[strings.ToLower(k)] = struct{}{}
		}
	}
	redactMapKeysRecursive(obj, keySet, redactValue)
	return obj
}

func redactMapKeysRecursive(obj map[string]any, keySet map[string]struct{}, redactValue any) {
	for key, val := range obj {
		if _, match := keySet[strings.ToLower(key)]; match {
			obj[key] = redactValue
			continue
		}
		switch nested := val.(type) {
		case map[string]any:
			redactMapKeysRecursive(nested, keySet, redactValue)
		case []any:
			for _, item := range nested {
				if m, ok := item.(map[string]any); ok {
					redactMapKeysRecursive(m, keySet, redactValue)
				}
			}
		}
	}
}

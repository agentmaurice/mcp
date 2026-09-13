package security

import (
	"encoding/json"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
)

// RedactResult applies column and JSON-key redaction in-place.
func RedactResult(columns []shared.Column, rows [][]any, keys []string, redactValue any) {
	if len(rows) == 0 {
		return
	}
	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		keySet[strings.ToLower(key)] = struct{}{}
	}

	for rowIdx := range rows {
		row := rows[rowIdx]
		for colIdx, col := range columns {
			if colIdx >= len(row) {
				continue
			}
			if shouldRedactColumn(col.Name, keySet) {
				row[colIdx] = redactValue
				continue
			}
			row[colIdx] = redactValueInValue(row[colIdx], keySet, redactValue)
		}
		rows[rowIdx] = row
	}
}

func shouldRedactColumn(name string, keySet map[string]struct{}) bool {
	if name == "" {
		return false
	}
	_, ok := keySet[strings.ToLower(name)]
	return ok
}

func redactValueInValue(value any, keySet map[string]struct{}, redactValue any) any {
	switch v := value.(type) {
	case map[string]any:
		return redactMap(v, keySet, redactValue)
	case []any:
		for i := range v {
			v[i] = redactValueInValue(v[i], keySet, redactValue)
		}
		return v
	case string:
		var obj map[string]any
		if err := json.Unmarshal([]byte(v), &obj); err == nil {
			obj = redactMap(obj, keySet, redactValue)
			if data, err := json.Marshal(obj); err == nil {
				return string(data)
			}
		}
	case []byte:
		var obj map[string]any
		if err := json.Unmarshal(v, &obj); err == nil {
			obj = redactMap(obj, keySet, redactValue)
			if data, err := json.Marshal(obj); err == nil {
				return string(data)
			}
		}
	}
	return value
}

func redactMap(input map[string]any, keySet map[string]struct{}, redactValue any) map[string]any {
	for key, val := range input {
		if _, ok := keySet[strings.ToLower(key)]; ok {
			input[key] = redactValue
			continue
		}
		switch nested := val.(type) {
		case map[string]any:
			input[key] = redactMap(nested, keySet, redactValue)
		case []any:
			for i := range nested {
				nested[i] = redactValueInValue(nested[i], keySet, redactValue)
			}
			input[key] = nested
		}
	}
	return input
}

package security

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
)

const (
	ActionRedact = "redact"
	ActionHash   = "hash"
	ActionDrop   = "drop"
)

// PolicyRule represents a pii_policy rule.
type PolicyRule struct {
	Column string
	Key    string
	Action string
}

// ParsePolicyRule converts a column_path + action into a PolicyRule.
func ParsePolicyRule(path string, action string) (PolicyRule, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return PolicyRule{}, false
	}
	segments := strings.Split(path, ".")
	if len(segments) == 0 {
		return PolicyRule{}, false
	}

	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = ActionRedact
	}
	if action != ActionRedact && action != ActionHash && action != ActionDrop {
		return PolicyRule{}, false
	}

	if len(segments) == 1 {
		return PolicyRule{Column: segments[0], Action: action}, true
	}
	if len(segments) == 2 {
		return PolicyRule{Column: segments[1], Action: action}, true
	}

	column := segments[1]
	key := strings.Join(segments[2:], ".")
	if column == "" || key == "" {
		return PolicyRule{}, false
	}
	return PolicyRule{Column: column, Key: key, Action: action}, true
}

// ApplyPolicyRules applies pii_policy rules to query results.
func ApplyPolicyRules(columns []shared.Column, rows [][]any, rules []PolicyRule, redactValue any) {
	if len(rows) == 0 || len(columns) == 0 || len(rules) == 0 {
		return
	}

	rulesByColumn := make(map[string][]PolicyRule)
	var wildcard []PolicyRule
	for _, rule := range rules {
		col := strings.ToLower(strings.TrimSpace(rule.Column))
		if col == "" {
			continue
		}
		if col == "*" {
			wildcard = append(wildcard, rule)
			continue
		}
		rulesByColumn[col] = append(rulesByColumn[col], rule)
	}

	for rowIdx := range rows {
		row := rows[rowIdx]
		for colIdx, col := range columns {
			if colIdx >= len(row) {
				continue
			}
			colName := strings.ToLower(col.Name)
			rulesFor := append([]PolicyRule{}, rulesByColumn[colName]...)
			rulesFor = append(rulesFor, wildcard...)
			if len(rulesFor) == 0 {
				continue
			}
			value := row[colIdx]
			for _, rule := range rulesFor {
				if rule.Key == "" {
					value = applyAction(value, rule.Action, redactValue)
					continue
				}
				value = applyActionToJSONPath(value, rule.Key, rule.Action, redactValue)
			}
			row[colIdx] = value
		}
		rows[rowIdx] = row
	}
}

func applyAction(value any, action string, redactValue any) any {
	switch action {
	case ActionDrop:
		return nil
	case ActionHash:
		return hashValue(value)
	case ActionRedact:
		return redactValue
	default:
		return value
	}
}

func applyActionToJSONPath(value any, key string, action string, redactValue any) any {
	switch v := value.(type) {
	case map[string]any:
		applyActionToMapPath(v, strings.Split(key, "."), action, redactValue)
		return v
	case []byte:
		var obj map[string]any
		if err := json.Unmarshal(v, &obj); err == nil {
			applyActionToMapPath(obj, strings.Split(key, "."), action, redactValue)
			if data, err := json.Marshal(obj); err == nil {
				return string(data)
			}
		}
	case string:
		var obj map[string]any
		if err := json.Unmarshal([]byte(v), &obj); err == nil {
			applyActionToMapPath(obj, strings.Split(key, "."), action, redactValue)
			if data, err := json.Marshal(obj); err == nil {
				return string(data)
			}
		}
	}
	return value
}

func applyActionToMapPath(obj map[string]any, path []string, action string, redactValue any) {
	if len(path) == 0 {
		return
	}
	key := path[0]
	if len(path) == 1 {
		for k, val := range obj {
			if !strings.EqualFold(k, key) {
				continue
			}
			switch action {
			case ActionDrop:
				delete(obj, k)
			case ActionHash:
				obj[k] = hashValue(val)
			case ActionRedact:
				obj[k] = redactValue
			}
		}
		return
	}
	for k, val := range obj {
		if !strings.EqualFold(k, key) {
			continue
		}
		child, ok := val.(map[string]any)
		if !ok {
			return
		}
		applyActionToMapPath(child, path[1:], action, redactValue)
		obj[k] = child
	}
}

func hashValue(value any) string {
	if value == nil {
		return ""
	}
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		raw = strings.TrimSpace(fmt.Sprint(v))
	}
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

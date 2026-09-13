package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/rs/xid"
)

type privacyInput struct {
	Mode       string   `json:"mode"`
	FieldPaths []string `json:"field_paths"`
	Tags       []string `json:"tags"`
	StoreRaw   bool     `json:"store_raw"`
}

func normalizePayloadObject(payload any) (map[string]any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("payload must be an object")
	}
	return out, nil
}

func sanitizePrivatePayload(kind string, payload map[string]any, privacy privacyInput) (map[string]any, []string, string, error) {
	mode := strings.TrimSpace(strings.ToLower(privacy.Mode))
	switch mode {
	case "redact", "hash", "drop_fields", "ephemeral":
	default:
		return nil, nil, "", fmt.Errorf("privacy.mode must be one of redact, hash, drop_fields, ephemeral")
	}

	originalHash, err := hashAny(payload)
	if err != nil {
		return nil, nil, "", err
	}

	sanitized, err := deepCopyMap(payload)
	if err != nil {
		return nil, nil, "", err
	}

	redactedPaths := make([]string, 0, len(privacy.FieldPaths))
	for _, rawPath := range privacy.FieldPaths {
		path, err := normalizeFieldPath(rawPath)
		if err != nil {
			return nil, nil, "", err
		}
		applied, err := transformPath(sanitized, path, mode)
		if err != nil {
			return nil, nil, "", err
		}
		if applied {
			redactedPaths = append(redactedPaths, strings.Join(path, "."))
		}
	}

	if mode == "ephemeral" {
		applyEphemeralDefaults(kind, sanitized)
	}

	return sanitized, redactedPaths, originalHash, nil
}

func normalizeFieldPath(raw string) ([]string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, fmt.Errorf("field_paths contains an empty path")
	}
	parts := strings.Split(value, ".")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("invalid field path: %s", raw)
		}
	}
	return parts, nil
}

func transformPath(root map[string]any, path []string, mode string) (bool, error) {
	current := root
	for idx, part := range path[:len(path)-1] {
		next, ok := current[part]
		if !ok {
			return false, nil
		}
		child, ok := next.(map[string]any)
		if !ok {
			return false, nil
		}
		current = child
		if idx == len(path)-2 {
			break
		}
	}

	leaf := path[len(path)-1]
	value, ok := current[leaf]
	if !ok {
		return false, nil
	}

	switch mode {
	case "redact", "ephemeral":
		current[leaf] = "[REDACTED]"
	case "hash":
		hashed, err := hashAny(value)
		if err != nil {
			return false, err
		}
		current[leaf] = hashed
	case "drop_fields":
		delete(current, leaf)
	default:
		return false, fmt.Errorf("unsupported privacy mode: %s", mode)
	}
	return true, nil
}

func applyEphemeralDefaults(kind string, payload map[string]any) {
	switch kind {
	case "fact":
		payload["payload"] = map[string]any{"_ephemeral": true}
	case "entity":
		items, ok := payload["items"].([]any)
		if !ok {
			return
		}
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			item["attributes"] = map[string]any{"_ephemeral": true}
			if _, exists := item["name"]; exists {
				item["name"] = ""
			}
		}
	case "document":
		payload["metadata"] = map[string]any{"_ephemeral": true}
	}
}

func deepCopyMap(input map[string]any) (map[string]any, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to copy payload: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("failed to decode copied payload: %w", err)
	}
	return out, nil
}

func hashAny(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("failed to hash value: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func insertIngestReceipt(
	ctx context.Context,
	manager storage.Manager,
	q storage.Querier,
	tenantID string,
	kind string,
	privacy privacyInput,
	redactedPaths []string,
	targetType string,
	targetIDs []string,
	payloadHash string,
) (string, error) {
	receiptID := xid.New().String()

	tagsJSON, _ := json.Marshal(privacy.Tags)
	if tagsJSON == nil {
		tagsJSON = []byte("[]")
	}
	pathsJSON, _ := json.Marshal(redactedPaths)
	if pathsJSON == nil {
		pathsJSON = []byte("[]")
	}
	targetJSON, _ := json.Marshal(targetIDs)
	if targetJSON == nil {
		targetJSON = []byte("[]")
	}

	_, err := q.ExecContext(ctx,
		rebindQuery(manager, "INSERT INTO ingest_receipts (receipt_id, tenant_id, kind, privacy_mode, privacy_tags, redacted_paths, target_type, target_ids, payload_hash, raw_persisted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"),
		receiptID,
		tenantID,
		kind,
		privacy.Mode,
		string(tagsJSON),
		string(pathsJSON),
		targetType,
		string(targetJSON),
		payloadHash,
		false,
	)
	if err != nil {
		return "", err
	}

	return receiptID, nil
}

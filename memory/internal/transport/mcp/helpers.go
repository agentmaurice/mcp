package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
)

var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// SourceInput matches the source payload schema.
type SourceInput struct {
	System    string         `json:"system"`
	Author    string         `json:"author"`
	Timestamp string         `json:"timestamp"`
	TraceID   string         `json:"traceId"`
	Raw       map[string]any `json:"raw"`
}

// WriterInput matches the writer payload schema.
type WriterInput struct {
	AppID     string `json:"appId"`
	ActorID   string `json:"actorId"`
	ActorType string `json:"actorType"`
}

func errorResult(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: message,
			},
		},
	}
}

func structuredResult(payload any) *mcp.CallToolResult {
	data, _ := json.Marshal(payload)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: string(data),
			},
		},
		StructuredContent: payload,
	}
}

func truncateForSummary(input string, limit int) string {
	value := strings.TrimSpace(input)
	if value == "" {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func resolveTenantID(ctx context.Context, cfg *config.Config, tenantArg string, rawArgs any) (string, error) {
	tenantID := strings.TrimSpace(tenantArg)
	if tenantID == "" {
		tenantID = strings.TrimSpace(extractTenantArg(rawArgs))
	}
	if tenantID == "" {
		identity := shared.IdentityFromContext(ctx)
		tenantID = strings.TrimSpace(identity.TenantID)
	}
	if tenantID == "" && cfg != nil {
		tenantID = strings.TrimSpace(cfg.Storage.DefaultTenantID)
	}
	if tenantID == "" {
		return "", fmt.Errorf("tenant_id is required")
	}
	if !tenantIDPattern.MatchString(tenantID) {
		return "", fmt.Errorf("invalid tenant_id: %s", tenantID)
	}
	return tenantID, nil
}

func extractTenantArg(input any) string {
	if input == nil {
		return ""
	}
	args, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	if val, ok := args["tenant_id"].(string); ok {
		return val
	}
	if val, ok := args["tenantId"].(string); ok {
		return val
	}
	return ""
}

func insertSource(ctx context.Context, manager storage.Manager, q storage.Querier, source SourceInput, timestamp time.Time) (string, error) {
	sourceID := xid.New().String()
	rawJSON, _ := json.Marshal(source.Raw)
	if rawJSON == nil {
		rawJSON = []byte("{}")
	}
	if _, err := q.ExecContext(ctx,
		rebindQuery(manager, "INSERT INTO sources (source_id, system, author, timestamp, trace_id, raw) VALUES (?, ?, ?, ?, ?, ?)"),
		sourceID, source.System, source.Author, timestamp, source.TraceID, string(rawJSON)); err != nil {
		return "", err
	}
	return sourceID, nil
}

func ensureWriter(ctx context.Context, manager storage.Manager, q storage.Querier, writer WriterInput) (string, error) {
	actorType := writer.ActorType
	if actorType == "" {
		actorType = "agent"
	}
	qr, err := q.QueryContext(ctx,
		rebindQuery(manager, "SELECT writer_id FROM writers WHERE app_id = ? AND actor_id = ? AND actor_type = ? LIMIT 1"),
		writer.AppID, writer.ActorID, actorType)
	if err != nil {
		return "", err
	}
	if len(qr.Rows) > 0 {
		if id, ok := qr.Rows[0]["writer_id"].(string); ok && id != "" {
			return id, nil
		}
	}
	writerID := xid.New().String()
	if _, err := q.ExecContext(ctx,
		rebindQuery(manager, "INSERT INTO writers (writer_id, app_id, actor_id, actor_type) VALUES (?, ?, ?, ?)"),
		writerID, writer.AppID, writer.ActorID, actorType); err != nil {
		return "", err
	}
	return writerID, nil
}

// queryScalarString executes a query and returns the first column of the first row as a string.
func queryScalarString(ctx context.Context, q storage.Querier, query string, args ...any) (string, error) {
	qr, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	if len(qr.Rows) == 0 {
		return "", nil
	}
	for _, v := range qr.Rows[0] {
		if s, ok := v.(string); ok {
			return s, nil
		}
		if s, ok := v.(fmt.Stringer); ok {
			return s.String(), nil
		}
		return fmt.Sprintf("%v", v), nil
	}
	return "", nil
}

// countQuery executes a COUNT query and returns the integer result.
func countQuery(ctx context.Context, q storage.Querier, query string, args ...any) int {
	qr, err := q.QueryContext(ctx, query, args...)
	if err != nil || len(qr.Rows) == 0 {
		return 0
	}
	for _, v := range qr.Rows[0] {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return 0
}

func rebindQuery(manager storage.Manager, query string) string {
	if manager == nil {
		return query
	}
	if strings.ToLower(manager.Dialect()) != "postgres" {
		return query
	}
	return rebindDollar(query)
}

func rebindDollar(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			b.WriteString(fmt.Sprintf("$%d", arg))
			arg++
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

func parseJSONValue(raw any) any {
	switch value := raw.(type) {
	case nil:
		return nil
	case map[string]any, []any:
		return value
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil
		}
		var out any
		if err := json.Unmarshal([]byte(text), &out); err == nil {
			return out
		}
		return text
	case []byte:
		if len(value) == 0 {
			return nil
		}
		var out any
		if err := json.Unmarshal(value, &out); err == nil {
			return out
		}
		return string(value)
	default:
		return value
	}
}

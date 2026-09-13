package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	stypes "github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/types"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

func TestBuildCompactSearchResponse(t *testing.T) {
	longContent := strings.Repeat("detail ", 80)
	payload := buildCompactSearchResponse(&shared.SearchResponse{
		Mode:         "hybrid",
		Query:        "find auth service",
		TotalResults: 1,
		Results: []shared.SearchResult{
			{
				Score: 0.91,
				Document: shared.Document{
					ID:       "doc-1",
					FilePath: "internal/auth/service.go",
					Title:    "Auth Service",
					DocType:  "code",
					Language: "go",
				},
				Chunk: shared.Chunk{
					ID:         "chunk-1",
					ChunkType:  "function",
					SymbolName: "Authenticate",
					StartLine:  10,
					EndLine:    42,
					Content:    longContent,
				},
			},
		},
	})

	if len(payload.Results) != 1 {
		t.Fatalf("expected one result, got %d", len(payload.Results))
	}
	result := payload.Results[0]
	if result.ID != "chunk:chunk-1" {
		t.Fatalf("unexpected compact id: %s", result.ID)
	}
	if result.Document.FilePath != "internal/auth/service.go" {
		t.Fatalf("unexpected file path: %s", result.Document.FilePath)
	}
	if result.Summary == "" || !strings.HasSuffix(result.Summary, "...") {
		t.Fatalf("expected truncated summary, got %q", result.Summary)
	}
	if result.NextHint == "" {
		t.Fatalf("expected next hint")
	}
}

func TestStatsToolReportsDisabledEmbeddingProviderWithoutModel(t *testing.T) {
	manager := &fakeBrainManager{q: &fakeBrainQuerier{chunksByID: map[string]chunkRecord{}}}
	cfg := &config.Config{
		Storage: config.StorageConfig{DefaultTenantID: "tenant"},
		Embedding: config.EmbeddingConfig{
			Provider: "none",
			Model:    "nomic-embed-text",
		},
	}
	tool := NewStatsTool(manager, cfg, NewResponseWrapper(zap.NewNop()), zap.NewNop())

	result, err := tool.Handler()(context.Background(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name: tool.Name(), Arguments: map[string]any{"tenant_id": "tenant"},
	}})
	if err != nil || result.IsError {
		t.Fatalf("stats failed: result=%#v err=%v", result, err)
	}
	payload := result.StructuredContent.(map[string]interface{})
	if payload["embedding_provider"] != "none" {
		t.Fatalf("unexpected embedding provider: %#v", payload["embedding_provider"])
	}
	if payload["embedding_model"] != "" {
		t.Fatalf("disabled provider must not advertise a model: %#v", payload["embedding_model"])
	}
}

func TestTimelineToolDeduplicatesOverlappingAnchors(t *testing.T) {
	manager := &fakeBrainManager{
		q: &fakeBrainQuerier{
			chunksByID: map[string]chunkRecord{
				"chunk-1": testChunk("chunk-1", "doc-1", 1, "before"),
				"chunk-2": testChunk("chunk-2", "doc-1", 2, "anchor one"),
				"chunk-3": testChunk("chunk-3", "doc-1", 3, "anchor two"),
				"chunk-4": testChunk("chunk-4", "doc-1", 4, "after"),
			},
		},
	}
	cfg := &config.Config{Storage: config.StorageConfig{DefaultTenantID: "tenant"}}
	tool := NewTimelineTool(manager, cfg, NewResponseWrapper(zap.NewNop()), zap.NewNop())

	req := mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name: tool.Name(),
		Arguments: map[string]any{
			"tenant_id": "tenant",
			"hit_ids":   []string{"chunk:chunk-2", "chunk:chunk-3"},
			"window":    1,
		},
	}}

	result, err := tool.Handler()(context.Background(), req)
	if err != nil {
		t.Fatalf("timeline failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("timeline returned error: %#v", result.Content)
	}
	payload, ok := result.StructuredContent.(timelineResponse)
	if !ok {
		t.Fatalf("unexpected payload type: %T", result.StructuredContent)
	}
	if len(payload.Groups) != 1 {
		t.Fatalf("expected one group, got %d", len(payload.Groups))
	}
	group := payload.Groups[0]
	if len(group.Items) != 4 {
		t.Fatalf("expected four unique items, got %d", len(group.Items))
	}
	roles := map[string]string{}
	for _, item := range group.Items {
		roles[item.ID] = item.Role
	}
	if roles["chunk:chunk-2"] != "anchor" || roles["chunk:chunk-3"] != "anchor" {
		t.Fatalf("expected overlapping anchors to remain anchors, got %#v", roles)
	}
}

func TestGetToolTruncatesDocumentChunks(t *testing.T) {
	manager := &fakeBrainManager{
		q: &fakeBrainQuerier{
			chunksByID: map[string]chunkRecord{
				"chunk-1": testChunk("chunk-1", "doc-1", 1, strings.Repeat("a", 500)),
				"chunk-2": testChunk("chunk-2", "doc-1", 2, strings.Repeat("b", 500)),
				"chunk-3": testChunk("chunk-3", "doc-1", 3, strings.Repeat("c", 500)),
			},
		},
	}
	cfg := &config.Config{Storage: config.StorageConfig{DefaultTenantID: "tenant"}}
	tool := NewGetTool(manager, cfg, NewResponseWrapper(zap.NewNop()), zap.NewNop())

	req := mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name: tool.Name(),
		Arguments: map[string]any{
			"tenant_id": "tenant",
			"ids":       []string{"document:doc-1"},
			"include":   []string{"summary", "content"},
			"max_chars": 500,
		},
	}}

	result, err := tool.Handler()(context.Background(), req)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("get returned error: %#v", result.Content)
	}
	payload, ok := result.StructuredContent.(getResponse)
	if !ok {
		t.Fatalf("unexpected payload type: %T", result.StructuredContent)
	}
	if !payload.Truncated {
		t.Fatalf("expected response truncation")
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected one item, got %d", len(payload.Items))
	}
	if len(payload.Items[0].Chunks) == 0 {
		t.Fatalf("expected at least one chunk preview")
	}
}

func TestTimelineToolIncludesContentWhenRequested(t *testing.T) {
	manager := &fakeBrainManager{
		q: &fakeBrainQuerier{
			chunksByID: map[string]chunkRecord{
				"chunk-1": testChunk("chunk-1", "doc-1", 1, "line one"),
				"chunk-2": testChunk("chunk-2", "doc-1", 2, "line two"),
			},
		},
	}
	cfg := &config.Config{Storage: config.StorageConfig{DefaultTenantID: "tenant"}}
	tool := NewTimelineTool(manager, cfg, NewResponseWrapper(zap.NewNop()), zap.NewNop())

	req := mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name: tool.Name(),
		Arguments: map[string]any{
			"tenant_id": "tenant",
			"hit_ids":   []string{"chunk:chunk-2"},
			"window":    1,
			"include":   []string{"content"},
		},
	}}

	result, err := tool.Handler()(context.Background(), req)
	if err != nil {
		t.Fatalf("timeline failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("timeline returned error: %#v", result.Content)
	}

	payload := result.StructuredContent.(timelineResponse)
	if len(payload.Groups) != 1 || len(payload.Groups[0].Items) == 0 {
		t.Fatalf("expected timeline items")
	}
	foundContent := false
	for _, item := range payload.Groups[0].Items {
		if item.Content != "" {
			foundContent = true
		}
		if item.Summary != "" {
			t.Fatalf("did not expect summary when only content is requested")
		}
	}
	if !foundContent {
		t.Fatalf("expected content to be included")
	}
}

func TestTimelineToolRejectsInvalidIDKinds(t *testing.T) {
	manager := &fakeBrainManager{q: &fakeBrainQuerier{chunksByID: map[string]chunkRecord{}}}
	cfg := &config.Config{Storage: config.StorageConfig{DefaultTenantID: "tenant"}}
	tool := NewTimelineTool(manager, cfg, NewResponseWrapper(zap.NewNop()), zap.NewNop())

	req := mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name: tool.Name(),
		Arguments: map[string]any{
			"tenant_id": "tenant",
			"hit_ids":   []string{"document:doc-1"},
		},
	}}

	result, err := tool.Handler()(context.Background(), req)
	if err != nil {
		t.Fatalf("timeline failed: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected invalid kind error")
	}
}

func TestGetToolRejectsUnsupportedIDKinds(t *testing.T) {
	manager := &fakeBrainManager{q: &fakeBrainQuerier{chunksByID: map[string]chunkRecord{}}}
	cfg := &config.Config{Storage: config.StorageConfig{DefaultTenantID: "tenant"}}
	tool := NewGetTool(manager, cfg, NewResponseWrapper(zap.NewNop()), zap.NewNop())

	req := mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name: tool.Name(),
		Arguments: map[string]any{
			"tenant_id": "tenant",
			"ids":       []string{"source:src-1"},
		},
	}}

	result, err := tool.Handler()(context.Background(), req)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected unsupported id kind error")
	}
}

type fakeBrainManager struct {
	q storage.Querier
}

func (m *fakeBrainManager) Backend() string { return "duckdb" }
func (m *fakeBrainManager) Dialect() string { return "duckdb" }
func (m *fakeBrainManager) OpenTenant(context.Context, string) (storage.Querier, error) {
	return m.q, nil
}
func (m *fakeBrainManager) WithWriter(ctx context.Context, tenantID string, fn func(context.Context, storage.Querier) error) error {
	return fn(ctx, m.q)
}
func (m *fakeBrainManager) TenantPath(string) string { return "" }
func (m *fakeBrainManager) Close() error             { return nil }

type fakeBrainQuerier struct {
	chunksByID map[string]chunkRecord
}

func (q *fakeBrainQuerier) QueryContext(_ context.Context, query string, args ...any) (*stypes.QueryResult, error) {
	switch {
	case strings.Contains(query, "WHERE c.tenant_id = ? AND c.id = ?"):
		id := fmt.Sprintf("%v", args[1])
		record, ok := q.chunksByID[id]
		if !ok {
			return &stypes.QueryResult{Rows: []map[string]any{}}, nil
		}
		return &stypes.QueryResult{Rows: []map[string]any{rowFromChunk(record)}}, nil

	case strings.Contains(query, "WHERE c.tenant_id = ? AND c.document_id = ? AND c.chunk_index BETWEEN ? AND ?"):
		documentID := fmt.Sprintf("%v", args[1])
		minIndex := int(args[2].(int))
		maxIndex := int(args[3].(int))
		rows := []map[string]any{}
		for _, record := range q.chunksByID {
			if record.DocumentID != documentID {
				continue
			}
			if record.ChunkIndex < minIndex || record.ChunkIndex > maxIndex {
				continue
			}
			rows = append(rows, rowFromChunk(record))
		}
		return &stypes.QueryResult{Rows: rows}, nil

	case strings.Contains(query, "FROM documents d"):
		return &stypes.QueryResult{Rows: []map[string]any{{
			"doc_id":       "doc-1",
			"file_path":    "internal/auth/service.go",
			"title":        "Auth Service",
			"doc_type":     "code",
			"language":     "go",
			"source_id":    "src-1",
			"source_type":  "filesystem",
			"source_uri":   "/tmp/repo",
			"display_name": "repo",
		}}}, nil
	}

	return nil, fmt.Errorf("unexpected query: %s", query)
}

func (q *fakeBrainQuerier) ExecContext(context.Context, string, ...any) (int64, error) {
	return 0, nil
}

func testChunk(id, documentID string, index int, content string) chunkRecord {
	return chunkRecord{
		ChunkID:    id,
		DocumentID: documentID,
		ChunkIndex: index,
		Content:    content,
		Summary:    "summary " + content,
		ChunkType:  "function",
		SymbolName: "Fn",
		StartLine:  index * 10,
		EndLine:    index*10 + 5,
		Document: compactDocument{
			ID:       documentID,
			FilePath: "internal/auth/service.go",
			Title:    "Auth Service",
			DocType:  "code",
			Language: "go",
		},
		Source: minimalSource{
			ID:          "src-1",
			SourceType:  "filesystem",
			SourceURI:   "/tmp/repo",
			DisplayName: "repo",
		},
	}
}

func rowFromChunk(record chunkRecord) map[string]any {
	return map[string]any{
		"id":           record.ChunkID,
		"tenant_id":    "tenant",
		"document_id":  record.DocumentID,
		"chunk_index":  record.ChunkIndex,
		"content":      record.Content,
		"summary":      record.Summary,
		"context":      record.Context,
		"chunk_type":   record.ChunkType,
		"symbol_name":  record.SymbolName,
		"start_line":   record.StartLine,
		"end_line":     record.EndLine,
		"metadata":     `{"kind":"test"}`,
		"doc_id":       record.Document.ID,
		"file_path":    record.Document.FilePath,
		"title":        record.Document.Title,
		"doc_type":     record.Document.DocType,
		"language":     record.Document.Language,
		"source_id":    record.Source.ID,
		"source_type":  record.Source.SourceType,
		"source_uri":   record.Source.SourceURI,
		"display_name": record.Source.DisplayName,
	}
}

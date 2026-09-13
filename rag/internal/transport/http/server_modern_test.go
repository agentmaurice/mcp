package http

import (
	"bytes"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/config"
	mcptransport "github.com/agentmaurice/mcpchatui/mcp/rag/internal/transport/mcp"
	"go.uber.org/zap"
)

func TestBuildMountsModernMCPWithCORS(t *testing.T) {
	cfg := config.ServerConfig{BasePath: "/mcp"}
	mcpServer := mcptransport.NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cfg, zap.NewNop())
	if err := mcpServer.Build(); err != nil {
		t.Fatal(err)
	}
	httpServer := NewServer(mcpServer, nil, nil, nil, nil, nil, nil, nil, cfg, zap.NewNop())
	if err := httpServer.Build(); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "server/discover",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(stdhttp.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "server/discover")
	recorder := httptest.NewRecorder()
	httpServer.httpServer.Handler.ServeHTTP(recorder, req)
	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}

	options := httptest.NewRequest(stdhttp.MethodOptions, "/mcp", nil)
	optionsRecorder := httptest.NewRecorder()
	httpServer.httpServer.Handler.ServeHTTP(optionsRecorder, options)
	if optionsRecorder.Code != stdhttp.StatusOK {
		t.Fatalf("OPTIONS status = %d", optionsRecorder.Code)
	}
	allowedHeaders := optionsRecorder.Header().Get("Access-Control-Allow-Headers")
	for _, header := range []string{"Mcp-Protocol-Version", "Mcp-Method", "Mcp-Name", "Mcp-Session-Id", "Last-Event-ID"} {
		if !strings.Contains(allowedHeaders, header) {
			t.Fatalf("Access-Control-Allow-Headers = %q, missing %s", allowedHeaders, header)
		}
	}
}

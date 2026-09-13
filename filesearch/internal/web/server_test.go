package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/filesearch/internal/config"
	legacymcp "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

func TestHTTPServerRoutesModernTransportAndCORS(t *testing.T) {
	legacy := legacymcp.NewSSEServer(legacymcp.NewMCPServer("test", "1"))
	called := false
	modern := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	server := NewHTTPServer(config.ServerConfig{BasePath: "/mcp/filesearch"}, legacy, modern, zap.NewNop())

	request := httptest.NewRequest(http.MethodPost, "/mcp/filesearch", nil)
	recorder := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || !called {
		t.Fatalf("modern endpoint status = %d, called = %v", recorder.Code, called)
	}

	called = false
	request = httptest.NewRequest(http.MethodOptions, "/mcp/filesearch", nil)
	recorder = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("OPTIONS status = %d, want 200", recorder.Code)
	}
	if called {
		t.Fatal("modern handler called for CORS preflight")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("Access-Control-Allow-Headers is empty")
	}
}

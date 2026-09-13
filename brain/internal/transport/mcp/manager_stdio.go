package mcp

import (
	"context"
	"os"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// StdioManager manages the stdio transport lifecycle.
type StdioManager struct {
	srv    *Server
	cfg    string // default tenant ID
	logger *zap.Logger
	cancel context.CancelFunc
}

// NewStdioManager creates a new stdio transport manager.
func NewStdioManager(srv *Server, defaultTenant string, logger *zap.Logger) *StdioManager {
	return &StdioManager{srv: srv, cfg: defaultTenant, logger: logger.Named("stdio")}
}

func (m *StdioManager) Name() string { return "stdio" }

func (m *StdioManager) Start(ctx context.Context) error {
	stdioServer := server.NewStdioServer(m.srv.MCP())
	defaultTenant := m.cfg
	stdioServer.SetContextFunc(func(ctx context.Context) context.Context {
		id := shared.IdentityFromEnv(defaultTenant)
		return shared.ContextWithIdentity(ctx, id)
	})
	m.srv.stdioServer = stdioServer

	m.logger.Info("starting stdio server")

	childCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	go func() {
		if err := stdioServer.Listen(childCtx, os.Stdin, os.Stdout); err != nil {
			m.logger.Error("stdio server error", zap.Error(err))
		}
	}()

	return nil
}

func (m *StdioManager) Stop() error {
	if m.cancel != nil {
		m.cancel()
	}
	return nil
}

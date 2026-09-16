package mcp

import (
	"context"
	"os"
	"time"

	"go.uber.org/zap"
)

// StdioManager wraps stdio server lifecycle.
type StdioManager struct {
	server *Server
	logger *zap.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// NewStdioManager creates a new stdio manager.
func NewStdioManager(server *Server, logger *zap.Logger) *StdioManager {
	return &StdioManager{
		server: server,
		logger: logger.Named("mcp-stdio"),
		done:   make(chan struct{}),
	}
}

func (m *StdioManager) Name() string {
	return "mcp-stdio"
}

// Done closes when the client disconnects or the STDIO transport stops.
func (m *StdioManager) Done() <-chan struct{} {
	return m.done
}

func (m *StdioManager) Start(ctx context.Context) error {
	if m.server == nil || m.server.GetStdioServer() == nil {
		return nil
	}
	stdioCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	go func() {
		defer close(m.done)
		m.logger.Info("starting MCP STDIO transport")
		if err := m.server.GetStdioServer().Listen(stdioCtx, os.Stdin, os.Stdout); err != nil && stdioCtx.Err() == nil {
			m.logger.Error("stdio server stopped with error", zap.Error(err))
		}
	}()

	return nil
}

func (m *StdioManager) Stop() error {
	if m.cancel != nil {
		m.cancel()
	}
	if m.done != nil {
		select {
		case <-m.done:
		case <-time.After(3 * time.Second):
			m.logger.Warn("timeout waiting for stdio shutdown")
		}
	}
	return nil
}

func (m *StdioManager) Health(ctx context.Context) error {
	return nil
}

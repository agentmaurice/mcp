package mcp

import (
	"context"
	"os"

	"go.uber.org/zap"
)

// StdioManager wraps the stdio server lifecycle.
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
		logger: logger.Named("stdio"),
		done:   make(chan struct{}),
	}
}

func (m *StdioManager) Name() string {
	return "mcp-stdio"
}

func (m *StdioManager) Start(ctx context.Context) error {
	if m.server == nil || m.server.Stdio() == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	go func() {
		defer close(m.done)
		m.logger.Info("starting stdio server")
		if err := m.server.Stdio().Listen(ctx, os.Stdin, os.Stdout); err != nil {
			m.logger.Error("stdio server stopped", zap.Error(err))
		}
	}()
	return nil
}

func (m *StdioManager) Stop() error {
	if m.cancel != nil {
		m.cancel()
	}
	if m.done != nil {
		<-m.done
	}
	return nil
}

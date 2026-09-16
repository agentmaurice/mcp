package mcp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestStdioManagerReportsClientEOF(t *testing.T) {
	stdin := os.Stdin
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stdin = reader
	t.Cleanup(func() { os.Stdin = stdin; reader.Close(); writer.Close() })
	manager := NewStdioManager(&Server{stdioServer: server.NewStdioServer(server.NewMCPServer("test", "1"))}, zap.NewNop())
	require.NoError(t, manager.Start(context.Background()))
	require.NoError(t, writer.Close())
	select {
	case <-manager.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("STDIO did not report client disconnection")
	}
	require.NoError(t, manager.Stop())
}

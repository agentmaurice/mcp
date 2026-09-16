package logging

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConsoleLogsDoNotCorruptMCPStdout(t *testing.T) {
	stdout, stderr := os.Stdout, os.Stderr
	outReader, outWriter, err := os.Pipe()
	require.NoError(t, err)
	errReader, errWriter, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = outWriter, errWriter
	t.Cleanup(func() {
		os.Stdout, os.Stderr = stdout, stderr
		outReader.Close()
		outWriter.Close()
		errReader.Close()
		errWriter.Close()
	})
	logger, err := New("info")
	require.NoError(t, err)
	logger.Info("RAG startup")
	_ = logger.Sync()
	require.NoError(t, outWriter.Close())
	require.NoError(t, errWriter.Close())
	out, err := io.ReadAll(outReader)
	require.NoError(t, err)
	require.Empty(t, out, "stdout is reserved for JSON-RPC")
	diagnostic, err := io.ReadAll(errReader)
	require.NoError(t, err)
	var message map[string]interface{}
	require.NoError(t, json.Unmarshal(diagnostic, &message))
	require.Equal(t, "RAG startup", message["msg"])
}

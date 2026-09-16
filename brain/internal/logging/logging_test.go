package logging

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestLogsKeepSTDOUTAvailableForMCP(t *testing.T) {
	stdout, stderr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer errR.Close()
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = stdout, stderr; outW.Close(); errW.Close() }()
	logger, err := New("info")
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("MCP startup diagnostic")
	_ = logger.Sync()
	outW.Close()
	errW.Close()
	out, _ := io.ReadAll(outR)
	diagnostic, _ := io.ReadAll(errR)
	if len(out) != 0 {
		t.Fatalf("logs polluted MCP stdout: %s", out)
	}
	if !strings.Contains(string(diagnostic), "MCP startup diagnostic") {
		t.Fatalf("missing stderr diagnostic: %s", diagnostic)
	}
}

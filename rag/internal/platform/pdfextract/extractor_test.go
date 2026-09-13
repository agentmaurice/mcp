package pdfextract

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestIsPDFMagic(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected bool
	}{
		{"valid PDF header", []byte("%PDF-1.4 rest of file"), true},
		{"minimal PDF header", []byte("%PDF"), true},
		{"not a PDF", []byte("Hello world"), false},
		{"empty data", []byte{}, false},
		{"too short", []byte("%PD"), false},
		{"base64 encoded (not raw)", []byte("JVBER"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPDFMagic(tt.data)
			if result != tt.expected {
				t.Errorf("isPDFMagic(%q) = %v, want %v", string(tt.data[:min(len(tt.data), 10)]), result, tt.expected)
			}
		})
	}
}

func TestParsePageRange(t *testing.T) {
	tests := []struct {
		input         string
		expectedFirst int
		expectedLast  int
	}{
		{"1-5", 1, 5},
		{"3", 3, 3},
		{"10-20", 10, 20},
		{"", 0, 0},
		{"abc", 0, 0},
		{"-5", 0, 0},
		{"1-", 0, 0},
		{" 2 - 8 ", 2, 8},
	}

	for _, tt := range tests {
		t.Run("pages="+tt.input, func(t *testing.T) {
			first, last := parsePageRange(tt.input)
			if first != tt.expectedFirst || last != tt.expectedLast {
				t.Errorf("parsePageRange(%q) = (%d, %d), want (%d, %d)",
					tt.input, first, last, tt.expectedFirst, tt.expectedLast)
			}
		})
	}
}

func TestAvailable(t *testing.T) {
	// This test documents the behavior; it may pass or fail depending on
	// whether pdftotext is installed in the test environment.
	result := Available()
	t.Logf("pdftotext available: %v", result)
}

func TestExtractText_EmptyData(t *testing.T) {
	logger := zap.NewNop()
	ext := NewExtractor(Config{Enabled: true}, logger)

	_, err := ext.ExtractText(context.Background(), nil, Options{})
	if err == nil {
		t.Error("expected error for empty data")
	}
}

func TestExtractText_NotPDF(t *testing.T) {
	logger := zap.NewNop()
	ext := NewExtractor(Config{Enabled: true}, logger)

	_, err := ext.ExtractText(context.Background(), []byte("This is not a PDF"), Options{})
	if err == nil {
		t.Error("expected error for non-PDF data")
	}
}

func TestExtractText_FileSizeExceeded(t *testing.T) {
	logger := zap.NewNop()
	ext := NewExtractor(Config{
		Enabled:     true,
		MaxFileSize: 10, // 10 bytes max
	}, logger)

	// Create data that starts with PDF magic but exceeds limit
	data := []byte("%PDF-1.4 this is some data that exceeds the limit")
	_, err := ext.ExtractText(context.Background(), data, Options{})
	if err == nil {
		t.Error("expected error for oversized PDF")
	}
}

func TestBuildArgs(t *testing.T) {
	logger := zap.NewNop()

	t.Run("default layout", func(t *testing.T) {
		ext := NewExtractor(Config{PreserveLayout: true}, logger)
		args := ext.buildArgs("/tmp/test.pdf", Options{})
		assertContains(t, args, "-layout")
		assertContains(t, args, "-enc")
		assertContains(t, args, "UTF-8")
		assertContains(t, args, "/tmp/test.pdf")
		assertContains(t, args, "-")
	})

	t.Run("no layout", func(t *testing.T) {
		ext := NewExtractor(Config{PreserveLayout: false}, logger)
		args := ext.buildArgs("/tmp/test.pdf", Options{PreserveLayout: false})
		assertNotContains(t, args, "-layout")
	})

	t.Run("with page range", func(t *testing.T) {
		ext := NewExtractor(Config{}, logger)
		args := ext.buildArgs("/tmp/test.pdf", Options{Pages: "2-5"})
		assertContains(t, args, "-f")
		assertContains(t, args, "2")
		assertContains(t, args, "-l")
		assertContains(t, args, "5")
	})
}

func TestNewExtractor_Defaults(t *testing.T) {
	logger := zap.NewNop()
	ext := NewExtractor(Config{}, logger)

	if ext.maxFileSize != DefaultMaxFileSize {
		t.Errorf("maxFileSize = %d, want %d", ext.maxFileSize, DefaultMaxFileSize)
	}
	if ext.timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want %v", ext.timeout, DefaultTimeout)
	}
}

func TestNewExtractor_CustomConfig(t *testing.T) {
	logger := zap.NewNop()
	ext := NewExtractor(Config{
		MaxFileSize:    100 * 1024,
		TimeoutSeconds: 30,
		PreserveLayout: true,
	}, logger)

	if ext.maxFileSize != 100*1024 {
		t.Errorf("maxFileSize = %d, want %d", ext.maxFileSize, 100*1024)
	}
	if ext.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want %v", ext.timeout, 30*time.Second)
	}
	if !ext.preserveLayout {
		t.Error("preserveLayout should be true")
	}
}

// Helper functions

func assertContains(t *testing.T, slice []string, item string) {
	t.Helper()
	for _, s := range slice {
		if s == item {
			return
		}
	}
	t.Errorf("expected %v to contain %q", slice, item)
}

func assertNotContains(t *testing.T, slice []string, item string) {
	t.Helper()
	for _, s := range slice {
		if s == item {
			t.Errorf("expected %v to NOT contain %q", slice, item)
			return
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package pdfextract

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"go.uber.org/zap"
)

// DefaultTimeout is the default timeout for pdftotext execution.
const DefaultTimeout = 120 * time.Second

// DefaultMaxFileSize is the default maximum PDF file size (50 MB).
const DefaultMaxFileSize = 50 * 1024 * 1024

// Options configures the PDF extraction behavior.
type Options struct {
	// PreserveLayout attempts to maintain the original physical layout of the text.
	PreserveLayout bool
	// Pages selects specific pages to extract (e.g., "1-5", "3"). Empty means all pages.
	Pages string
}

// Config holds configuration for the PDF extractor.
type Config struct {
	Enabled        bool
	MaxFileSize    int64
	TimeoutSeconds int
	PreserveLayout bool
}

// Extractor wraps pdftotext for PDF text extraction.
type Extractor struct {
	maxFileSize    int64
	timeout        time.Duration
	preserveLayout bool
	logger         *zap.Logger
}

// NewExtractor creates a new PDF extractor.
func NewExtractor(cfg Config, logger *zap.Logger) *Extractor {
	maxFileSize := cfg.MaxFileSize
	if maxFileSize <= 0 {
		maxFileSize = DefaultMaxFileSize
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &Extractor{
		maxFileSize:    maxFileSize,
		timeout:        timeout,
		preserveLayout: cfg.PreserveLayout,
		logger:         logger.Named("pdfextract"),
	}
}

// Available checks if pdftotext is available in the system PATH.
func Available() bool {
	_, err := exec.LookPath("pdftotext")
	return err == nil
}

// ExtractText extracts text from PDF binary data using pdftotext.
// The opts parameter can override the default extractor settings per call.
func (e *Extractor) ExtractText(ctx context.Context, pdfData []byte, opts Options) (string, error) {
	if len(pdfData) == 0 {
		return "", fmt.Errorf("empty PDF data")
	}

	// Check file size limit
	if int64(len(pdfData)) > e.maxFileSize {
		return "", fmt.Errorf("PDF file size %d bytes exceeds maximum %d bytes", len(pdfData), e.maxFileSize)
	}

	// Validate PDF magic bytes
	if !isPDFMagic(pdfData) {
		return "", fmt.Errorf("data does not appear to be a valid PDF (missing %%PDF header)")
	}

	// Write PDF data to a temporary file
	tmpFile, err := os.CreateTemp("", "rag-pdf-*.pdf")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(pdfData); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("failed to write PDF to temp file: %w", err)
	}
	tmpFile.Close()

	// Build pdftotext command arguments
	args := e.buildArgs(tmpPath, opts)

	// Create context with timeout
	execCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	e.logger.Debug("executing pdftotext",
		zap.String("tmp_file", tmpPath),
		zap.Int("pdf_size", len(pdfData)),
		zap.Strings("args", args))

	// Execute pdftotext
	cmd := exec.CommandContext(execCtx, "pdftotext", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("pdftotext timed out after %s", e.timeout)
		}
		return "", fmt.Errorf("pdftotext failed: %w (stderr: %s)", err, stderr.String())
	}

	// Sanitize output to valid UTF-8
	extracted := shared.SanitizeUTF8Clean(stdout.String())
	extracted = strings.TrimSpace(extracted)

	if extracted == "" {
		return "", fmt.Errorf("pdftotext produced no text output (PDF may be image-based or empty)")
	}

	e.logger.Info("PDF text extracted",
		zap.Int("pdf_size", len(pdfData)),
		zap.Int("extracted_length", len(extracted)))

	return extracted, nil
}

// buildArgs builds the pdftotext command-line arguments.
func (e *Extractor) buildArgs(tmpPath string, opts Options) []string {
	var args []string

	// Layout option: use per-call option if set, otherwise use extractor default
	if opts.PreserveLayout || e.preserveLayout {
		args = append(args, "-layout")
	}

	// Page selection
	if opts.Pages != "" {
		first, last := parsePageRange(opts.Pages)
		if first > 0 {
			args = append(args, "-f", strconv.Itoa(first))
		}
		if last > 0 {
			args = append(args, "-l", strconv.Itoa(last))
		}
	}

	// Encoding
	args = append(args, "-enc", "UTF-8")

	// Input file and output to stdout ("-")
	args = append(args, tmpPath, "-")

	return args
}

// parsePageRange parses a page range string like "1-5" or "3" into first/last page numbers.
func parsePageRange(pages string) (first, last int) {
	pages = strings.TrimSpace(pages)
	if pages == "" {
		return 0, 0
	}

	parts := strings.SplitN(pages, "-", 2)
	if len(parts) == 1 {
		// Single page number
		n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || n <= 0 {
			return 0, 0
		}
		return n, n
	}

	// Range: "first-last"
	f, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	l, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || f <= 0 || l <= 0 {
		return 0, 0
	}
	return f, l
}

// isPDFMagic checks if data starts with the PDF magic bytes (%PDF).
func isPDFMagic(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	return data[0] == '%' && data[1] == 'P' && data[2] == 'D' && data[3] == 'F'
}

package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/agentmaurice/mcpchatui/mcp/form/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/form/internal/pdfform"
)

const mimePDF = "application/pdf"

// Stable error codes owned by the server (the PDF ones live in package pdfform).
const (
	codeInvalidArguments = "invalid_arguments"
	codeFileTooLarge     = "file_too_large"
	codeStorageRequired  = "storage_required"
	codeBufferUnavail    = "buffer_unavailable"
	codeBufferNotFound   = "buffer_not_found"
	codeBufferFailed     = "buffer_failed"
	codeInvalidOutput    = "invalid_output"
)

type toolError struct {
	code, message string
	details       any
}

func (e *toolError) Error() string { return e.code + ": " + e.message }

func terr(code, message string) *toolError { return &toolError{code: code, message: message} }

// fileInput is an input file: inline base64 or a Buffer reference.
type fileInput struct {
	ContentBase64 string `json:"content_base64"`
	Ref           string `json:"ref"`
}

// load returns the bytes of an input file, checked to look like a PDF.
func (h *handlers) load(ctx context.Context, name string, in fileInput) ([]byte, error) {
	hasB64, hasRef := strings.TrimSpace(in.ContentBase64) != "", strings.TrimSpace(in.Ref) != ""
	switch {
	case hasB64 == hasRef:
		return nil, terr(codeInvalidArguments, name+" needs exactly one of content_base64 or ref")
	case hasB64:
		if len(in.ContentBase64) > h.deps.MaxInputBytes*4/3+8 {
			return nil, terr(codeFileTooLarge, name+" exceeds the input size limit")
		}
		data, err := base64.StdEncoding.DecodeString(in.ContentBase64)
		if err != nil {
			return nil, terr(codeInvalidArguments, name+".content_base64 is not valid base64")
		}
		return h.checkPDF(name, data)
	default:
		if h.deps.Buffer == nil {
			return nil, terr(codeBufferUnavail, "no Buffer service is configured: pass content_base64 instead of ref")
		}
		if !strings.HasPrefix(in.Ref, "buffer://") {
			return nil, terr(codeInvalidArguments, name+".ref must be a buffer:// reference")
		}
		data, err := h.deps.Buffer.Load(ctx, in.Ref, h.deps.MaxInputBytes)
		switch {
		case errors.Is(err, buffer.ErrNotFound):
			return nil, terr(codeBufferNotFound, name+".ref does not exist or has expired")
		case errors.Is(err, buffer.ErrTooLarge):
			return nil, terr(codeFileTooLarge, name+" exceeds the input size limit")
		case err != nil:
			return nil, terr(codeBufferFailed, err.Error())
		}
		return h.checkPDF(name, data)
	}
}

func (h *handlers) checkPDF(name string, data []byte) ([]byte, error) {
	if len(data) > h.deps.MaxInputBytes {
		return nil, terr(codeFileTooLarge, name+" exceeds the input size limit")
	}
	head := data[:min(len(data), 1024)]
	if !bytes.Contains(head, []byte("%PDF-")) {
		return nil, &pdfform.Error{Code: pdfform.CodeInvalidPDF, Message: name + " is not a PDF file"}
	}
	return data, nil
}

// cleanFilename keeps a plain file name with the .pdf extension.
func cleanFilename(name, fallback string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			continue
		}
		b.WriteRune(r)
	}
	name = strings.Trim(b.String(), " .")
	if name == "" || name == "." || name == ".." {
		name = fallback
	}
	name = strings.TrimSuffix(name, ".pdf")
	name = strings.TrimSuffix(name, ".PDF")
	if len(name) > maxFilenameLength {
		name = name[:maxFilenameLength]
	}
	if name == "" {
		name = fallback
	}
	return name + ".pdf"
}

type fileOut struct {
	Filename      string  `json:"filename"`
	MimeType      string  `json:"mime_type"`
	Bytes         int     `json:"bytes"`
	SHA256        string  `json:"sha256"`
	ContentBase64 *string `json:"content_base64,omitempty"`
	Ref           string  `json:"ref,omitempty"`
}

// emit builds the `file` and `outputs` members of a result: a Buffer upload when
// configured, otherwise inline base64 up to the inline limit.
func (h *handlers) emit(ctx context.Context, tool, filename string, data []byte, out map[string]any) error {
	f := fileOut{Filename: filename, MimeType: mimePDF, Bytes: len(data), SHA256: pdfform.SHA(data)}
	if h.deps.Buffer != nil {
		ref, err := h.deps.Buffer.Upload(ctx, tool, filename, mimePDF, data, f.SHA256)
		if err != nil {
			return terr(codeBufferFailed, err.Error())
		}
		f.Ref = ref
		out["outputs"] = []map[string]any{{"ref": ref, "mime_type": mimePDF, "bytes": f.Bytes, "sha256": f.SHA256,
			"metadata": map[string]any{"filename": filename, "sha256": f.SHA256, "bytes": f.Bytes}}}
	} else {
		if f.Bytes > h.deps.MaxInlineBytes {
			return terr(codeStorageRequired, "the produced file exceeds the inline limit and no Buffer service is configured")
		}
		enc := base64.StdEncoding.EncodeToString(data)
		f.ContentBase64 = &enc
	}
	out["file"] = f
	return nil
}

// checkOutput verifies a produced file before it is reported as a document:
// non-empty, within the input limit, a PDF header and an end-of-file marker.
func checkOutput(data []byte, max int) error {
	bad := func(why string) error { return terr(codeInvalidOutput, "the produced PDF is invalid: "+why) }
	switch {
	case len(data) == 0:
		return bad("empty")
	case len(data) > max:
		return bad("too large")
	case !bytes.HasPrefix(data, []byte("%PDF-")):
		return bad("no PDF header")
	case !bytes.Contains(data[max0(len(data)-1024):], []byte("%%EOF")):
		return bad("no end-of-file marker")
	}
	return nil
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

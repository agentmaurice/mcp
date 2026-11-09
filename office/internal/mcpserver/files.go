package mcpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/agentmaurice/mcpchatui/mcp/office/internal/buffer"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	mimeDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimeXlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

// Stable error codes owned by the server (the engine ones live in package engine).
const (
	codeInvalidArguments = "invalid_arguments"
	codeFileTooLarge     = "file_too_large"
	codeStorageRequired  = "storage_required"
	codeBufferUnavail    = "buffer_unavailable"
	codeBufferNotFound   = "buffer_not_found"
	codeBufferFailed     = "buffer_failed"
	codeUnsupported      = "unsupported_format"
)

type toolError struct {
	code, message string
	details       any
}

func (e *toolError) Error() string { return e.code + ": " + e.message }

func terr(code, message string) *toolError { return &toolError{code: code, message: message} }

func mimeFor(format string) string {
	if format == "xlsx" {
		return mimeXlsx
	}
	return mimeDocx
}

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fileInput is an input file: inline base64 or a Buffer reference.
type fileInput struct {
	ContentBase64 string `json:"content_base64"`
	Ref           string `json:"ref"`
}

// load returns the bytes of an input file, checked to be an OPC (zip) package.
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
		return checkPackage(name, data, h.deps.MaxInputBytes)
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
		return checkPackage(name, data, h.deps.MaxInputBytes)
	}
}

func checkPackage(name string, data []byte, max int) ([]byte, error) {
	if len(data) > max {
		return nil, terr(codeFileTooLarge, name+" exceeds the input size limit")
	}
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return nil, &toolError{code: "invalid_document", message: name + " is not a DOCX or XLSX package"}
	}
	return data, nil
}

// writeInput stores an input file in the call directory.
func writeInput(path string, data []byte) error { return os.WriteFile(path, data, 0o600) }

// cleanFilename keeps a plain file name with the right extension.
func cleanFilename(name, format, fallback string) string {
	ext := "." + format
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
	name = strings.TrimSuffix(name, ext)
	name = strings.TrimSuffix(name, strings.ToUpper(ext))
	if len(name) > 100 {
		name = name[:100]
	}
	if name == "" {
		name = fallback
	}
	return name + ext
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
func (h *handlers) emit(ctx context.Context, tool, filename, mime string, data []byte, out map[string]any) error {
	f := fileOut{Filename: filename, MimeType: mime, Bytes: len(data), SHA256: hexSHA(data)}
	if h.deps.Buffer != nil {
		ref, err := h.deps.Buffer.Upload(ctx, tool, filename, mime, data, f.SHA256)
		if err != nil {
			return terr(codeBufferFailed, err.Error())
		}
		f.Ref = ref
		out["outputs"] = []map[string]any{{"ref": ref, "mime_type": mime, "bytes": f.Bytes, "sha256": f.SHA256,
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

func failure(code, message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"error": code, "message": message}}
}

func failureWith(code, message string, details any) *mcp.CallToolResult {
	body := map[string]any{"error": code, "message": message}
	if details != nil {
		body["details"] = details
	}
	return &mcp.CallToolResult{IsError: true, StructuredContent: body}
}

const codeInvalidOutput = "invalid_output"

// checkOutput verifies a produced file before it is reported as a document:
// non-empty, within the limit, a ZIP package with [Content_Types].xml and the
// main part of the requested format, well-formed roots, a content type for
// every part and relationships whose internal targets exist.
func checkOutput(format string, data []byte, max int) error {
	bad := func(why string) error {
		return terr(codeInvalidOutput, "the engine produced an invalid "+format+" file: "+why)
	}
	if len(data) == 0 {
		return bad("empty")
	}
	if len(data) > max {
		return bad("too large")
	}
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return bad("not a ZIP package")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return bad("unreadable ZIP package")
	}
	main := "word/document.xml"
	if format == "xlsx" {
		main = "xl/workbook.xml"
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	ct, doc := files["[Content_Types].xml"], files[main]
	if ct == nil || doc == nil {
		return bad("required parts are missing")
	}
	if err := checkXMLRoot(ct, "Types", nsContentTypes); err != nil {
		return bad("[Content_Types].xml is not valid OOXML")
	}
	local, spaces := "document", []string{nsWordML, nsWordMLStrict}
	if format == "xlsx" {
		local, spaces = "workbook", []string{nsSheetML, nsSheetMLStrict}
	}
	if err := checkXMLRoot(doc, local, spaces...); err != nil {
		return bad(main + " is not valid OOXML")
	}
	budget := int64(maxRelsTotalBytes)
	if err := checkContentTypes(files, &budget); err != nil {
		return bad("[Content_Types].xml does not type every part")
	}
	if err := checkRelationships(files, &budget); err != nil {
		return bad("a relationship is broken: " + err.Error())
	}
	return nil
}

const (
	nsContentTypes  = "http://schemas.openxmlformats.org/package/2006/content-types"
	nsWordML        = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsWordMLStrict  = "http://purl.oclc.org/ooxml/wordprocessingml/main"
	nsSheetML       = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	nsSheetMLStrict = "http://purl.oclc.org/ooxml/spreadsheetml/main"
	maxXMLPartBytes = 8 << 20
)

// checkXMLRoot reads a part (bounded), requires well-formed XML and the given
// root element in one of the OOXML namespaces.
func checkXMLRoot(f *zip.File, local string, namespaces ...string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	lr := &io.LimitedReader{R: rc, N: maxXMLPartBytes + 1}
	dec := xml.NewDecoder(lr)
	rootSeen := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if lr.N <= 0 {
			return errors.New("part too large")
		}
		if se, ok := tok.(xml.StartElement); ok && !rootSeen {
			rootSeen = true
			okNS := false
			for _, ns := range namespaces {
				okNS = okNS || se.Name.Space == ns
			}
			if se.Name.Local != local || !okNS {
				return errors.New("unexpected root element")
			}
		}
	}
	if !rootSeen {
		return errors.New("no root element")
	}
	return nil
}

// Package mcpserver exposes the Form MCP: inspect and fill the AcroForm fields
// of an existing PDF without breaking the rest of the file.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/agentmaurice/mcpchatui/mcp/form/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/form/internal/pdfform"
	"github.com/agentmaurice/mcpchatui/mcp/form/internal/version"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
	pdfcpuModel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

var serviceVersion = version.Version

const (
	maxInlineBytes    = 2 * 1024 * 1024
	defaultMaxInput   = 20 * 1024 * 1024
	defaultMaxValues  = 2000
	maxValuesBytes    = 1 << 20
	maxListedFields   = 5000
	maxFilenameLength = 100
	engineLicense     = "Apache-2.0"
	engineName        = "pdfcpu"
)

// injectedFields are the identifiers the One adds to every tool call; they are
// removed before the strict argument validation.
var injectedFields = []string{"deployment_id", "organization_id", "user_id", "space_id", "session_id"}

// Deps are the collaborators and limits of the server.
type Deps struct {
	Buffer         *buffer.Client // nil when the Buffer service is not configured
	MaxInputBytes  int
	MaxInlineBytes int // inline output limit without a Buffer (default 2 MiB)
	MaxValues      int // fields per fill (default 2000)
}

type handlers struct{ deps Deps }

// New builds the server from the environment: FORM_MAX_INPUT_BYTES (20 MiB),
// FORM_MAX_VALUES (2000) and the FORM_BUFFER_* variables (or the generic
// BUFFER_SERVICE_URL, BUFFER_TOKEN, BUFFER_NAMESPACE).
func New() *modernmcp.Server { return NewWithDeps(DepsFromEnv()) }

// DepsFromEnv reads the configuration from the environment.
func DepsFromEnv() Deps {
	geti := func(name string, def int) int {
		if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
			return v
		}
		return def
	}
	return Deps{
		Buffer:        buffer.FromEnv(),
		MaxInputBytes: geti("FORM_MAX_INPUT_BYTES", defaultMaxInput),
		MaxValues:     geti("FORM_MAX_VALUES", defaultMaxValues),
	}
}

// NewWithDeps builds the server with explicit dependencies (tests).
func NewWithDeps(deps Deps) *modernmcp.Server {
	if deps.MaxInputBytes <= 0 {
		deps.MaxInputBytes = defaultMaxInput
	}
	if deps.MaxInlineBytes <= 0 {
		deps.MaxInlineBytes = maxInlineBytes
	}
	if deps.MaxValues <= 0 {
		deps.MaxValues = defaultMaxValues
	}
	h := &handlers{deps: deps}
	s := modernmcp.New("agentmaurice-form", serviceVersion, "Use Form to list the fields of an existing PDF form (AcroForm) and to fill them by field name, atomically, without touching the rest of the file. Encrypted, signed and XFA PDFs are refused.")
	s.AddTool(readTool("form_health_v1", "Check the Form MCP.", schema(nil, nil)), h.health)
	s.AddTool(readTool("form_capabilities_v1", "Describe the field types, limits, refusals and error codes of the Form MCP.", schema(nil, nil)), h.capabilities)
	s.AddTool(readTool("form_inspect_v1", inspectDescription, inspectSchema()), h.inspect)
	s.AddTool(writeTool("form_fill_v1", fillDescription, fillSchema()), h.fill)
	return s
}

func (h *handlers) health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]any{"status": "ok", "version": serviceVersion}), nil
}

func (h *handlers) capabilities(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]any{
		"status":  "ok",
		"version": serviceVersion,
		"engine":  map[string]any{"name": engineName, "version": pdfcpuModel.VersionStr, "license": engineLicense},
		"tools":   []string{"form_health_v1", "form_capabilities_v1", "form_inspect_v1", "form_fill_v1"},
		"field_types": map[string]any{
			"text":      "string; max_length counted in characters; line breaks (\n, never \r) only when multiline; subtype date is a text field",
			"checkbox":  "true or false (the strings true, false, yes, no, on, off, 1, 0 are accepted)",
			"radio":     "one of options",
			"combobox":  "one of options (a free value is refused: pdfcpu drops it)",
			"listbox":   "one of options, or a list of options when multiple is true",
			"signature": "listed, never filled",
			"button":    "push buttons are listed, never filled",
		},
		"refusals": map[string]any{
			pdfform.CodeEncrypted: "the PDF is encrypted (with or without a password)",
			pdfform.CodeSigned:    "the PDF carries a digital signature: any rewrite would invalidate it",
			pdfform.CodeXFA:       "the PDF is an XFA form",
		},
		"fill": map[string]any{
			"atomic":    true,
			"flatten":   "optional, default false; draws the appearances into the page content and removes the fields (implemented on the pdfcpu object model, pdfcpu has no flatten API)",
			"integrity": []string{"pdfcpu validation of the output", "same page count", "every written value read back identically", "no other field changed", "page content unchanged (without flatten)", "no signature", "SHA-256 of input and output"},
			"read_only": "read-only fields are refused with form_field_readonly",
		},
		"limits": map[string]any{
			"max_input_bytes": h.deps.MaxInputBytes, "max_values": h.deps.MaxValues, "max_values_bytes": maxValuesBytes,
			"max_inline_output_bytes": h.deps.MaxInlineBytes, "max_listed_fields": maxListedFields,
		},
		"error_codes": []string{
			codeInvalidArguments, codeFileTooLarge, codeStorageRequired, codeBufferUnavail, codeBufferNotFound, codeBufferFailed, codeInvalidOutput,
			pdfform.CodeInvalidPDF, pdfform.CodeNoForm, pdfform.CodeEncrypted, pdfform.CodeSigned, pdfform.CodeXFA,
			pdfform.CodeUnknownField, pdfform.CodeTypeMismatch, pdfform.CodeUnknownOption, pdfform.CodeValueTooLong,
			pdfform.CodeFieldReadOnly, pdfform.CodeInvalidValue, pdfform.CodeFlattenFailed, pdfform.CodeIntegrityFailed, pdfform.CodeFillFailed, pdfform.CodeCancelled,
		},
		"buffer_configured": h.deps.Buffer != nil,
		"output_mime_types": []string{mimePDF},
	}), nil
}

// decode removes the identifiers injected by the One, then decodes strictly.
func decode(request mcp.CallToolRequest, target any) error {
	args := map[string]any{}
	if m, ok := request.Params.Arguments.(map[string]any); ok {
		for k, v := range m {
			args[k] = v
		}
	} else if request.Params.Arguments != nil {
		b, err := json.Marshal(request.Params.Arguments)
		if err != nil || json.Unmarshal(b, &args) != nil {
			return errors.New("arguments must be an object")
		}
	}
	for _, k := range injectedFields {
		delete(args, k)
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("arguments do not match the schema: %w", err)
	}
	return nil
}

func readTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := true, false, true
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}

func writeTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	// The tool never overwrites its input: it returns a new file.
	readOnly, destructive, idempotent := false, false, true
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}

func schema(properties map[string]any, required []string) mcp.ToolInputSchema {
	if properties == nil {
		properties = map[string]any{}
	}
	return mcp.ToolInputSchema{Type: "object", Properties: properties, Required: required}
}

func result(content any) *mcp.CallToolResult {
	return &mcp.CallToolResult{StructuredContent: content}
}

// failure is a business error: isError is true, with a stable code.
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

// fail converts any error into an isError result with a stable code.
func fail(err error) *mcp.CallToolResult {
	var te *toolError
	var pe *pdfform.Error
	switch {
	case errors.As(err, &te):
		return failureWith(te.code, te.message, te.details)
	case errors.As(err, &pe):
		if pe.Field != "" {
			return failureWith(pe.Code, pe.Message, map[string]any{"field": pe.Field})
		}
		return failure(pe.Code, pe.Message)
	default:
		return failure(pdfform.CodeFillFailed, "unexpected failure")
	}
}

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/office/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/office/internal/engine"
	"github.com/agentmaurice/mcpchatui/mcp/office/internal/version"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/mark3labs/mcp-go/mcp"
)

var serviceVersion = version.Version

const (
	maxInlineBytes      = 2 * 1024 * 1024
	defaultMaxInput     = 20 * 1024 * 1024
	defaultMaxContent   = 4 * 1024 * 1024
	maxAuthorLength     = 100
	engineLicense       = "Apache-2.0"
	engineAttributeName = "GenOffice"
)

// Deps are the collaborators and limits of the server.
type Deps struct {
	Engine          *engine.Runner // nil or unconfigured: tools answer engine_unavailable
	Buffer          *buffer.Client // nil when the Buffer service is not configured
	MaxInputBytes   int
	MaxContentBytes int
	MaxInlineBytes  int    // inline output limit without a Buffer (default 2 MiB)
	EngineVersion   string // version of the engine sources the image was built from (OFFICE_ENGINE_VERSION)
}

type handlers struct {
	deps   Deps
	xlsxOK bool // workbook helper checked once at start
}

// New builds the server from the environment: OFFICE_CLI_PATH,
// OFFICE_XLSX_SIDECAR_PATH (or XLSX_SIDECAR_PATH), OFFICE_CLI_TIMEOUT_SECONDS (60),
// OFFICE_MAX_CONCURRENCY (2), OFFICE_MAX_INPUT_BYTES (20 MiB),
// OFFICE_ENGINE_VERSION, and the OFFICE_BUFFER_* variables.
func New() *modernmcp.Server { return NewWithDeps(DepsFromEnv()) }

// DepsFromEnv reads the configuration from the environment.
func DepsFromEnv() Deps {
	geti := func(name string, def int) int {
		if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
			return v
		}
		return def
	}
	sidecar := os.Getenv("OFFICE_XLSX_SIDECAR_PATH")
	if sidecar == "" {
		sidecar = os.Getenv("XLSX_SIDECAR_PATH")
	}
	r := engine.New(os.Getenv("OFFICE_CLI_PATH"), sidecar, time.Duration(geti("OFFICE_CLI_TIMEOUT_SECONDS", 60))*time.Second, geti("OFFICE_MAX_CONCURRENCY", 2))
	r.PdfiumWasm = os.Getenv("OFFICE_PDFIUM_WASM_PATH")
	return Deps{
		Engine:          r,
		Buffer:          buffer.FromEnv(),
		MaxInputBytes:   geti("OFFICE_MAX_INPUT_BYTES", defaultMaxInput),
		MaxContentBytes: defaultMaxContent,
		EngineVersion:   os.Getenv("OFFICE_ENGINE_VERSION"),
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
	if deps.MaxContentBytes <= 0 {
		deps.MaxContentBytes = defaultMaxContent
	}
	h := &handlers{deps: deps, xlsxOK: deps.Engine != nil && sidecarUsable(deps.Engine.XlsxSidecar)}
	s := modernmcp.New("agentmaurice-office", serviceVersion, "Use Office to create DOCX and XLSX files, edit them with structured operations, compare two DOCX files into tracked changes, and inspect DOCX and XLSX files.")
	s.AddTool(readTool("office_health_v1", "Check the Office MCP.", schema(nil, nil)), h.health)
	s.AddTool(readTool("office_capabilities_v1", "Describe the supported formats, operations and limits of the Office MCP.", schema(nil, nil)), h.capabilities)
	s.AddTool(writeTool("office_create_v1", createDescription, createSchema()), h.create)
	s.AddTool(writeTool("office_edit_v1", editDescription, editSchema()), h.edit)
	s.AddTool(writeTool("office_compare_v1", "Compare two DOCX files and return a DOCX whose differences are tracked changes (insertions and deletions), with their counts.", compareSchema()), h.compare)
	s.AddTool(readTool("office_inspect_v1", "Read a DOCX or XLSX file: its text, its structure (headings, tables, sheets) and the issues found by the document checks.", inspectSchema()), h.inspect)
	return s
}

func (h *handlers) health(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(map[string]any{"status": "ok", "version": serviceVersion}), nil
}

func decode(request mcp.CallToolRequest, target any) error {
	payload, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("arguments do not match schema: %w", err)
	}
	return nil
}

func readTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
	readOnly, destructive, idempotent := true, false, true
	return mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent}}
}

func writeTool(name, description string, input mcp.ToolInputSchema) mcp.Tool {
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

// fail converts any error into an isError result with a stable code.
func fail(err error) *mcp.CallToolResult {
	switch e := err.(type) {
	case *toolError:
		return failureWith(e.code, e.message, e.details)
	case *engine.Error:
		return failureWith(e.Code, e.Message, e.Details)
	default:
		return failure(engine.CodeFailed, "unexpected failure")
	}
}

// begin starts an engine call; the caller must End it.
func (h *handlers) begin(ctx context.Context) (*engine.Call, error) {
	return h.deps.Engine.Begin(ctx)
}

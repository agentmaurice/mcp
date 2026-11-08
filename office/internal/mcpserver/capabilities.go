package mcpserver

import (
	"context"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/office/internal/engine"
	"github.com/mark3labs/mcp-go/mcp"
)

// capabilities reports the engine (the only place that names it, for
// attribution), the formats available in this deployment and the limits.
func (h *handlers) capabilities(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	available := false
	cliVersion := ""
	if h.deps.Engine.Configured() {
		vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if c, err := h.begin(vctx); err == nil {
			if v, err := c.Version(); err == nil {
				available, cliVersion = true, v
			}
			c.End()
		}
		cancel()
	}
	engineVersion := h.deps.EngineVersion
	if engineVersion == "" {
		engineVersion = cliVersion
	}
	xlsx := h.xlsxAvailable()
	edit := []string{"docx"}
	inspect := []string{"docx"}
	if xlsx {
		edit = append(edit, "xlsx")
		inspect = append(inspect, "xlsx")
	}
	timeout := 0
	concurrency := 0
	if h.deps.Engine != nil {
		timeout = int(h.deps.Engine.Timeout / time.Second)
		concurrency = cap(engineSlots(h.deps.Engine))
	}
	return result(map[string]any{
		"version": serviceVersion,
		"engine": map[string]any{
			"name": engineAttributeName, "version": engineVersion, "license": engineLicense, "available": available,
		},
		"formats": map[string]any{
			"create":  []string{"docx", "xlsx"},
			"edit":    edit,
			"compare": []string{"docx"},
			"inspect": inspect,
		},
		"create_sources": map[string]any{"docx": []string{"markdown", "html"}, "xlsx": []string{"json"}},
		"edit_ops":       map[string]any{"docx": sortedCopy(docxOps), "xlsx": sortedCopy(xlsxOps), "xlsx_cell_entries": "{cell, sheet?, value | formula}"},
		"limits": map[string]any{
			"max_input_bytes": h.deps.MaxInputBytes, "max_content_bytes": h.deps.MaxContentBytes,
			"max_inline_output_bytes": maxInlineBytes, "max_ops": maxOps,
			"cli_timeout_seconds": timeout, "max_concurrency": concurrency, "max_inspect_text_bytes": maxInspectText,
		},
		"error_codes": []string{
			codeInvalidArguments, codeUnsupported, codeFileTooLarge, "invalid_document", engine.CodeOpRejected,
			engine.CodeUnavailable, engine.CodeTimeout, engine.CodeFailed, engine.CodeBusy,
			codeInvalidOutput, codeStorageRequired, codeBufferUnavail, codeBufferNotFound, codeBufferFailed,
		},
		"buffer_configured": h.deps.Buffer != nil,
		"output_mime_types": []string{mimeDocx, mimeXlsx},
	}), nil
}

func engineSlots(r *engine.Runner) chan struct{} { return r.Slots() }

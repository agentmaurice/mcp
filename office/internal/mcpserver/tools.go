package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/agentmaurice/mcpchatui/mcp/office/internal/engine"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	maxInspectText = 200 * 1024
	maxInspectCols = 10 // sheets read by inspect
)

const createDescription = "Create a DOCX or XLSX file from content. DOCX: source_format markdown or html (a restricted HTML fragment: headings, paragraphs, lists, tables, bold/italic, links). XLSX: source_format json, either a 2-D array of cell values or {\"sheets\":[{\"name\":\"...\",\"rows\":[[...]]}]}; a string starting with \"=\" is a formula. Returns the file (a Buffer reference when a Buffer is configured, otherwise inline base64 up to 2 MiB)."

const editDescription = `Apply an ordered, atomic batch of structured operations (at most 200) to a DOCX or XLSX file: if one operation is rejected nothing is written (error op_rejected, with the rejected operations in details).
DOCX operations, each an object with "op": findReplace {find, replace, matchCase?, target?}, deleteBlocks {target}, moveBlocks {blockIndexes, afterBlockIndex}, setHeadingLevel {target, level}, setFont / setMatchedFont / setParagraphFormat / applyStyle / setList / clearList, insertToc, insertField, insertBookmark, updateFields, table operations (insertTableRow, deleteTableRow, insertTableColumn, deleteTableColumn, mergeTableCells, splitTableCell, setTableCellFormat, setTableStyle), insert_content {html, afterBlockIndex?}, replace_blocks {startBlockIndex, endBlockIndex, html}, insert_text_box, insert_chart, edit_chart, set_header_footer, set_page_setup, insert_section_break, define_style, add_comment / reply_comment / resolve_comment / delete_comment, accept_changes / reject_changes, insert_footnote / insert_endnote / edit_note / delete_note, set_properties. A target is {"blockIndexes":[...]} or {"nodeType":"docHeading"|"docParagraph"|"docListItem","headingLevel"?}; block indexes come from office_inspect_v1 or a previous read. With track=true the content edits are recorded as tracked changes (author defaults to "Office"). Pictures and external content are not supported.
XLSX operations: either cell entries {cell:"B2", sheet?, value | formula} or operation entries with "op" (set_cell {sheet?, address, value}, set_formula {sheet?, address, formula}, set_range {sheet?, start, values:[[...]]}, clear_range, fill_range, copy_range, move_range, find_replace, sort_range, convert_to_values, format_range, merge_cells, set_col_width, insert_rows, delete_rows, add_sheet, rename_sheet and others); do not mix the two styles in one call. Returns the edited file and "applied", the number of operations the engine reports as applied (an answer without that report fails with invalid_output). If the engine reports failed, skipped or fewer operations than requested the call fails with op_rejected (details: applied, failed, skipped) and no file is returned.`

func fileSchema(desc string) map[string]any {
	return map[string]any{"type": "object", "description": desc,
		"properties": map[string]any{
			"content_base64": map[string]any{"type": "string", "description": "The file, base64 encoded (20 MiB decoded at most)."},
			"ref":            map[string]any{"type": "string", "description": "A buffer:// reference, read from the Buffer service when one is configured."},
		}}
}

func formatProp(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func createSchema() mcp.ToolInputSchema {
	return schema(map[string]any{
		"format":        formatProp("docx", "xlsx"),
		"source_format": formatProp("markdown", "html", "json"),
		"content":       map[string]any{"type": "string", "maxLength": defaultMaxContent, "description": "The source text. DOCX takes markdown or html; XLSX takes json."},
		"filename":      map[string]any{"type": "string", "description": "Optional output file name (the extension is set from format)."},
	}, []string{"format", "source_format", "content"})
}

func editSchema() mcp.ToolInputSchema {
	return schema(map[string]any{
		"format":   formatProp("docx", "xlsx"),
		"file":     fileSchema("The file to edit."),
		"ops":      map[string]any{"type": "array", "maxItems": maxOps, "items": map[string]any{"type": "object"}, "description": "Ordered operations, see the tool description."},
		"track":    map[string]any{"type": "boolean", "description": "DOCX only: record content edits as tracked changes."},
		"author":   map[string]any{"type": "string", "description": "DOCX with track: author of the tracked changes (default Office)."},
		"filename": map[string]any{"type": "string", "description": "Optional output file name."},
	}, []string{"format", "file", "ops"})
}

func compareSchema() mcp.ToolInputSchema {
	return schema(map[string]any{
		"format":   formatProp("docx"),
		"before":   fileSchema("The original DOCX."),
		"after":    fileSchema("The revised DOCX."),
		"author":   map[string]any{"type": "string", "description": "Author recorded on every tracked change (default Office)."},
		"filename": map[string]any{"type": "string", "description": "Optional output file name."},
	}, []string{"before", "after"})
}

func inspectSchema() mcp.ToolInputSchema {
	return schema(map[string]any{
		"format": formatProp("docx", "xlsx"),
		"file":   fileSchema("The file to inspect."),
	}, []string{"format", "file"})
}

func cleanAuthor(a string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(a) {
		if !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > maxAuthorLength {
		s = s[:maxAuthorLength]
	}
	if s == "" {
		return "Office"
	}
	return s
}

func (h *handlers) readOutput(c *engine.Call, name, format string) ([]byte, error) {
	data, err := os.ReadFile(c.Path(name))
	if err != nil {
		return nil, terr(codeInvalidOutput, "the engine did not produce the file")
	}
	if err := checkOutput(format, data, h.deps.MaxInputBytes*2); err != nil {
		return nil, err
	}
	return data, nil
}

// --- create ---

func (h *handlers) create(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		Format       string `json:"format"`
		SourceFormat string `json:"source_format"`
		Content      string `json:"content"`
		Filename     string `json:"filename"`
	}
	if err := decode(request, &in); err != nil {
		return failure(codeInvalidArguments, err.Error()), nil
	}
	in.Format = strings.ToLower(in.Format)
	in.SourceFormat = strings.ToLower(in.SourceFormat)
	var ext string
	switch {
	case in.Format == "docx" && in.SourceFormat == "markdown":
		ext = "md"
	case in.Format == "docx" && in.SourceFormat == "html":
		ext = "html"
	case in.Format == "xlsx" && in.SourceFormat == "json":
		ext = "json"
		if !json.Valid([]byte(in.Content)) {
			return failure(codeInvalidArguments, "content is not valid JSON"), nil
		}
	case in.Format != "docx" && in.Format != "xlsx":
		return failure(codeUnsupported, "format must be docx or xlsx"), nil
	default:
		return failure(codeUnsupported, fmt.Sprintf("this source_format is not supported for %s (docx: markdown or html; xlsx: json)", in.Format)), nil
	}
	if strings.TrimSpace(in.Content) == "" {
		return failure(codeInvalidArguments, "content is required"), nil
	}
	if len(in.Content) > h.deps.MaxContentBytes {
		return failure(codeFileTooLarge, "content exceeds the size limit"), nil
	}
	c, err := h.begin(ctx)
	if err != nil {
		return fail(err), nil
	}
	defer c.End()
	src := "source." + ext
	out := "output." + in.Format
	if err := writeInput(c.Path(src), []byte(in.Content)); err != nil {
		return fail(&engine.Error{Code: engine.CodeFailed, Message: "cannot write the working file"}), nil
	}
	res, err := c.Run("create", "--type", in.Format, "--from", src, "--out", out)
	if err != nil {
		return fail(err), nil
	}
	data, err := h.readOutput(c, out, in.Format)
	if err != nil {
		return fail(err), nil
	}
	ans, err := parseCreate(res)
	if err != nil {
		return fail(err), nil
	}
	body := map[string]any{"status": "ok"}
	if ans.Sheets != nil {
		body["sheets"] = *ans.Sheets
	}
	if ans.Blocks != nil {
		body["blocks"] = *ans.Blocks
	}
	if err := h.emit(ctx, "office_create_v1", cleanFilename(in.Filename, in.Format, "document"), mimeFor(in.Format), data, body); err != nil {
		return fail(err), nil
	}
	return result(body), nil
}

// --- edit ---

func (h *handlers) edit(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		Format   string           `json:"format"`
		File     fileInput        `json:"file"`
		Ops      []map[string]any `json:"ops"`
		Track    bool             `json:"track"`
		Author   string           `json:"author"`
		Filename string           `json:"filename"`
	}
	if err := decode(request, &in); err != nil {
		return failure(codeInvalidArguments, err.Error()), nil
	}
	in.Format = strings.ToLower(in.Format)
	if in.Format != "docx" && in.Format != "xlsx" {
		return failure(codeUnsupported, "format must be docx or xlsx"), nil
	}
	if in.Track && in.Format != "docx" {
		return failure(codeInvalidArguments, "track is only supported for docx"), nil
	}
	if in.Format == "xlsx" && !h.xlsxAvailable() {
		return failure(codeUnsupported, "xlsx editing is not available in this deployment"), nil
	}
	mode, err := validateOps(in.Format, in.Ops)
	if err != nil {
		return fail(err), nil
	}
	data, err := h.load(ctx, "file", in.File)
	if err != nil {
		return fail(err), nil
	}
	c, err := h.begin(ctx)
	if err != nil {
		return fail(err), nil
	}
	defer c.End()
	inName, outName := "input."+in.Format, "output."+in.Format
	if err := writeInput(c.Path(inName), data); err != nil {
		return fail(&engine.Error{Code: engine.CodeFailed, Message: "cannot write the working file"}), nil
	}
	opsJSON, err := json.Marshal(in.Ops)
	if err != nil || writeInput(c.Path("ops.json"), opsJSON) != nil {
		return fail(&engine.Error{Code: engine.CodeFailed, Message: "cannot write the working file"}), nil
	}
	var res *engine.Result
	switch {
	case in.Format == "docx":
		args := []string{"docs", "apply", inName, "--ops", "ops.json", "--out", outName}
		if in.Track {
			args = append(args, "--track", "--author", cleanAuthor(in.Author))
		}
		res, err = c.Run(args...)
	case mode == xlsxCells:
		res, err = c.Run("sheet", "apply", inName, "--cells", "ops.json", "--out", outName)
	default:
		res, err = c.Run("sheet", "apply", inName, "--ops", "ops.json", "--out", outName)
	}
	if err != nil {
		return fail(err), nil
	}
	applied, err := appliedOps(res, in.Format == "xlsx" && mode == xlsxCells, len(in.Ops))
	if err != nil {
		return fail(err), nil
	}
	out, err := h.readOutput(c, outName, in.Format)
	if err != nil {
		return fail(err), nil
	}
	if in.Track && applied != 0 {
		if err := checkTrackedEdit(data, out, in.Ops); err != nil {
			return fail(err), nil
		}
	}
	body := map[string]any{"status": "ok", "applied": applied}
	if err := h.emit(ctx, "office_edit_v1", cleanFilename(in.Filename, in.Format, "document"), mimeFor(in.Format), out, body); err != nil {
		return fail(err), nil
	}
	return result(body), nil
}

// --- compare ---

func (h *handlers) compare(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		Format   string    `json:"format"`
		Before   fileInput `json:"before"`
		After    fileInput `json:"after"`
		Author   string    `json:"author"`
		Filename string    `json:"filename"`
	}
	if err := decode(request, &in); err != nil {
		return failure(codeInvalidArguments, err.Error()), nil
	}
	if f := strings.ToLower(in.Format); f != "" && f != "docx" {
		return failure(codeUnsupported, "compare supports docx only"), nil
	}
	before, err := h.load(ctx, "before", in.Before)
	if err != nil {
		return fail(err), nil
	}
	after, err := h.load(ctx, "after", in.After)
	if err != nil {
		return fail(err), nil
	}
	c, err := h.begin(ctx)
	if err != nil {
		return fail(err), nil
	}
	defer c.End()
	if writeInput(c.Path("before.docx"), before) != nil || writeInput(c.Path("after.docx"), after) != nil {
		return fail(&engine.Error{Code: engine.CodeFailed, Message: "cannot write the working file"}), nil
	}
	res, err := c.Run("docs", "compare", "before.docx", "after.docx", "--out", "output.docx", "--author", cleanAuthor(in.Author))
	if err != nil {
		return fail(err), nil
	}
	out, err := h.readOutput(c, "output.docx", "docx")
	if err != nil {
		return fail(err), nil
	}
	ans, err := parseCompare(res)
	if err != nil {
		return fail(err), nil
	}
	rev, err := trackedCounts(out)
	if err != nil {
		return fail(invalidOutput("the produced file has no readable body")), nil
	}
	if (*ans.Insertions > 0) != (rev.Ins > 0) || (*ans.Deletions > 0) != (rev.Del > 0) {
		return fail(invalidOutput("the produced file does not match the announced insertions and deletions")), nil
	}
	changes := map[string]any{"insertions": *ans.Insertions, "deletions": *ans.Deletions}
	if ans.ChangedParagraphs != nil {
		changes["changed_paragraphs"] = *ans.ChangedParagraphs
	}
	body := map[string]any{"status": "ok", "changes": changes}
	if err := h.emit(ctx, "office_compare_v1", cleanFilename(in.Filename, "docx", "comparison"), mimeDocx, out, body); err != nil {
		return fail(err), nil
	}
	return result(body), nil
}

// --- inspect ---

func (h *handlers) inspect(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		Format string    `json:"format"`
		File   fileInput `json:"file"`
	}
	if err := decode(request, &in); err != nil {
		return failure(codeInvalidArguments, err.Error()), nil
	}
	in.Format = strings.ToLower(in.Format)
	if in.Format != "docx" && in.Format != "xlsx" {
		return failure(codeUnsupported, "format must be docx or xlsx"), nil
	}
	if in.Format == "xlsx" && !h.xlsxAvailable() {
		return failure(codeUnsupported, "xlsx inspection is not available in this deployment"), nil
	}
	data, err := h.load(ctx, "file", in.File)
	if err != nil {
		return fail(err), nil
	}
	c, err := h.begin(ctx)
	if err != nil {
		return fail(err), nil
	}
	defer c.End()
	name := "input." + in.Format
	if err := writeInput(c.Path(name), data); err != nil {
		return fail(&engine.Error{Code: engine.CodeFailed, Message: "cannot write the working file"}), nil
	}
	var body map[string]any
	if in.Format == "docx" {
		body, err = inspectDocx(c, name)
	} else {
		body, err = inspectXlsx(c, name)
	}
	if err != nil {
		return fail(err), nil
	}
	body["status"] = "ok"
	return result(body), nil
}

func truncateText(s string) (string, bool) {
	if len(s) <= maxInspectText {
		return s, false
	}
	cut := maxInspectText
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func inspectDocx(c *engine.Call, name string) (map[string]any, error) {
	info, err := c.Run("info", name)
	if err != nil {
		return nil, err
	}
	d, err := parseDocInfo(info)
	if err != nil {
		return nil, err
	}
	headings := make([]map[string]any, 0, len(d.Headings))
	for _, h := range d.Headings {
		headings = append(headings, map[string]any{"level": h.Level, "text": h.Text})
	}
	structure := map[string]any{"format": "docx", "blocks": *d.Blocks, "block_types": d.BlockTypes, "headings": headings}
	for k, v := range map[string]*int{"comments": d.Comments, "footnotes": d.Footnotes, "endnotes": d.Endnotes} {
		if v != nil {
			structure[k] = *v
		}
	}
	if d.Protected != nil {
		structure["protected"] = *d.Protected
	}
	if _, err := c.Run("convert", name, "--to", "md", "--out", "text.md"); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(c.Path("text.md"))
	if err != nil {
		return nil, invalidOutput("the text was not produced")
	}
	text, truncated := truncateText(string(raw))
	check, err := c.Run("docs", "check", name)
	if err != nil {
		return nil, err
	}
	issues, err := parseCheck(check)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"text": text, "structure": structure, "issues": issues}
	if truncated {
		body["text_truncated"] = true
	}
	return body, nil
}

func inspectXlsx(c *engine.Call, name string) (map[string]any, error) {
	info, err := c.Run("info", name)
	if err != nil {
		return nil, err
	}
	d, err := parseWorkbookInfo(info)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	sheets := make([]map[string]any, 0, len(*d.Sheets))
	for i, s := range *d.Sheets {
		entry := map[string]any{"name": *s.Name}
		if s.Rows != nil {
			entry["rows"] = *s.Rows
		}
		if s.Columns != nil {
			entry["columns"] = *s.Columns
		}
		if i >= maxInspectCols {
			entry["read"] = false
			sheets = append(sheets, entry)
			continue
		}
		res, err := c.Run("sheet", "read", name, "--sheet", *s.Name)
		if err != nil {
			return nil, err
		}
		sd, err := parseSheetRead(res)
		if err != nil {
			return nil, err
		}
		if sd.Range != nil {
			entry["range"] = *sd.Range
		}
		entry["formulas"] = len(sd.Formulas)
		entry["truncated"] = sd.Truncated != nil && *sd.Truncated
		sheets = append(sheets, entry)
		fmt.Fprintf(&text, "## %s\n", *s.Name)
		for _, row := range *sd.Rows {
			for j, cell := range row {
				if j > 0 {
					text.WriteByte('\t')
				}
				if cell != nil {
					fmt.Fprint(&text, cell)
				}
			}
			text.WriteByte('\n')
		}
	}
	check, err := c.Run("sheet", "check", name)
	if err != nil {
		return nil, err
	}
	issues, err := parseCheck(check)
	if err != nil {
		return nil, err
	}
	out, truncated := truncateText(text.String())
	structure := map[string]any{"format": "xlsx", "sheets": sheets}
	if d.DefinedNames != nil {
		structure["defined_names"] = *d.DefinedNames
	}
	body := map[string]any{"text": out, "structure": structure, "issues": issues}
	if truncated {
		body["text_truncated"] = true
	}
	return body, nil
}

// xlsxAvailable tells whether the workbook helper is configured.
func (h *handlers) xlsxAvailable() bool { return h.xlsxOK }

// sidecarUsable checks that the helper exists, is a regular file and is executable.
func sidecarUsable(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// appliedOps reports what the engine says it applied, never what was asked.
// A batch that is not fully applied (failed, skipped, or total/applied different from the number requested) is refused as op_rejected (the
// produced file is not returned); a batch without an engine report (no batch,
// or no cells count for cell entries) is invalid_output.
func appliedOps(res *engine.Result, cells bool, requested int) (any, error) {
	a, err := parseApply(res)
	if err != nil {
		return nil, err
	}
	var applied, failed, skipped int
	hasApplied := false
	if b := a.Batch; b != nil {
		if b.Applied != nil {
			applied, hasApplied = *b.Applied, true
		}
		if b.Failed != nil {
			failed = *b.Failed
		}
		if b.Skipped != nil {
			skipped = *b.Skipped
		}
		partial := failed > 0 || skipped > 0 ||
			(b.Total != nil && *b.Total != requested) || (hasApplied && applied != requested)
		if partial {
			details := map[string]any{"requested": requested, "applied": applied, "failed": failed, "skipped": skipped}
			if b.Total != nil {
				details["total"] = *b.Total
			}
			return nil, &engine.Error{Code: engine.CodeOpRejected, Message: "the engine did not apply every requested operation; nothing is returned", Details: details}
		}
	}
	if !cells {
		if !hasApplied {
			return nil, invalidOutput("the engine reported no batch result")
		}
		return applied, nil
	}
	if cells {
		if a.Cells == nil {
			return nil, invalidOutput("the written cell count is missing")
		}
		if *a.Cells != requested {
			return nil, &engine.Error{Code: engine.CodeOpRejected, Message: "the engine did not write every requested cell; nothing is returned",
				Details: map[string]any{"requested": requested, "cells": *a.Cells}}
		}
		return *a.Cells, nil
	}
	return nil, invalidOutput("the engine reported no result")
}

// revisionOps are the operations that accept or reject tracked changes (they
// remove revisions); every other docx operation is an edit.
var revisionOps = map[string]bool{"accept_changes": true, "reject_changes": true}

// checkTrackedEdit verifies a track=true edit against the produced file:
//   - no accept/reject op: the revision total (w:ins, w:del, format changes and moves) must increase;
//   - accept/reject ops mixed with edits: the output must hold a revision;
//   - only accept/reject ops: no revision is required.
func checkTrackedEdit(input, output []byte, ops []map[string]any) error {
	edits, revs := 0, 0
	for _, op := range ops {
		if name, _ := op["op"].(string); revisionOps[name] {
			revs++
		} else {
			edits++
		}
	}
	if edits == 0 {
		return nil
	}
	after, err := trackedCounts(output)
	if err != nil {
		return invalidOutput("the produced file has no readable body")
	}
	if revs > 0 {
		if after.Total() == 0 {
			return invalidOutput("track was requested but the produced file has no tracked changes")
		}
		return nil
	}
	before, err := trackedCounts(input)
	if err != nil || after.Total() <= before.Total() {
		return invalidOutput("track was requested but the tracked changes did not increase")
	}
	return nil
}

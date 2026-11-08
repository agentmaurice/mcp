package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const maxOps = 200

// docxOps is the allowlist of document edit operations. Operations that fetch
// or embed external content (pictures, watermarks with an image, package part
// removal, content controls) are not offered.
var docxOps = []string{
	"setFont", "setMatchedFont", "setParagraphFormat", "setHeadingLevel", "findReplace",
	"deleteBlocks", "moveBlocks", "setList", "clearList", "setImageProperties", "insertToc",
	"insertField", "insertBookmark", "updateFields", "insertTableRow", "deleteTableRow",
	"insertTableColumn", "deleteTableColumn", "mergeTableCells", "splitTableCell",
	"setTableCellFormat", "setTableStyle", "applyStyle",
	"insert_content", "replace_blocks", "insert_text_box", "insert_chart", "edit_chart",
	"set_header_footer", "set_page_setup", "insert_section_break", "define_style",
	"add_comment", "reply_comment", "resolve_comment", "delete_comment",
	"accept_changes", "reject_changes", "insert_footnote", "insert_endnote", "edit_note", "delete_note",
	"set_properties",
}

// xlsxOps are the workbook operations (cell entries `{cell, value|formula}` are
// accepted too).
var xlsxOps = []string{
	"set_cell", "set_formula", "set_range", "clear_cell", "clear_range", "fill_range", "copy_range",
	"move_range", "find_replace", "sort_range", "convert_to_values", "format_range",
	"add_conditional_format", "clear_conditional_formats", "set_data_validation", "merge_cells",
	"unmerge_cells", "set_row_height", "set_col_width", "set_rows_hidden", "set_cols_hidden",
	"set_freeze", "set_filter", "set_filter_criteria", "clear_filter", "set_page_setup",
	"protect_sheet", "set_hyperlink", "set_note", "add_defined_name", "delete_defined_name",
	"add_table", "add_pivot", "add_chart", "edit_chart", "add_shape", "add_sparkline",
	"insert_rows", "delete_rows", "insert_cols", "delete_cols", "add_sheet", "delete_sheet",
	"rename_sheet", "duplicate_sheet", "move_sheet", "set_sheet_hidden",
}

func opSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

var (
	docxOpSet = opSet(docxOps)
	xlsxOpSet = opSet(xlsxOps)
	// embedded markup that could make the engine fetch a resource
	externalMarkup = regexp.MustCompile(`(?i)<\s*(img|video|audio|iframe|object|embed|link|script|source)\b`)
)

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// xlsxMode tells how a workbook edit is passed to the engine.
type xlsxMode int

const (
	xlsxCells xlsxMode = iota // `{cell, value|formula}` entries
	xlsxDSL                   // `{op, ...}` entries
)

// validateOps checks the batch and returns, for workbooks, the entry style.
func validateOps(format string, ops []map[string]any) (xlsxMode, error) {
	if len(ops) == 0 {
		return 0, terr(codeInvalidArguments, "ops must contain at least one operation")
	}
	if len(ops) > maxOps {
		return 0, terr(codeInvalidArguments, fmt.Sprintf("ops is limited to %d operations", maxOps))
	}
	mode := xlsxDSL
	for i, op := range ops {
		name, _ := op["op"].(string)
		switch format {
		case "docx":
			if name == "" {
				return 0, terr(codeInvalidArguments, fmt.Sprintf("missing op name at index %d", i))
			}
			if !docxOpSet[name] {
				return 0, terr(codeInvalidArguments, fmt.Sprintf("unknown or unsupported op at index %d", i))
			}
		case "xlsx":
			isCell := name == "" && op["cell"] != nil
			if i == 0 {
				mode = xlsxDSL
				if isCell {
					mode = xlsxCells
				}
			}
			if (mode == xlsxCells) != isCell {
				return 0, terr(codeInvalidArguments, fmt.Sprintf("ops[%d]: do not mix {cell, value|formula} entries with {op} entries", i))
			}
			if !isCell && !xlsxOpSet[name] {
				return 0, terr(codeInvalidArguments, fmt.Sprintf("unknown or unsupported op at index %d", i))
			}
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(op); err != nil {
			return 0, terr(codeInvalidArguments, fmt.Sprintf("ops[%d] is not valid JSON", i))
		}
		raw := buf.Bytes()
		if externalMarkup.Match(raw) || strings.Contains(strings.ToLower(string(raw)), "file://") {
			return 0, terr(codeInvalidArguments, fmt.Sprintf("ops[%d] references external content, which is not supported", i))
		}
	}
	return mode, nil
}

package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/office/internal/engine"
)

const integrationQuote = `# Quote DV-2026-1106

## Terms

Validity 30 days. Payment at 30 days.

| Ref | Item | Qty | Total |
|---|---:|---:|---:|
| L01 | Audit | 2 | 900 |
| L02 | Scoping | 3 | 1 800 |
`

// TestIntegrationRealEngine calls the real CLI. It runs only when
// OFFICE_CLI_PATH is set (and XLSX_SIDECAR_PATH for workbooks); when
// OFFICE_TEST_OUT is set, the produced files are written there for external
// checks (LibreOffice opening, tracked changes read back).
func TestIntegrationRealEngine(t *testing.T) {
	cli := os.Getenv("OFFICE_CLI_PATH")
	if cli == "" {
		t.Skip("OFFICE_CLI_PATH is not set")
	}
	out := os.Getenv("OFFICE_TEST_OUT")
	save := func(name string, content map[string]any) {
		if out == "" {
			return
		}
		if err := os.WriteFile(filepath.Join(out, name), decodeFile(t, content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := engine.New(cli, os.Getenv("XLSX_SIDECAR_PATH"), 90*time.Second, 2)
	deps := Deps{Engine: r, MaxInputBytes: 20 << 20}

	caps := call(t, deps, "office_capabilities_v1", nil).Content
	if caps["engine"].(map[string]any)["available"] != true {
		t.Fatalf("engine unavailable: %v", caps)
	}

	docx := mustOK(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": integrationQuote, "filename": "quote"}))
	save("quote.docx", docx)
	docxFile := map[string]any{"content_base64": docx["file"].(map[string]any)["content_base64"]}

	insp := mustOK(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "docx", "file": docxFile}))
	if !strings.Contains(insp["text"].(string), "Validity 30 days") || !strings.Contains(insp["text"].(string), "Scoping") {
		t.Fatalf("inspect text: %v", insp["text"])
	}
	if insp["structure"].(map[string]any)["format"] != "docx" {
		t.Fatalf("structure: %v", insp["structure"])
	}

	ops := []any{
		map[string]any{"op": "findReplace", "find": "Validity 30 days", "replace": "Validity 45 days"},
		map[string]any{"op": "findReplace", "find": "Payment at 30 days", "replace": "Payment at 45 days"},
	}
	edited := mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": docxFile, "ops": ops, "filename": "quote-v2"}))
	if edited["applied"].(float64) != 2 {
		t.Fatalf("applied: %v", edited["applied"])
	}
	save("quote-v2.docx", edited)
	editedFile := map[string]any{"content_base64": edited["file"].(map[string]any)["content_base64"]}
	check := mustOK(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "docx", "file": editedFile}))
	if !strings.Contains(check["text"].(string), "Validity 45 days") || strings.Contains(check["text"].(string), "30 days") {
		t.Fatalf("edit not applied: %v", check["text"])
	}

	tracked := mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": docxFile, "ops": ops[:1], "track": true, "author": "Reviewer", "filename": "quote-tracked"}))
	save("quote-tracked.docx", tracked)

	// a tracked format-only edit: the engine must record an rPrChange
	fmtEdit := mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": docxFile, "track": true, "author": "Reviewer",
		"ops": []any{map[string]any{"op": "setFont", "target": map[string]any{"blockIndexes": []int{0}}, "bold": true}}}))
	rc, err := trackedCounts(decodeFile(t, fmtEdit))
	if err != nil || rc.Other == 0 {
		t.Fatalf("tracked setFont produced no format revision: %+v %v", rc, err)
	}
	t.Logf("tracked setFont revisions: %+v", rc)

	cmp := mustOK(t, call(t, deps, "office_compare_v1", map[string]any{"format": "docx", "before": docxFile, "after": editedFile, "author": "Reviewer", "filename": "redline"}))
	ch := cmp["changes"].(map[string]any)
	if ch["insertions"].(float64) < 2 || ch["deletions"].(float64) < 2 {
		t.Fatalf("changes: %v", ch)
	}
	save("redline.docx", cmp)

	// a rejected batch is atomic and reported with a stable code
	bad := call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": docxFile, "ops": []any{map[string]any{"op": "findReplace", "find": "text that is absent", "replace": "x"}}})
	c := mustFail(t, bad, "op_rejected")
	if strings.Contains(strings.ToLower(c["message"].(string)), "genoffice") {
		t.Fatalf("message names the engine: %v", c)
	}
	// not a document
	mustFail(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "docx", "file": map[string]any{"content_base64": "UEsDBGp1bmsgbm90IGEgemlw"}}), "invalid_document")

	if os.Getenv("XLSX_SIDECAR_PATH") == "" {
		t.Log("XLSX_SIDECAR_PATH not set: workbook checks skipped")
		return
	}
	xlsx := mustOK(t, call(t, deps, "office_create_v1", map[string]any{"format": "xlsx", "source_format": "json",
		"content": `{"sheets":[{"name":"Quote","rows":[["Ref","Qty","Price","Total"],["L01",2,450,"=B2*C2"],["L02",3,600,"=B3*C3"],["","","Sum","=SUM(D2:D3)"]]}]}`, "filename": "quote"}))
	save("quote.xlsx", xlsx)
	xFile := map[string]any{"content_base64": xlsx["file"].(map[string]any)["content_base64"]}
	xi := mustOK(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "xlsx", "file": xFile}))
	if !strings.Contains(xi["text"].(string), "L02") || !strings.Contains(xi["text"].(string), "2700") {
		t.Fatalf("xlsx text: %v", xi["text"])
	}
	cells := mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": xFile, "filename": "quote-cells",
		"ops": []any{map[string]any{"cell": "A5", "value": "note"}, map[string]any{"cell": "D5", "formula": "=D4*2"}}}))
	save("quote-cells.xlsx", cells)
	cf := map[string]any{"content_base64": cells["file"].(map[string]any)["content_base64"]}
	ci := mustOK(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "xlsx", "file": cf}))
	if !strings.Contains(ci["text"].(string), "note") || !strings.Contains(ci["text"].(string), "5400") {
		t.Fatalf("xlsx cell edit not applied: %v", ci["text"])
	}
	dsl := mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": xFile, "filename": "quote-ops",
		"ops": []any{map[string]any{"op": "set_cell", "address": "B2", "value": 10}}}))
	if a := dsl["applied"]; a != float64(1) {
		t.Fatalf("op edit applied: %v", a)
	}
	t.Logf("xlsx op edit applied=%v, cell edit applied=%v", dsl["applied"], cells["applied"])
	if a := cells["applied"]; a != float64(2) {
		t.Fatalf("cell edit applied: %v", a)
	}
	save("quote-ops.xlsx", dsl)
	di := mustOK(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "xlsx", "file": map[string]any{"content_base64": dsl["file"].(map[string]any)["content_base64"]}}))
	if !strings.Contains(di["text"].(string), "10\t450\t4500") {
		t.Fatalf("xlsx op edit not applied: %v", di["text"])
	}
}

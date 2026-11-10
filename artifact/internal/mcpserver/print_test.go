package mcpserver

import (
	"encoding/base64"
	"math"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template/templatetest"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst/typsttest"
)

func TestRenderPrintThroughTheTool(t *testing.T) {
	deps := Deps{Compiler: typsttest.Runner(t)}
	args := goldenArgs(t, "print-brochure", "", "pdf", "preview")
	args["print"] = map[string]any{"bleed_mm": 3, "mirror_margins": true, "crop_marks": true}
	a := call(t, deps, "artifact_render_v1", args)
	b := call(t, deps, "artifact_render_v1", args)
	if a.IsError || b.IsError {
		t.Fatalf("error: %v %v", a.Content, b.Content)
	}
	file := a.Content["file"].(map[string]any)
	if file["sha256"] != b.Content["file"].(map[string]any)["sha256"] {
		t.Fatal("two print renders differ")
	}
	pdf, err := base64.StdEncoding.DecodeString(file["content_base64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	pages := templatetest.PDFPageBoxes(t, pdf)
	if len(pages) != 3 {
		t.Fatalf("pages: %d", len(pages))
	}
	pad := 10 * 72 / 25.4
	for _, p := range pages {
		if len(p.Trim) != 4 || len(p.Bleed) != 4 || math.Abs(p.Trim[0]-pad) > 0.01 || math.Abs(p.Bleed[0]-7*72/25.4) > 0.01 {
			t.Fatalf("boxes: %+v", p)
		}
	}
	prov := a.Content["provenance"].(map[string]any)["print"].(map[string]any)
	if prov["bleed_mm"] != float64(3) || prov["mirror_margins"] != true || prov["crop_marks"] != true {
		t.Fatalf("provenance print: %v", prov)
	}
}

func TestRenderPrintErrorsAreStable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		print any
		code  string
		tpl   string
		out   string
	}{
		{"over", map[string]any{"bleed_mm": 5.5}, template.CodePrintInvalid, "print-brochure", "pdf"},
		{"negative", map[string]any{"bleed_mm": -1}, template.CodePrintInvalid, "print-brochure", "pdf"},
		{"type", map[string]any{"bleed_mm": "3"}, template.CodePrintInvalid, "print-brochure", "pdf"},
		{"unknown-field", map[string]any{"trim_mm": 3}, template.CodePrintInvalid, "print-brochure", "pdf"},
		{"not-an-object", "yes", template.CodePrintInvalid, "print-brochure", "pdf"},
		{"template-without-fragment", map[string]any{}, template.CodePrintUnsupported, "quote", "pdf"},
		{"png", map[string]any{}, template.CodePrintUnsupported, "print-brochure", "png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := goldenArgs(t, tc.tpl, "", tc.out, "preview")
			args["print"] = tc.print
			if tc.tpl == "print-brochure" && tc.out == "png" {
				args["template"].(map[string]any)["manifest"].(map[string]any)["outputs"] = []any{"pdf", "png"}
			}
			r := call(t, Deps{}, "artifact_render_v1", args) // refused before any engine is needed
			if !r.IsError || r.Content["error"] != tc.code {
				t.Fatalf("want %s: %v", tc.code, r.Content)
			}
		})
	}
}

func TestCapabilitiesPublishTheClosedPrintSchema(t *testing.T) {
	r := call(t, Deps{}, "artifact_capabilities_v1", nil)
	pr, ok := r.Content["print"].(map[string]any)
	if !ok {
		t.Fatalf("no print entry: %v", r.Content)
	}
	sch := pr["schema"].(map[string]any)
	props := sch["properties"].(map[string]any)
	bleed := props["bleed_mm"].(map[string]any)
	if ty, _ := sch["type"].([]any); len(ty) != 2 || ty[0] != "object" || ty[1] != "null" {
		t.Fatalf("print type must be object or null: %v", sch["type"])
	}
	if sch["additionalProperties"] != false || bleed["minimum"] != float64(0) || bleed["maximum"] != float64(5) || bleed["default"] != float64(3) ||
		props["mirror_margins"].(map[string]any)["type"] != "boolean" || props["crop_marks"].(map[string]any)["type"] != "boolean" || len(props) != 3 {
		t.Fatalf("print schema: %v", sch)
	}
	if pr["fragment"] != "fragments/print.typ" || pr["mark_zone_mm"] != float64(7) {
		t.Fatalf("print capabilities: %v", pr)
	}
	declared := map[string]bool{}
	for _, c := range r.Content["error_codes"].([]any) {
		declared[c.(string)] = true
	}
	for _, c := range []string{template.CodePrintInvalid, template.CodePrintUnsupported, template.CodePrintBoxFailed} {
		if !declared[c] {
			t.Fatalf("%s is not in error_codes", c)
		}
	}
}

// print: null is no option: same bytes as a call without print.
func TestPrintNullEqualsNoOption(t *testing.T) {
	deps := Deps{Compiler: typsttest.Runner(t)}
	without := call(t, deps, "artifact_render_v1", goldenArgs(t, "print-brochure", "", "pdf", "preview"))
	args := goldenArgs(t, "print-brochure", "", "pdf", "preview")
	args["print"] = nil
	withNull := call(t, deps, "artifact_render_v1", args)
	if without.IsError || withNull.IsError {
		t.Fatalf("errors: %v %v", without.Content, withNull.Content)
	}
	a, b := without.Content["file"].(map[string]any), withNull.Content["file"].(map[string]any)
	if a["sha256"] != b["sha256"] || a["content_base64"] != b["content_base64"] {
		t.Fatal("print: null must give the same bytes as no print")
	}
	if _, ok := withNull.Content["provenance"].(map[string]any)["print"]; ok {
		t.Fatal("print: null must not appear in the provenance")
	}
}

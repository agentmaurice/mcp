package template_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template/templatetest"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst/typsttest"
)

const mmPt = 72.0 / 25.4

// A4 in points as Typst writes it (5 decimals).
var (
	a4W = 210 * mmPt
	a4H = 297 * mmPt
)

func renderPrint(t *testing.T, c template.Compiler, tpl template.Template, data any, out string, mode string, po *template.PrintOptions) (*template.Result, error) {
	t.Helper()
	ctx, assets := templatetest.Context(t)
	return template.Render(context.Background(), template.Request{
		Template: tpl, Data: data, Context: ctx, Assets: assets, Output: out, Mode: mode, Print: po,
	}, c, template.Options{Timeout: 120 * time.Second})
}

func mustPrint(t *testing.T, c template.Compiler, name string, po *template.PrintOptions) *template.Result {
	t.Helper()
	tpl := templatetest.Load(t, "valid/"+name)
	res, err := renderPrint(t, c, tpl, templatetest.Data(t, tpl, ""), "pdf", template.ModePreview, po)
	if err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return res
}

func near(t *testing.T, what string, got []float64, want ...float64) {
	t.Helper()
	if len(got) != 4 {
		t.Fatalf("%s: missing or malformed box %v", what, got)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 0.01 { // 0.01 pt: Typst writes five decimals
			t.Fatalf("%s: got %v, want %v (index %d)", what, got, want, i)
		}
	}
}

func code(err error) string {
	var te *template.Error
	if errors.As(err, &te) {
		return te.Code
	}
	return ""
}

// The boxes are read back from the produced file, page by page, through the xref chain
// of the incremental update.
func TestPrintBoxesInTheProducedPDF(t *testing.T) {
	runner := typsttest.Runner(t)
	for _, tc := range []struct {
		name         string
		bleed        float64
		mirror, crop bool
	}{
		{"bleed3-crop", 3, true, true}, {"bleed3", 3, true, false}, {"bleed0-crop", 0, false, true},
		{"bleed5", 5, false, false}, {"bleed2.5-crop", 2.5, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := mustPrint(t, runner, "print-brochure", &template.PrintOptions{BleedMM: tc.bleed, MirrorMargins: tc.mirror, CropMarks: tc.crop})
			slug := 0.0
			if tc.crop {
				slug = template.PrintMarkZoneMM
			}
			pad := (tc.bleed + slug) * mmPt
			sl := slug * mmPt
			pages := templatetest.PDFPageBoxes(t, res.Content)
			if len(pages) != 3 || res.Pages != 3 {
				t.Fatalf("want 3 pages, got %d (result %d)", len(pages), res.Pages)
			}
			for i, p := range pages {
				w, h := a4W+2*pad, a4H+2*pad
				near(t, "MediaBox", p.Media, 0, 0, w, h)
				near(t, "BleedBox", p.Bleed, sl, sl, w-sl, h-sl)
				near(t, "TrimBox", p.Trim, pad, pad, w-pad, h-pad)
				// TrimBox is the finished A4 size, whatever the bleed and the marks
				if tw, th := p.Trim[2]-p.Trim[0], p.Trim[3]-p.Trim[1]; math.Abs(tw-a4W) > 0.01 || math.Abs(th-a4H) > 0.01 {
					t.Fatalf("page %d: TrimBox is %.3f x %.3f pt, want A4 %.3f x %.3f", i+1, tw, th, a4W, a4H)
				}
				// BleedBox is the TrimBox grown by the bleed
				if bw := p.Bleed[2] - p.Bleed[0]; math.Abs(bw-(a4W+2*tc.bleed*mmPt)) > 0.01 {
					t.Fatalf("page %d: BleedBox width %.3f", i+1, bw)
				}
			}
			if res.Provenance.Print == nil || *res.Provenance.Print != (template.PrintOptions{BleedMM: tc.bleed, MirrorMargins: tc.mirror, CropMarks: tc.crop}) {
				t.Fatalf("provenance print: %+v", res.Provenance.Print)
			}
		})
	}
}

func TestPrintRendersAreIdenticalToTheByte(t *testing.T) {
	runner := typsttest.Runner(t)
	po := &template.PrintOptions{BleedMM: 3, MirrorMargins: true, CropMarks: true}
	a := mustPrint(t, runner, "print-brochure", po)
	b := mustPrint(t, runner, "print-brochure", po)
	if !bytes.Equal(a.Content, b.Content) {
		t.Fatalf("PDF not reproducible: %d vs %d bytes", len(a.Content), len(b.Content))
	}
	// the original Typst bytes are kept: the file is one incremental update of them
	if !bytes.HasPrefix(a.Content, []byte("%PDF-")) || !bytes.HasSuffix(a.Content, []byte("%%EOF\n")) || bytes.Count(a.Content, []byte("startxref")) != 2 {
		t.Fatal("the PDF is not a single incremental update of the Typst output")
	}
}

// printProbe is a one-line template of the print layout with a black bar of 6 mm in the text
// flow on three pages, no background: the bar shows the left and right margins of every page.
func printProbe() template.Template {
	files := map[string]string{
		"main.typ": `#import "/fragments/print.typ": print-layout
#show: print-layout.with(paper: "a4", margin: (top: 20mm, bottom: 20mm, inside: 20mm, outside: 12mm))
#rect(width: 100%, height: 6mm, fill: black)
#pagebreak()
#rect(width: 100%, height: 6mm, fill: black)
#pagebreak()
#rect(width: 100%, height: 6mm, fill: black)
`,
		"input.schema.json":     `{"type":"object"}`,
		"examples/default.json": `{}`,
	}
	manifest := `{"schema":"agentmaurice.template/v1","key":"print-probe","version":"1.0.0","title":"Print probe","description":"Print layout probe.",
"kind":"document","profile":"document","source":{"engine":"typst","entry":"main.typ"},"outputs":["pdf","png"],"locales":["fr"],"default_locale":"fr",
"input_schema":"input.schema.json","examples":[{"name":"default","data":"examples/default.json"}],"fragments":["fragments/print.typ"],"limits":{"max_source_bytes":20000,"max_pages":6}}`
	return template.Template{Manifest: json.RawMessage(manifest), Files: files}
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func dark(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return (r+g+b)/3 < 0xF000 // anything but white (anti-aliased marks count)
}

// Left and right edge (px) of the black bar, searched on the row at the middle of the bar.
func barEdges(t *testing.T, img image.Image, y int) (int, int) {
	t.Helper()
	left, right := -1, -1
	for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
		if (func() bool { r, g, b, _ := img.At(x, y).RGBA(); return r+g+b < 0x3000 })() {
			if left < 0 {
				left = x
			}
			right = x + 1
		}
	}
	if left < 0 {
		t.Fatalf("no black bar on row %d", y)
	}
	return left, right
}

func TestPrintMarginsAlternateWithPageParity(t *testing.T) {
	runner := typsttest.Runner(t)
	const ppi = 144.0 // final mode: 1 px = 0.5 pt
	px := ppi / 72.0
	for _, mirror := range []bool{true, false} {
		res, err := renderPrint(t, runner, printProbe(), map[string]any{}, "pdf", template.ModeFinal, &template.PrintOptions{BleedMM: 3, MirrorMargins: mirror})
		if err != nil {
			t.Fatal(err)
		}
		if res.Pages != 3 || len(res.Previews) != 3 {
			t.Fatalf("pages=%d previews=%d", res.Pages, len(res.Previews))
		}
		pad := 3.0 // bleed, no slug without crop marks
		inside, outside := 20.0, 12.0
		for i, pv := range res.Previews {
			img := decode(t, pv.PNG)
			// row in the middle of the bar: top margin + pad + 3 mm
			y := int((pad + 20 + 3) * mmPt * px)
			left, right := barEdges(t, img, y)
			odd := i%2 == 0 // page 1 is a recto
			wantL, wantR := pad+inside, pad+outside
			if mirror && !odd { // verso: inside is on the right
				wantL, wantR = pad+outside, pad+inside
			}
			pageW := img.Bounds().Dx()
			gotL, gotR := float64(left)/px/mmPt, float64(pageW-right)/px/mmPt
			if math.Abs(gotL-wantL) > 0.3 || math.Abs(gotR-wantR) > 0.3 {
				t.Fatalf("mirror=%v page %d: margins left %.2f mm right %.2f mm, want %.2f and %.2f", mirror, i+1, gotL, gotR, wantL, wantR)
			}
		}
	}
}

func TestPrintCropMarksAreOutsideTheFinishedArea(t *testing.T) {
	runner := typsttest.Runner(t)
	const px = 2.0 // final mode 144 ppi
	for _, crop := range []bool{true, false} {
		res, err := renderPrint(t, runner, printProbe(), map[string]any{}, "pdf", template.ModeFinal, &template.PrintOptions{BleedMM: 3, CropMarks: crop})
		if err != nil {
			t.Fatal(err)
		}
		img := decode(t, res.Previews[0].PNG)
		boxes := templatetest.PDFPageBoxes(t, res.Content)[0]
		count := func(x0, y0, x1, y1 float64) int { // a rectangle in points, top-left origin
			n := 0
			for y := int(y0 * px); y < int(y1*px); y++ {
				for x := int(x0 * px); x < int(x1*px); x++ {
					if dark(img, x, y) {
						n++
					}
				}
			}
			return n
		}
		w, h := boxes.Media[2], boxes.Media[3]
		trim, bleed := boxes.Trim, boxes.Bleed // PDF space has a bottom-left origin; the symmetry makes the top-left reading valid
		markZone, finishedCorners := 0, 0
		for _, c := range [][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
			// the four corner squares of 14 mm (marks and bleed), measured from the page corners
			x0, y0 := c[0]*(w-14*mmPt), c[1]*(h-14*mmPt)
			markZone += count(x0, y0, x0+14*mmPt, y0+14*mmPt)
		}
		// bleed ring: between the BleedBox and the TrimBox, whole perimeter (no background: white)
		ring := count(bleed[0], bleed[1], bleed[2], trim[1]) + count(bleed[0], trim[3], bleed[2], bleed[3]) +
			count(bleed[0], trim[1], trim[0], trim[3]) + count(trim[2], trim[1], bleed[2], trim[3])
		bleedZone := ring
		// 10 mm squares in the four corners of the finished area (the bar is further in)
		for _, c := range [][2]float64{{trim[0], trim[1]}, {trim[2] - 10*mmPt, trim[1]}, {trim[0], trim[3] - 10*mmPt}, {trim[2] - 10*mmPt, trim[3] - 10*mmPt}} {
			finishedCorners += count(c[0], c[1], c[0]+10*mmPt, c[1]+10*mmPt)
		}
		if bleedZone != 0 || finishedCorners != 0 {
			t.Fatalf("crop=%v: marks leak into the bleed (%d) or the finished area (%d)", crop, bleedZone, finishedCorners)
		}
		if crop && markZone == 0 {
			t.Fatal("crop marks requested but none found in the mark zone")
		}
		if !crop && markZone != 0 {
			t.Fatalf("no crop marks requested, found %d dark pixels in the mark zone", markZone)
		}
	}
}

func TestPrintOptionOutOfRangeIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"over", `{"bleed_mm": 5.01}`}, {"negative", `{"bleed_mm": -0.01}`}, {"huge", `{"bleed_mm": 1e9}`},
		{"string", `{"bleed_mm": "3"}`}, {"bool-as-number", `{"mirror_margins": 1}`}, {"unknown", `{"bleed": 3}`},
		{"array", `[]`}, {"scalar", `3`}, {"trailing", `{} {}`}, {"trailing-garbage", `{} garbage`}, {"crop-string", `{"crop_marks": "yes"}`},
		{"upper-bleed", `{"BLEED_MM": 0, "CROP_MARKS": true}`}, {"mixed-case", `{"Bleed_mm": 1}`}, {"upper-crop", `{"CROP_MARKS": true}`},
		{"capital-mirror", `{"Mirror_Margins": true}`}, {"valid-plus-wrong-case", `{"bleed_mm": 1, "Crop_Marks": true}`},
		{"null-bleed", `{"bleed_mm": null}`}, {"null-flags", `{"mirror_margins": null, "crop_marks": null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := template.ParsePrint(json.RawMessage(tc.raw)); code(err) != template.CodePrintInvalid {
				t.Fatalf("want print_invalid, got %v", err)
			}
		})
	}
	for _, raw := range []string{``, `null`, `{}`, `{"bleed_mm":0}`, `{"bleed_mm":5}`, `{"bleed_mm":2.5,"mirror_margins":true,"crop_marks":true}`} {
		if _, err := template.ParsePrint(json.RawMessage(raw)); err != nil {
			t.Fatalf("%q must be accepted: %v", raw, err)
		}
	}
	if po, _ := template.ParsePrint(json.RawMessage(`{}`)); po == nil || po.BleedMM != 3 || po.MirrorMargins || po.CropMarks {
		t.Fatalf("defaults: %+v", po)
	}
	if po, _ := template.ParsePrint(nil); po != nil {
		t.Fatal("no option must give nil")
	}
	// A Go caller bypassing ParsePrint is bounded by Render itself.
	tpl := templatetest.Load(t, "valid/print-brochure")
	for _, bad := range []float64{-1, 5.5, math.NaN(), math.Inf(1)} {
		_, err := renderPrint(t, nil, tpl, templatetest.Data(t, tpl, ""), "pdf", template.ModePreview, &template.PrintOptions{BleedMM: bad})
		if code(err) != template.CodePrintInvalid {
			t.Fatalf("bleed %v: want print_invalid, got %v", bad, err)
		}
	}
}

func TestPrintIsRefusedWhereItCannotApply(t *testing.T) {
	runner := typsttest.Runner(t)
	po := &template.PrintOptions{BleedMM: 3}
	if _, err := renderPrint(t, runner, printProbe(), map[string]any{}, "png", template.ModePreview, po); code(err) != template.CodePrintUnsupported {
		t.Fatalf("png output: want print_unsupported, got %v", err)
	}
	quote := templatetest.Load(t, "valid/quote") // does not declare fragments/print.typ
	if _, err := renderPrint(t, runner, quote, templatetest.Data(t, quote, ""), "pdf", template.ModePreview, po); code(err) != template.CodePrintUnsupported || !strings.Contains(err.Error(), "fragments/print.typ") {
		t.Fatalf("template without the fragment: want print_unsupported, got %v", err)
	}
	mail := templatetest.Load(t, "valid/support-reply")
	if _, err := renderPrint(t, runner, mail, templatetest.Data(t, mail, ""), "html", template.ModePreview, po); code(err) != template.CodePrintUnsupported {
		t.Fatalf("message profile: want print_unsupported, got %v", err)
	}
}

// Without print, nothing changes: the PDFs of the goldens are the bytes of v0.2.8 (SHA-256 taken on that
// code, same pinned Typst), and so are the provenance and /brand.typ (through source_sha256).
func TestWithoutPrintTheOutputIsByteIdenticalToV028(t *testing.T) {
	runner := typsttest.Runner(t)
	for _, tc := range []struct{ name, example, sha string }{
		{"quote", "default", "b54a208c7093e7b9038245fbd9c2bfda4f97e1b862a1ea6a838e7f0f75194332"},
		{"meeting-minutes", "default", "39c8e1e921587dd698251966831413999d97b14379a49bc1ab148e2ad3623a71"},
		{"meeting-minutes", "minimal", "2ab5c153a691304601328b28e7b7db1f01f6c1c3843186765430e873ee158bfd"},
		{"dashboard", "default", "dd81c97cc29d71993f6ec0f127388e9b26a0d92e4e2eb1ac9c4dbf75eec03423"},
		{"qr-badge", "default", "306a3789b41f4bc39dca68202cabaf588c94e8e1046cb5df74d6ab5e93f33210"},
		{"qr-badge", "accents", "dd2d55e527e6184c835ded68b3a704205fb79a8ddf30b2b61bf17e4f2f8bb0d0"},
	} {
		t.Run(tc.name+"/"+tc.example, func(t *testing.T) {
			res := render(t, runner, tc.name, tc.example, "pdf")
			sum := sha256.Sum256(res.Content)
			if got := hex.EncodeToString(sum[:]); got != tc.sha {
				t.Fatalf("PDF differs from v0.2.8: sha256 %s, want %s", got, tc.sha)
			}
			js, _ := json.Marshal(res.Provenance)
			if strings.Contains(string(js), `"print"`) {
				t.Fatalf("provenance without print must not mention it: %s", js)
			}
			if bytes.Contains(res.Content, []byte("/TrimBox")) || bytes.Contains(res.Content, []byte("/BleedBox")) {
				t.Fatal("boxes written without print")
			}
		})
	}
}

// print-layout without the option is an exact no-op: the same bytes as a plain `set page`.
func TestPrintLayoutWithoutOptionIsAnExactNoOp(t *testing.T) {
	runner := typsttest.Runner(t)
	tpl := templatetest.Load(t, "valid/print-brochure")
	data := templatetest.Data(t, tpl, "")
	withLayout, err := renderPrint(t, runner, tpl, data, "pdf", template.ModePreview, nil)
	if err != nil {
		t.Fatal(err)
	}
	main := tpl.Files["main.typ"]
	start := strings.Index(main, "#show: print-layout.with(")
	end := strings.Index(main, "#set text(")
	if start < 0 || end < start {
		t.Fatal("unexpected main.typ")
	}
	plain := main[:start] + `#set page(
  paper: brand.page.paper,
  margin: (top: brand.page.margins.top, bottom: brand.page.margins.bottom, left: brand.page.margins.left, right: brand.page.margins.right),
  background: place(top, rect(width: 100%, height: 16mm, fill: brand.colors.primary, stroke: none)),
)
` + main[end:]
	plain = strings.Replace(plain, `#import "/fragments/print.typ": print-layout`+"\n", "", 1)
	files := map[string]string{}
	for k, v := range tpl.Files {
		files[k] = v
	}
	files["main.typ"] = plain
	ref, err := renderPrint(t, runner, template.Template{Manifest: tpl.Manifest, Files: files}, data, "pdf", template.ModePreview, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(withLayout.Content, ref.Content) {
		t.Fatalf("print-layout without the option is not a no-op: %d vs %d bytes", len(withLayout.Content), len(ref.Content))
	}
	if withLayout.Pages != 3 {
		t.Fatalf("the brochure must have 3 pages, got %d", withLayout.Pages)
	}
}

// A template that declares fragments/print.typ but only sets its own page gets no boxes
// and no success: the plain render shows that the layout was not applied.
func TestPrintIsRefusedWhenTheFragmentIsDeclaredButNotApplied(t *testing.T) {
	runner := typsttest.Runner(t)
	probe := printProbe()
	files := map[string]string{}
	for k, v := range probe.Files {
		files[k] = v
	}
	files["main.typ"] = "#set page(paper: \"a4\")\n#rect(width: 100%, height: 6mm, fill: black)\n"
	notApplied := template.Template{Manifest: probe.Manifest, Files: files}
	for _, po := range []*template.PrintOptions{{BleedMM: 3}, {BleedMM: 3, CropMarks: true}} {
		_, err := renderPrint(t, runner, notApplied, map[string]any{}, "pdf", template.ModePreview, po)
		if code(err) != template.CodePrintUnsupported || !strings.Contains(err.Error(), "the template declares fragments/print.typ but does not apply print-layout") {
			t.Fatalf("%+v: want print_unsupported, got %v", po, err)
		}
	}
	// (bleed 0 without crop marks changes no page size, so nothing can be told apart: the boxes then equal the MediaBox, which is right)
	// the same template renders without the option, and the probe that applies it still passes
	if _, err := renderPrint(t, runner, notApplied, map[string]any{}, "pdf", template.ModePreview, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := renderPrint(t, runner, printProbe(), map[string]any{}, "pdf", template.ModePreview, &template.PrintOptions{BleedMM: 3, CropMarks: true}); err != nil {
		t.Fatal(err)
	}
}

// Mirrored margins alone change no page size: a symmetric layout is a correct result, boxes = MediaBox.
func TestPrintMirrorMarginsAloneIsAccepted(t *testing.T) {
	runner := typsttest.Runner(t)
	po := &template.PrintOptions{BleedMM: 0, MirrorMargins: true}
	brochure := templatetest.Load(t, "valid/print-brochure") // symmetric brand margins
	for name, tpl := range map[string]template.Template{"print-brochure": brochure, "probe": printProbe()} {
		data := map[string]any{}
		if name == "print-brochure" {
			data = templatetest.Data(t, brochure, "").(map[string]any)
		}
		res, err := renderPrint(t, runner, tpl, data, "pdf", template.ModePreview, po)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, p := range templatetest.PDFPageBoxes(t, res.Content) {
			near(t, name+" TrimBox", p.Trim, 0, 0, a4W, a4H)
			near(t, name+" BleedBox", p.Bleed, 0, 0, a4W, a4H)
			near(t, name+" MediaBox", p.Media, 0, 0, a4W, a4H)
		}
	}
}

// The fragment is declared but the entry does not apply print-layout: refused by the static check,
// before any compilation (no engine needed), even without bleed.
func TestPrintIsRefusedStaticallyWhenPrintLayoutIsNotApplied(t *testing.T) {
	probe := printProbe()
	for name, main := range map[string]string{
		"fixed margins": "#set page(paper: \"a4\", margin: 20mm)\n#rect(width: 100%, height: 6mm, fill: black)\n#pagebreak()\n#rect(width: 100%, height: 6mm, fill: black)\n",
		"comment only":  "// #show: print-layout.with(paper: \"a4\")\n#set page(paper: \"a4\")\nx\n",
		"block comment": "/* #show: print-layout */\n#set page(paper: \"a4\")\nx\n",
		"string only":   "#let s = \"#show: print-layout\"\n#set page(paper: \"a4\")\nx\n",
		"import only":   "#import \"/fragments/print.typ\": print-layout\n#set page(paper: \"a4\")\nx\n",
	} {
		files := map[string]string{}
		for k, v := range probe.Files {
			files[k] = v
		}
		files["main.typ"] = main
		for _, po := range []*template.PrintOptions{{BleedMM: 0, MirrorMargins: true}, {BleedMM: 3}} {
			_, err := renderPrint(t, nil, template.Template{Manifest: probe.Manifest, Files: files}, map[string]any{}, "pdf", template.ModePreview, po)
			if code(err) != template.CodePrintUnsupported || !strings.Contains(err.Error(), "the template declares fragments/print.typ but does not apply print-layout") {
				t.Fatalf("%s %+v: want print_unsupported, got %v", name, po, err)
			}
		}
	}
}

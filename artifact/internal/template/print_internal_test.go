package template

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// miniPDF builds a two-page PDF with a classic xref table, the way Typst writes it.
func miniPDF(media string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	objs := []string{
		"<</Type/Pages/Count 2/Kids[2 0 R 3 0 R]>>",
		"<</Type/Page/Resources<<>>/MediaBox[" + media + "]/Parent 1 0 R>>",
		"<</Type/Page/Resources<<>>/MediaBox[" + media + "]/Parent 1 0 R>>",
		"<</Type/Catalog/Pages 1 0 R>>",
	}
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 5\n0000000000 65535 f \n")
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<</Size 5/Root 4 0 R>>\nstartxref\n%d\n%%%%EOF", x)
	return b.Bytes()
}

const a4Box = "0 0 595.2756 841.8898"

func TestAddPrintBoxesGeometryOnAHandMadeFile(t *testing.T) {
	// A4 plus 3 mm bleed and 7 mm marks on each side, in points
	out, err := addPrintBoxes(miniPDF("0 0 651.9685 898.5827"), miniPDF(a4Box), PrintOptions{BleedMM: 3, CropMarks: true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"/MediaBox[0 0 651.9685 898.5827]/BleedBox[19.84252 19.84252 632.12599 878.74019]/TrimBox[28.34646 28.34646 623.62206 870.23626]",
		"/Prev ", "startxref",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s[len(miniPDF("0 0 651.9685 898.5827")):])
		}
	}
	if !bytes.HasPrefix(out, miniPDF("0 0 651.9685 898.5827")) {
		t.Fatal("the original bytes must be kept")
	}
	again, err := addPrintBoxes(miniPDF("0 0 651.9685 898.5827"), miniPDF(a4Box), PrintOptions{BleedMM: 3, CropMarks: true})
	if err != nil || !bytes.Equal(out, again) {
		t.Fatal("not deterministic")
	}
}

func TestAddPrintBoxesRefusesWhatItCannotRead(t *testing.T) {
	good := miniPDF("0 0 651.9685 898.5827")
	boxed, err := addPrintBoxes(good, miniPDF(a4Box), PrintOptions{BleedMM: 3, CropMarks: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string][]byte{
		"empty":           nil,
		"no startxref":    []byte("%PDF-1.7\n"),
		"offset past end": bytes.Replace(good, []byte("startxref\n"), []byte("startxref\n9"), 1),
		"xref stream":     bytes.Replace(good, []byte("\nxref\n"), []byte("\n12 0 obj\n"), 1),
		"already incr.":   boxed,
		"no media box":    bytes.ReplaceAll(good, []byte("/MediaBox"), []byte("/MediaBoY")),
	} {
		_, err := addPrintBoxes(in, miniPDF(a4Box), PrintOptions{BleedMM: 3, CropMarks: true})
		if err == nil || err.(*Error).Code != CodePrintBoxFailed {
			t.Fatalf("%s: want print_box_failed, got %v", name, err)
		}
	}
}

// The layout is proved by the plain render: same page count, every page grown by 2 x (bleed + slug).
func TestAddPrintBoxesRefusesALayoutThatWasNotApplied(t *testing.T) {
	o := PrintOptions{BleedMM: 3, CropMarks: true}
	for name, tc := range map[string]struct{ print, plain []byte }{
		"same size":    {miniPDF(a4Box), miniPDF(a4Box)},
		"bleed only":   {miniPDF("0 0 612.2835 858.8976"), miniPDF(a4Box)}, // 3 mm bleed but no mark zone
		"fewer pages":  {miniPDF("0 0 651.9685 898.5827"), miniPDF(a4Box)[:0]},
		"width only":   {miniPDF("0 0 651.9685 841.8898"), miniPDF(a4Box)},
		"off by 0.1pt": {miniPDF("0 0 652.0685 898.5827"), miniPDF(a4Box)},
	} {
		_, err := addPrintBoxes(tc.print, tc.plain, o)
		if name == "fewer pages" {
			if err == nil || err.(*Error).Code != CodePrintBoxFailed { // an unreadable reference
				t.Fatalf("%s: %v", name, err)
			}
			continue
		}
		if err == nil || err.(*Error).Code != CodePrintUnsupported || !strings.Contains(err.Error(), "the template declares fragments/print.typ but does not apply print-layout") {
			t.Fatalf("%s: want print_unsupported, got %v", name, err)
		}
	}
	// 0.01 pt of tolerance
	if _, err := addPrintBoxes(miniPDF("0 0 651.96851 898.58271"), miniPDF(a4Box), o); err != nil {
		t.Fatal(err)
	}
}

// Without bleed and mark zone nothing grows: identical PDFs are a correct result, boxes = MediaBox.
func TestAddPrintBoxesAcceptsIdenticalPDFsWithoutBleed(t *testing.T) {
	out, err := addPrintBoxes(miniPDF(a4Box), miniPDF(a4Box), PrintOptions{BleedMM: 0, MirrorMargins: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "/MediaBox[0 0 595.2756 841.8898]/BleedBox[0 0 595.2756 841.8898]/TrimBox[0 0 595.2756 841.8898]") {
		t.Fatal("boxes must equal the MediaBox")
	}
}

func TestAppliesPrintLayoutIsStaticAndIgnoresCommentsAndStrings(t *testing.T) {
	for src, want := range map[string]bool{
		"#show: print-layout\n":                                  true,
		"  #show: print-layout.with(paper: \"a4\")\n":            true,
		"#show:print-layout.with(\n  margin: 1mm)\n":             true,
		"// #show: print-layout\n":                               false,
		"/* #show: print-layout */\n":                            false,
		"/* a /* #show: print-layout */\n#show: print-layout */": false,
		"#let s = \"\\\"\n#show: print-layout\"\n":               false,
		"`\n#show: print-layout\n`":                              false,
		"#show: print-layouts\n":                                 false,
		"#set page(paper: \"a4\")\n":                             false,
		"// c\n#show: print-layout // c\n":                       true,
		"/* c */\n#show: print-layout\n":                         true,
	} {
		if got := appliesPrintLayout(src); got != want {
			t.Errorf("%q: got %v want %v", src, got, want)
		}
	}
}

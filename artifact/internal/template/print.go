package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Print options (render option `print`, contract template/v1 § Impression
// professionnelle, AGENT-1921). The option is closed: bleed in millimetres,
// mirrored margins for double-sided printing, crop marks. The Typst fragment
// fragments/print.typ lays the pages out; Typst 0.15.1 cannot write the PDF
// TrimBox and BleedBox, so addPrintBoxes writes them afterwards.
const (
	// PrintBleedMaxMM is the upper bound of bleed_mm (inclusive).
	PrintBleedMaxMM = 5.0
	// PrintBleedDefaultMM applies when print is given without bleed_mm.
	PrintBleedDefaultMM = 3.0
	// PrintMarkZoneMM is the mark zone (slug) around the bleed, only with crop marks.
	PrintMarkZoneMM = 7.0
	// PrintFragment is the library fragment a template must declare to take part.
	PrintFragment = "fragments/print.typ"
	ptPerMM       = 72.0 / 25.4
)

// PrintOptions is the validated `print` option.
type PrintOptions struct {
	BleedMM       float64 `json:"bleed_mm"`
	MirrorMargins bool    `json:"mirror_margins"`
	CropMarks     bool    `json:"crop_marks"`
}

// ParsePrint reads the JSON of the option. Empty or null means no print (nil).
// Unknown fields, wrong types, trailing data and out-of-range values are
// refused with print_invalid.
func ParsePrint(raw json.RawMessage) (*PrintOptions, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var in struct {
		Bleed  *float64 `json:"bleed_mm"`
		Mirror *bool    `json:"mirror_margins"`
		Crop   *bool    `json:"crop_marks"`
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, errf(CodePrintInvalid, "print must be an object with bleed_mm (number), mirror_margins and crop_marks (booleans): %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errf(CodePrintInvalid, "print must be a single JSON object")
	}
	// A present field must have its type: null is refused (only the whole option may be null).
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, errf(CodePrintInvalid, "print must be a single JSON object: %v", err)
	}
	for name, value := range fields {
		// encoding/json matches field names without regard to case: only the exact names are the contract.
		if name != "bleed_mm" && name != "mirror_margins" && name != "crop_marks" {
			return nil, errf(CodePrintInvalid, "print.%s is not a field of the option (bleed_mm, mirror_margins, crop_marks; names are case sensitive)", name)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errf(CodePrintInvalid, "print.%s cannot be null", name)
		}
	}
	o := &PrintOptions{BleedMM: PrintBleedDefaultMM}
	if in.Bleed != nil {
		o.BleedMM = *in.Bleed
	}
	o.MirrorMargins = in.Mirror != nil && *in.Mirror
	o.CropMarks = in.Crop != nil && *in.Crop
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return o, nil
}

// Validate checks the bounds: bleed_mm from 0 to 5 mm, both included.
func (o PrintOptions) Validate() error {
	if math.IsNaN(o.BleedMM) || math.IsInf(o.BleedMM, 0) || o.BleedMM < 0 || o.BleedMM > PrintBleedMaxMM {
		return errf(CodePrintInvalid, "print.bleed_mm %v is out of range: 0 to %v mm", o.BleedMM, PrintBleedMaxMM)
	}
	return nil
}

func (o PrintOptions) slugMM() float64 {
	if o.CropMarks {
		return PrintMarkZoneMM
	}
	return 0
}

func mm(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "mm" }

// typst renders the `print-options` value appended to /brand.typ.
func (o PrintOptions) typst() string {
	return fmt.Sprintf("#let print-options = (bleed: %s, slug: %s, mirror: %t, crop: %t)\n",
		mm(o.BleedMM), mm(o.slugMM()), o.MirrorMargins, o.CropMarks)
}

// checkPrintSupport refuses print on what cannot honour it.
func checkPrintSupport(engine, output string, fragments map[string]string, entry string) error {
	if engine != EngineTypst || output != "pdf" {
		return errf(CodePrintUnsupported, "print applies to the typst engine and the pdf output only (engine %q, output %q)", engine, output)
	}
	if _, ok := fragments[PrintFragment]; !ok {
		return errf(CodePrintUnsupported, "the template does not declare the fragment %s in its manifest, so it cannot take the print option", PrintFragment)
	}
	if !appliesPrintLayout(entry) {
		return errf(CodePrintUnsupported, "the template declares fragments/print.typ but does not apply print-layout")
	}
	return nil
}

var printLayoutRule = regexp.MustCompile(`(?m)^[ \t]*#show:[ \t]*print-layout\b`)

// appliesPrintLayout reports whether the Typst entry applies the fragment with a
// `#show: print-layout` (or `#show: print-layout.with(...)`) rule, outside comments,
// strings and raw text. The rest is checked after the compilation (page sizes).
func appliesPrintLayout(src string) bool {
	return printLayoutRule.MatchString(maskTypstNonCode(src))
}

// maskTypstNonCode blanks line and block comments (nested), strings and raw text of a
// Typst source with spaces, keeping newlines, so a rule can be searched in code only.
func maskTypstNonCode(src string) string {
	b := []byte(src)
	blank := func(from, to int) {
		for i := from; i < to && i < len(b); i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	for i := 0; i < len(b); {
		switch {
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			j := i
			for j < len(b) && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			depth, j := 1, i+2
			for j < len(b) && depth > 0 {
				switch {
				case b[j] == '/' && j+1 < len(b) && b[j+1] == '*':
					depth++
					j += 2
				case b[j] == '*' && j+1 < len(b) && b[j+1] == '/':
					depth--
					j += 2
				default:
					j++
				}
			}
			blank(i, j)
			i = j
		case b[i] == '"':
			j := i + 1
			for j < len(b) && b[j] != '"' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			blank(i, j+1)
			i = j + 1
		case b[i] == '`':
			n := 0
			for i+n < len(b) && b[i+n] == '`' {
				n++
			}
			end := bytes.Index(b[i+n:], bytes.Repeat([]byte("`"), n))
			j := len(b)
			if end >= 0 {
				j = i + n + end + n
			}
			blank(i, j)
			i = j
		default:
			i++
		}
	}
	return string(b)
}

var (
	pdfPageType   = regexp.MustCompile(`/Type\s*/Page\b`)
	pdfMediaBox   = regexp.MustCompile(`/MediaBox\s*\[\s*([-+0-9.]+)\s+([-+0-9.]+)\s+([-+0-9.]+)\s+([-+0-9.]+)\s*\]`)
	pdfObjHeader  = regexp.MustCompile(`^(\d+)\s+(\d+)\s+obj\b`)
	pdfStartxref  = regexp.MustCompile(`startxref\s+(\d+)\s*%%EOF\s*$`)
	pdfXrefEntry  = regexp.MustCompile(`^(\d{10})\s+(\d{5})\s+([nf])\s*$`)
	pdfSubsection = regexp.MustCompile(`^(\d+)\s+(\d+)\s*$`)
)

func boxErr(format string, args ...any) error {
	return errf(CodePrintBoxFailed, format, args...)
}

func pdfNumber(v float64) string {
	s := strconv.FormatFloat(v, 'f', 5, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

func pdfBox(x0, y0, x1, y1 float64) string {
	return "[" + pdfNumber(x0) + " " + pdfNumber(y0) + " " + pdfNumber(x1) + " " + pdfNumber(y1) + "]"
}

type pdfPage struct {
	num, gen int
	body     string
	mbEnd    int // end of the /MediaBox array in body
	mb       [4]float64
}

type pdfFile struct {
	pages   []pdfPage // in object number order (Typst numbers pages in page order)
	prev    int       // offset of the xref section
	trailer string
}

// readPDF reads the page objects of a PDF written by Typst: a classic xref table
// and plain page dictionaries. Any other structure is refused (print_box_failed).
func readPDF(pdf []byte) (*pdfFile, error) {
	m := pdfStartxref.FindSubmatchIndex(pdf)
	if m == nil {
		return nil, boxErr("the PDF has no trailing startxref")
	}
	prev, err := strconv.Atoi(string(pdf[m[2]:m[3]]))
	if err != nil || prev <= 0 || prev >= len(pdf) {
		return nil, boxErr("the PDF startxref offset is invalid")
	}
	section := string(pdf[prev:m[0]])
	if !strings.HasPrefix(section, "xref") {
		return nil, boxErr("the PDF uses a cross-reference stream, not a classic xref table")
	}
	lines := strings.Split(strings.ReplaceAll(section, "\r", ""), "\n")
	offsets := map[int]int{} // object number -> offset, in-use entries only
	gens := map[int]int{}
	var trailer string
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if line == "trailer" {
			trailer = strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
			break
		}
		sub := pdfSubsection.FindStringSubmatch(line)
		if sub == nil {
			return nil, boxErr("the PDF xref table is malformed")
		}
		first, _ := strconv.Atoi(sub[1])
		count, _ := strconv.Atoi(sub[2])
		for k := 0; k < count; k++ {
			i++
			if i >= len(lines) {
				return nil, boxErr("the PDF xref table is truncated")
			}
			e := pdfXrefEntry.FindStringSubmatch(strings.TrimSpace(lines[i]))
			if e == nil {
				return nil, boxErr("the PDF xref entry %d is malformed", first+k)
			}
			if e[3] == "n" {
				off, _ := strconv.Atoi(e[1])
				gen, _ := strconv.Atoi(e[2])
				offsets[first+k], gens[first+k] = off, gen
			}
		}
	}
	if !strings.HasPrefix(trailer, "<<") || !strings.HasSuffix(trailer, ">>") {
		return nil, boxErr("the PDF trailer dictionary is missing")
	}
	if strings.Contains(trailer, "/Prev") || strings.Contains(trailer, "/XRefStm") {
		return nil, boxErr("the PDF already has an incremental update")
	}
	nums := make([]int, 0, len(offsets))
	for n := range offsets {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	f := &pdfFile{prev: prev, trailer: trailer}
	for _, n := range nums {
		off := offsets[n]
		if off <= 0 || off >= len(pdf) {
			continue
		}
		end := bytes.Index(pdf[off:], []byte("endobj"))
		if end < 0 {
			continue
		}
		obj := string(pdf[off : off+end])
		h := pdfObjHeader.FindStringSubmatch(obj)
		if h == nil || h[1] != strconv.Itoa(n) {
			continue
		}
		body := strings.TrimSpace(obj[len(h[0]):])
		if !strings.HasPrefix(body, "<<") || strings.Contains(body, "stream") || !pdfPageType.MatchString(body) {
			continue
		}
		if strings.Contains(body, "/TrimBox") || strings.Contains(body, "/BleedBox") {
			return nil, boxErr("page object %d already has a TrimBox or a BleedBox", n)
		}
		mb := pdfMediaBox.FindStringSubmatchIndex(body)
		if mb == nil {
			return nil, boxErr("page object %d has no MediaBox", n)
		}
		pg := pdfPage{num: n, gen: gens[n], body: body, mbEnd: mb[1]}
		for k := range pg.mb {
			pg.mb[k], err = strconv.ParseFloat(body[mb[2+2*k]:mb[3+2*k]], 64)
			if err != nil {
				return nil, boxErr("page object %d has an unreadable MediaBox", n)
			}
		}
		f.pages = append(f.pages, pg)
	}
	if len(f.pages) == 0 {
		return nil, boxErr("the PDF has no page object")
	}
	return f, nil
}

// addPrintBoxes adds /BleedBox and /TrimBox to every page of a PDF written by
// Typst, as an incremental update: the original bytes are kept, the page objects
// are appended with their boxes, then a new xref section and trailer that point
// back to the previous one (/Prev). Nothing in the original file is shifted and
// no clock or random value is involved, so the output is reproducible.
//
// plain is the PDF of the same template compiled without print. It proves that
// the layout was applied: with the same page count, every page of pdf must be the
// plain page grown by 2 x (bleed + slug) in width and height, to 0.01 pt; else the
// template declares the fragment without applying print-layout (print_unsupported).
// The finished size comes from the plain MediaBox: TrimBox is the plain page placed
// at (bleed + slug) inside the MediaBox, BleedBox the TrimBox grown by the bleed.
// The standard library is enough because Typst writes plain page dictionaries and a
// classic xref table; any other structure is refused rather than guessed
// (print_box_failed).
func addPrintBoxes(pdf, plain []byte, o PrintOptions) ([]byte, error) {
	f, err := readPDF(pdf)
	if err != nil {
		return nil, err
	}
	pf, err := readPDF(plain)
	if err != nil {
		return nil, err
	}
	notApplied := func(format string, args ...any) error {
		return errf(CodePrintUnsupported, "the template declares fragments/print.typ but does not apply print-layout ("+format+")", args...)
	}
	if len(f.pages) != len(pf.pages) {
		return nil, notApplied("%d pages with print, %d without", len(f.pages), len(pf.pages))
	}
	bleed := o.BleedMM * ptPerMM
	pad := (o.BleedMM + o.slugMM()) * ptPerMM
	updates := make([]pdfPage, len(f.pages))
	for i, pg := range f.pages {
		w, h := pg.mb[2]-pg.mb[0], pg.mb[3]-pg.mb[1]
		pw, ph := pf.pages[i].mb[2]-pf.pages[i].mb[0], pf.pages[i].mb[3]-pf.pages[i].mb[1]
		if math.Abs(w-pw-2*pad) > 0.01 || math.Abs(h-ph-2*pad) > 0.01 {
			return nil, notApplied("page %d is %.2f x %.2f pt with print and %.2f x %.2f pt without, expected %.2f pt more on each axis", i+1, w, h, pw, ph, 2*pad)
		}
		tx0, ty0 := pg.mb[0]+pad, pg.mb[1]+pad
		tx1, ty1 := tx0+pw, ty0+ph
		boxes := "/BleedBox" + pdfBox(tx0-bleed, ty0-bleed, tx1+bleed, ty1+bleed) + "/TrimBox" + pdfBox(tx0, ty0, tx1, ty1)
		pg.body = pg.body[:pg.mbEnd] + boxes + pg.body[pg.mbEnd:]
		updates[i] = pg
	}
	sort.Slice(updates, func(a, b int) bool { return updates[a].num < updates[b].num })

	var out bytes.Buffer
	out.Write(pdf)
	if !bytes.HasSuffix(pdf, []byte("\n")) {
		out.WriteByte('\n')
	}
	newOff := map[int]int{}
	for _, u := range updates {
		newOff[u.num] = out.Len()
		fmt.Fprintf(&out, "%d %d obj\n%s\nendobj\n", u.num, u.gen, u.body)
	}
	xrefAt := out.Len()
	// Subsection 0 starts with the head of the free list, as in the original table; the
	// updated page objects follow in subsections of consecutive numbers.
	out.WriteString("xref\n0 1\n0000000000 65535 f \n")
	for i := 0; i < len(updates); {
		j := i + 1
		for j < len(updates) && updates[j].num == updates[j-1].num+1 {
			j++
		}
		fmt.Fprintf(&out, "%d %d\n", updates[i].num, j-i)
		for _, u := range updates[i:j] {
			fmt.Fprintf(&out, "%010d %05d n \n", newOff[u.num], u.gen)
		}
		i = j
	}
	fmt.Fprintf(&out, "trailer\n%s/Prev %d>>\nstartxref\n%d\n%%%%EOF\n", strings.TrimSuffix(f.trailer, ">>"), f.prev, xrefAt)
	return out.Bytes(), nil
}

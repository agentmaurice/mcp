package template_test

// AGENT-1919: the `qrModules` template function and the `qr/v1` fragment
// (contract/fragments/qr.typ). Checks:
//   - the matrices decode (independent ZXing decoder, test dependency only) to exactly the
//     input: ASCII, accented UTF-8, a payment URL, the four levels, every length from 1 to
//     512 bytes, and the smallest version for the known capacities;
//   - the QR code drawn by the fragment, read back from an SVG compilation, is the same
//     matrix, decodes to the input and uses only the darkest charter colour and white;
//   - two PDF renders are byte identical;
//   - empty, too long, wrong-type input and unknown levels fail with their stable code;
//   - a malformed matrix given to the fragment is refused with "qr/v1: ".

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template/templatetest"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst/typsttest"
)

const qrTextManifest = `{"schema":"agentmaurice.template/v1","key":"qr-text","version":"1.0.0","kind":"message","profile":"message",
	"source":{"engine":"text","entry":"main.txt"},"outputs":["txt"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":[]}`

// qrRender runs `{{ qrModules .data.t .data.l }}` (or the one-argument form when level is
// empty and withLevel is false) in a text template and returns the raw output.
func qrRender(t *testing.T, src string, data map[string]any) (string, error) {
	t.Helper()
	tpl := template.Template{Manifest: json.RawMessage(qrTextManifest), Files: map[string]string{
		"main.txt": src, "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"}}
	ctx, assets := templatetest.Context(t)
	res, err := template.Render(context.Background(), template.Request{Template: tpl, Data: data, Context: ctx, Assets: assets,
		Output: "txt", Mode: template.ModePreview}, nil, template.Options{Timeout: 30 * time.Second})
	if err != nil {
		return "", err
	}
	return string(res.Content), nil
}

func qrMatrix(t *testing.T, text, level string) []string {
	t.Helper()
	out, err := qrRender(t, `{{ qrModules .data.t .data.l }}`, map[string]any{"t": text, "l": level})
	if err != nil {
		t.Fatalf("qrModules(%d bytes, %s): %v", len(text), level, err)
	}
	return strings.Split(out, "/")
}

// qrDecode reads a module matrix like a phone would: a bitmap with a 4-module quiet zone.
func qrDecode(t *testing.T, rows []string) string {
	t.Helper()
	return qrDecodeResult(t, rows, nil).GetText()
}

// qrDecodeResult is qrDecode with extra ZXing hints, and returns the whole result.
func qrDecodeResult(t *testing.T, rows []string, extra map[gozxing.DecodeHintType]interface{}) *gozxing.Result {
	t.Helper()
	const scale, quiet = 4, 4
	n := len(rows)
	side := (n + 2*quiet) * scale
	img := image.NewGray(image.Rect(0, 0, side, side))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	for y, row := range rows {
		if len(row) != n {
			t.Fatalf("row %d has %d modules, matrix has %d rows", y, len(row), n)
		}
		for x, c := range row {
			if c != '1' {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.SetGray((x+quiet)*scale+dx, (y+quiet)*scale+dy, color.Gray{})
				}
			}
		}
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	hints := map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_PURE_BARCODE: true}
	for k, v := range extra {
		hints[k] = v
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, hints)
	if err != nil {
		t.Fatalf("the matrix does not decode: %v", err)
	}
	return res
}

// qrLeadingMode returns the mode indicator (first 4 bits) and the next 8 bits of the data
// codewords: 4 and anything for a byte segment alone, 7 then the designator for an ECI segment.
func qrLeadingMode(t *testing.T, res *gozxing.Result) (mode, next int) {
	t.Helper()
	raw := res.GetRawBytes()
	if len(raw) < 2 {
		t.Fatalf("raw bytes too short: %d", len(raw))
	}
	return int(raw[0] >> 4), int(raw[0]&0x0F)<<4 | int(raw[1]>>4)
}

var qrISOHint = map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_CHARACTER_SET: "ISO-8859-1"}

// A text with a byte outside ASCII carries an ECI segment (26, UTF-8) before the byte
// segment: a reader whose default is ISO-8859-1 still reads the exact text. Pure ASCII
// carries no ECI.
func TestQRModulesECIForNonASCII(t *testing.T) {
	for _, level := range []string{"L", "M", "Q", "H"} {
		for _, text := range []string{"é", "Café de l'Étoile", "日本語 ☕ €12,50", strings.Repeat("é", 200)} {
			rows := qrMatrix(t, text, level)
			res := qrDecodeResult(t, rows, nil)
			if mode, des := qrLeadingMode(t, res); mode != 7 || des != 26 {
				t.Errorf("%s %q: leading segment mode %d designator %d, want ECI (7) 26", level, text, mode, des)
			}
			if res.GetText() != text {
				t.Errorf("%s %q: decoded %q", level, text, res.GetText())
			}
			// the reader is told ISO-8859-1 by default: only the ECI keeps the text exact
			if got := qrDecodeResult(t, rows, qrISOHint).GetText(); got != text {
				t.Errorf("%s %q: with a default of ISO-8859-1, decoded %q", level, text, got)
			}
		}
		for _, text := range []string{"A", "Hello, QR code!", "https://pay.example.com/i/1?a=b&c=d"} {
			res := qrDecodeResult(t, qrMatrix(t, text, level), nil)
			if mode, _ := qrLeadingMode(t, res); mode != 4 {
				t.Errorf("%s %q: pure ASCII must start with the byte mode (4), got %d", level, text, mode)
			}
		}
	}
}

// The ECI costs 12 bits: the smallest version is computed with it. Known capacities (byte
// mode, in bytes) minus the 12 bits give the non-ASCII capacities below.
func TestQRModulesNonASCIICapacity(t *testing.T) {
	// Data codewords of versions 1 to 3 (L 19/34/55, M 16/28/44, Q 13/22/34, H 9/16/26) minus the
	// 12 + 4 + 8 header bits (ECI, byte mode, count), in bytes.
	for level, caps := range map[string][]int{"L": {16, 31, 52}, "M": {13, 25, 41}, "Q": {10, 19, 31}, "H": {6, 13, 23}} {
		for i, c := range caps {
			text := "é" + strings.Repeat("x", c-2)
			if got := len(qrMatrix(t, text, level)); got != 17+4*(i+1) {
				t.Errorf("%s: %d non-ASCII bytes use %d modules, want version %d", level, c, got, i+1)
			}
			if got := len(qrMatrix(t, text+"x", level)); got != 17+4*(i+2) {
				t.Errorf("%s: %d non-ASCII bytes use %d modules, want version %d", level, c+1, got, i+2)
			}
		}
	}
	for _, level := range []string{"L", "M", "Q", "H"} {
		text := strings.Repeat("é", 256)
		rows := qrMatrix(t, text, level)
		if len(rows) > 117 {
			t.Fatalf("%s: 512 non-ASCII bytes need %d modules, above the 117 of version 25", level, len(rows))
		}
		if got := qrDecodeResult(t, rows, qrISOHint).GetText(); got != text {
			t.Fatalf("%s: 512 non-ASCII bytes decode to %d bytes", level, len(got))
		}
	}
}

func TestQRModulesDecodeToTheInput(t *testing.T) {
	long := strings.Repeat("Étoile café déjà vu ☕ ", 20)[:300]
	for !isValidUTF8Prefix(long) {
		long = long[:len(long)-1]
	}
	cases := []struct{ name, text string }{
		{"one byte", "A"},
		{"ascii", "Hello, QR code!"},
		{"digits", "0123456789012345678901234567890123456789"},
		{"accents", "Café de l'Étoile : déjà réglé, merci"},
		{"symbols", "日本語 ☕ €12,50"},
		{"payment url", "https://pay.example.com/i/FA-2026-0042?amount=1250.00&cur=EUR&sig=3f9c1a"},
		{"payment url with accents", "https://pay.example.com/reçu/17?nom=Étoile%20Café&note=déjà réglé, merci"},
		{"long accented", long},
		{"slashes", "a/b//c///d"},
	}
	for _, level := range []string{"L", "M", "Q", "H"} {
		for _, tc := range cases {
			t.Run(level+"/"+tc.name, func(t *testing.T) {
				rows := qrMatrix(t, tc.text, level)
				if got := qrDecode(t, rows); got != tc.text {
					t.Fatalf("decoded %q, want %q", got, tc.text)
				}
			})
		}
	}
}

func isValidUTF8Prefix(s string) bool { return strings.ToValidUTF8(s, "") == s }

// Every length from 1 to the 512-byte bound, at the four levels: this walks every version
// the encoder can choose, with the block structure and the alignment patterns of each.
func TestQRModulesEveryLengthDecodes(t *testing.T) {
	if testing.Short() {
		t.Skip("long")
	}
	rnd := rand.New(rand.NewSource(1919))
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 -_.:/?=&%#+*"
	versions := map[int]bool{}
	for _, level := range []string{"L", "M", "Q", "H"} {
		for n := 1; n <= 512; n++ {
			b := make([]byte, n)
			for i := range b {
				b[i] = alphabet[rnd.Intn(len(alphabet))]
			}
			rows := qrMatrix(t, string(b), level)
			if got := qrDecode(t, rows); got != string(b) {
				t.Fatalf("level %s, %d bytes: decoded %q", level, n, got)
			}
			versions[len(rows)] = true
			// the same length with a leading accent (ECI segment), read with ISO-8859-1 as default
			if n >= 2 {
				u := "é" + string(b[:n-2])
				urows := qrMatrix(t, u, level)
				if got := qrDecodeResult(t, urows, qrISOHint).GetText(); got != u {
					t.Fatalf("level %s, %d non-ASCII bytes: decoded %q", level, n, got)
				}
			}
		}
	}
	if len(versions) != 25 {
		t.Fatalf("the lengths 1..512 must use all 25 versions, got %d sizes", len(versions))
	}
}

// The smallest version that holds the text: the byte-mode capacities of the standard.
func TestQRModulesUsesTheSmallestVersion(t *testing.T) {
	capacity := map[string][]int{
		"L": {17, 32, 53, 78, 106, 134, 154, 192, 230, 271},
		"M": {14, 26, 42, 62, 84, 106, 122, 152, 180, 213},
		"Q": {11, 20, 32, 46, 60, 74, 86, 108, 130, 151},
		"H": {7, 14, 24, 34, 44, 58, 64, 84, 98, 119},
	}
	for level, caps := range capacity {
		for i, c := range caps {
			version := i + 1
			if got := len(qrMatrix(t, strings.Repeat("x", c), level)); got != 17+4*version {
				t.Errorf("%s: %d bytes use %d modules, want version %d (%d)", level, c, got, version, 17+4*version)
			}
			if got := len(qrMatrix(t, strings.Repeat("x", c+1), level)); got != 17+4*(version+1) {
				t.Errorf("%s: %d bytes use %d modules, want version %d (%d)", level, c+1, got, version+1, 17+4*(version+1))
			}
		}
	}
	if got := len(qrMatrix(t, strings.Repeat("x", 512), "H")); got != 117 {
		t.Errorf("512 bytes at H use %d modules, want 117 (version 25, the maximum)", got)
	}
}

func TestQRModulesIsDeterministicAndLevelsDiffer(t *testing.T) {
	text := "https://pay.example.com/i/FA-2026-0042"
	a, err := qrRender(t, `{{ qrModules .data.t "Q" }}`, map[string]any{"t": text})
	b, err2 := qrRender(t, `{{ qrModules .data.t "Q" }}`, map[string]any{"t": text})
	if err != nil || err2 != nil || a != b {
		t.Fatalf("two calls must give the same matrix (%v, %v)", err, err2)
	}
	def, _ := qrRender(t, `{{ qrModules .data.t }}`, map[string]any{"t": text})
	m, _ := qrRender(t, `{{ qrModules .data.t "M" }}`, map[string]any{"t": text})
	if def != m {
		t.Fatal("the default level is M")
	}
	seen := map[string]string{}
	for _, l := range []string{"L", "M", "Q", "H"} {
		out, _ := qrRender(t, `{{ qrModules .data.t .data.l }}`, map[string]any{"t": text, "l": l})
		if other, dup := seen[out]; dup {
			t.Fatalf("levels %s and %s give the same matrix", l, other)
		}
		seen[out] = l
		if strings.Trim(out, "01/") != "" || strings.HasSuffix(out, "/") || strings.Contains(out, "//") {
			t.Fatalf("level %s: the matrix has characters other than 0, 1 and one / between rows", l)
		}
	}
}

// Reference vectors shared with pilote-maurice (tests/test_contract.py, QR reference test): the
// reference renderer carries a copy of this encoder and must give the same matrices.
func TestQRModulesReferenceVectors(t *testing.T) {
	for _, tc := range []struct {
		text    string
		vectors map[string]string
	}{
		{"https://pay.example.com/i/FA-2026-0042?amount=1250.00&cur=EUR&sig=3f9c1a", qrReferenceHashes},
		{"https://pay.example.com/reçu/17?nom=Étoile%20Café&note=déjà réglé, merci ☕", qrReferenceHashesAccents},
	} {
		checkQRVectors(t, tc.text, tc.vectors)
	}
}

func checkQRVectors(t *testing.T, text string, vectors map[string]string) {
	t.Helper()
	want := map[string]string{
		"L": "", "M": "", "Q": "", "H": "",
	}
	for level := range want {
		sum := sha256.Sum256([]byte(strings.Join(qrMatrix(t, text, level), "/")))
		want[level] = hex.EncodeToString(sum[:])
	}
	if len(vectors) != 4 {
		t.Fatal("four reference vectors expected")
	}
	for level, h := range vectors {
		if want[level] != h {
			t.Errorf("level %s: matrix sha256 %s, reference vector %s", level, want[level], h)
		}
	}
}

// sha256 of the matrix text of qr-badge's default payment_url, by level, produced by the
// reference renderer of pilote-maurice (a separate copy of the encoder).
var qrReferenceHashes = map[string]string{
	"L": "97137fe31bc3b13c567d2df727da5dd7e37b0eeab54278f9fa83a6b1b59a15ff",
	"M": "4c6fff20ce77b09e968ae7ac5fac125282372dff33a1c3ec773b219b006d2cab",
	"Q": "a00bcbb6e903790018971121a39369592b3aa5d1050391f4e69b7a889e8543a6",
	"H": "80ede1afd1269d7f1e4a157df2bb40cefe13d945745d746d5b9c89a3c5a097dd",
}

// Same for the `accents` example (non-ASCII bytes: ECI segment).
var qrReferenceHashesAccents = map[string]string{
	"L": "d24721bf3779af68c115e8c9d82a8d44f620c9c22358685fd7f0d6082f1eb482",
	"M": "bb993f853070612c547799149bcd22281def6d27e9d57c0cdbcebf3c8c6d4bfe",
	"Q": "028530d731badef74d793f9ab8e2328a5f3266d8f24ec15084be8912c951c656",
	"H": "2f252ea5be8a2c615257dbda8197b4e440430aa1c24efe4370fcd45839c17539",
}

func TestQRModulesErrors(t *testing.T) {
	big := strings.Repeat("a", 513)
	cases := []struct {
		name, src string
		data      map[string]any
		code      string
	}{
		{"empty", `{{ qrModules .data.t }}`, map[string]any{"t": ""}, template.CodeQRInputInvalid},
		{"absent", `{{ qrModules .data.nope }}`, map[string]any{}, template.CodeTemplateExec},
		{"null", `{{ qrModules .data.t }}`, map[string]any{"t": nil}, template.CodeTemplateExec},
		{"number", `{{ qrModules .data.t }}`, map[string]any{"t": 12.0}, template.CodeQRInputInvalid},
		{"list", `{{ qrModules .data.t }}`, map[string]any{"t": []any{"a"}}, template.CodeQRInputInvalid},
		{"513 bytes", `{{ qrModules .data.t }}`, map[string]any{"t": big}, template.CodeQRInputTooLong},
		{"513 bytes at L", `{{ qrModules .data.t "L" }}`, map[string]any{"t": big}, template.CodeQRInputTooLong},
		{"257 two-byte letters", `{{ qrModules .data.t }}`, map[string]any{"t": strings.Repeat("é", 257)}, template.CodeQRInputTooLong},
		{"level not a string", `{{ qrModules "a" 12 }}`, map[string]any{}, template.CodeTemplateExec},
		{"unknown level", `{{ qrModules .data.t "X" }}`, map[string]any{"t": "a"}, template.CodeQRLevelInvalid},
		{"lower case level", `{{ qrModules .data.t "m" }}`, map[string]any{"t": "a"}, template.CodeQRLevelInvalid},
		{"empty level", `{{ qrModules .data.t "" }}`, map[string]any{"t": "a"}, template.CodeQRLevelInvalid},
		{"two levels", `{{ qrModules .data.t "L" "M" }}`, map[string]any{"t": "a"}, template.CodeQRLevelInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := qrRender(t, tc.src, tc.data)
			var terr *template.Error
			if err == nil || !asTemplateError(err, &terr) || terr.Code != tc.code {
				t.Fatalf("want code %s, got %v", tc.code, err)
			}
		})
	}
	// the bound itself is accepted
	if _, err := qrRender(t, `{{ qrModules .data.t "H" }}`, map[string]any{"t": strings.Repeat("a", 512)}); err != nil {
		t.Fatalf("512 bytes must be accepted: %v", err)
	}
}

// ---------------------------------------------------------------- the fragment

const qrTypstManifest = `{"schema":"agentmaurice.template/v1","key":"qr-inline","version":"1.0.0","kind":"document","profile":"document",
	"source":{"engine":"typst","entry":"main.typ"},"outputs":["pdf"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":["fragments/qr.typ"]}`

func qrInline(main string) template.Template {
	return template.Template{Manifest: json.RawMessage(qrTypstManifest), Files: map[string]string{
		"main.typ": main, "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"}}
}

// qrSVG renders one QR code on an otherwise empty page and returns the SVG of the page.
func qrSVG(t *testing.T, runner typst.Runner, text, level string) string {
	t.Helper()
	main := "#import \"/fragments/qr.typ\": qr-code\n#set page(width: 80mm, height: 80mm, margin: 5mm)\n" +
		"#qr-code([{{ qrModules .data.t .data.l }}], size: 60mm)\n"
	ctx, assets := templatetest.Context(t)
	var pages []string
	if _, err := template.Render(context.Background(), template.Request{Template: qrInline(main), Data: map[string]any{"t": text, "l": level},
		Context: ctx, Assets: assets, Output: "pdf"}, svgCompiler{Runner: runner, svgs: &pages}, template.Options{Timeout: 120 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("want one page, got %d", len(pages))
	}
	return pages[0]
}

var qrRunRe = regexp.MustCompile(`<path fill="(#[0-9a-f]{6})" fill-rule="nonzero" transform="translate\((-?[0-9.]+) (-?[0-9.]+)\)" d="M 0 0v ([0-9.]+)h ([0-9.]+)v -?[0-9.]+Z "/>`)

// qrDarkest is the darkest colour of the charter (relative luminance, WCAG).
func qrDarkest(t *testing.T) string {
	t.Helper()
	ctx, _ := templatetest.Context(t)
	lin := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	best, bestL := "", 2.0
	for _, v := range ctx.Brand["colors"].(map[string]any) {
		h := strings.ToLower(v.(string))
		r, _ := strconv.ParseUint(h[1:3], 16, 8)
		g, _ := strconv.ParseUint(h[3:5], 16, 8)
		b, _ := strconv.ParseUint(h[5:7], 16, 8)
		l := 0.2126*lin(float64(r)/255) + 0.7152*lin(float64(g)/255) + 0.0722*lin(float64(b)/255)
		if l < bestL {
			best, bestL = h, l
		}
	}
	return best
}

// qrFromSVG rebuilds the module matrix from the dark runs drawn by the fragment, and checks
// the palette: dark runs in the darkest charter colour, everything else white.
func qrFromSVG(t *testing.T, svg string) []string {
	t.Helper()
	dark := qrDarkest(t)
	type run struct{ x, y, w, h float64 }
	var runs []run
	for _, m := range regexp.MustCompile(`fill="(#[0-9a-fA-F]{3,8})"`).FindAllStringSubmatch(svg, -1) {
		if c := strings.ToLower(m[1]); c != "#ffffff" && c != dark {
			t.Fatalf("colour %s is neither white nor the darkest charter colour %s", c, dark)
		}
	}
	for _, m := range qrRunRe.FindAllStringSubmatch(svg, -1) {
		if m[1] != dark {
			continue
		}
		f := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
		runs = append(runs, run{f(m[2]), f(m[3]), f(m[5]), f(m[4])})
	}
	if len(runs) == 0 {
		t.Fatal("no dark run found in the SVG")
	}
	u := runs[0].h
	minX, minY, maxR := runs[0].x, runs[0].y, runs[0].x+runs[0].w
	for _, r := range runs {
		minX, minY, maxR = math.Min(minX, r.x), math.Min(minY, r.y), math.Max(maxR, r.x+r.w)
		if math.Abs(r.h-u) > 1e-6 {
			t.Fatalf("runs of different heights: %v and %v", r.h, u)
		}
	}
	n := int(math.Round((maxR - minX) / u))
	grid := make([][]byte, n)
	for i := range grid {
		grid[i] = []byte(strings.Repeat("0", n))
	}
	for _, r := range runs {
		col, row, length := int(math.Round((r.x-minX)/u)), int(math.Round((r.y-minY)/u)), int(math.Round(r.w/u))
		if math.Abs(float64(col)*u-(r.x-minX)) > 0.01 || math.Abs(float64(row)*u-(r.y-minY)) > 0.01 {
			t.Fatalf("a run is off the module grid: %+v", r)
		}
		for i := 0; i < length; i++ {
			if grid[row][col+i] == '1' {
				t.Fatalf("two runs overlap at row %d, column %d", row, col+i)
			}
			grid[row][col+i] = '1'
		}
	}
	rows := make([]string, n)
	for i, g := range grid {
		rows[i] = string(g)
	}
	return rows
}

func TestQRFragmentDrawsTheMatrixAndDecodes(t *testing.T) {
	runner := typsttest.Runner(t)
	inputs := []string{
		"A",
		"https://pay.example.com/i/FA-2026-0042?amount=1250.00&cur=EUR&sig=3f9c1a",
		"Café de l'Étoile : déjà réglé ☕ " + strings.Repeat("0123456789", 12),
	}
	for _, level := range []string{"L", "M", "Q", "H"} {
		for i, text := range inputs {
			t.Run(fmt.Sprintf("%s/%d", level, i), func(t *testing.T) {
				want := qrMatrix(t, text, level)
				got := qrFromSVG(t, qrSVG(t, runner, text, level))
				if strings.Join(got, "/") != strings.Join(want, "/") {
					t.Fatalf("the drawn matrix differs from qrModules (%d modules drawn, %d expected)", len(got), len(want))
				}
				if dec := qrDecode(t, got); dec != text {
					t.Fatalf("the drawn QR code decodes to %q, want %q", dec, text)
				}
			})
		}
	}
}

func TestQRBadgeGoldenIsReproducible(t *testing.T) {
	runner := typsttest.Runner(t)
	for _, example := range []string{"default", "accents"} {
		t.Run(example, func(t *testing.T) {
			a := render(t, runner, "qr-badge", example, "pdf")
			b := render(t, runner, "qr-badge", example, "pdf")
			if !strings.HasPrefix(string(a.Content), "%PDF-") || a.Pages != 1 {
				t.Fatalf("not a one-page PDF (pages=%d)", a.Pages)
			}
			if string(a.Content) != string(b.Content) {
				t.Fatal("two renders must give the same bytes")
			}
		})
	}
}

func TestQRFragmentRefusesMalformedMatrices(t *testing.T) {
	runner := typsttest.Runner(t)
	good := strings.Join(qrMatrix(t, "hello", "M"), "/") // version 1, 21 x 21
	rows := strings.Split(good, "/")
	join := func(r []string) string { return strings.Join(r, "/") }
	cases := []struct{ name, matrix, want string }{
		{"empty", "", "the matrix must have 21 + 4k rows"},
		{"one row", rows[0], "the matrix must have 21 + 4k rows"},
		{"22 rows", join(append(append([]string{}, rows...), rows[0])), "the matrix must have 21 + 4k rows"},
		{"121 rows", join(repeatStr(121, strings.Repeat("0", 121))), "at most 117"},
		{"not square", join(append(append([]string{}, rows[:20]...), rows[20][:20])), "the matrix must be square"},
		{"too wide", join(append(append([]string{}, rows[:20]...), rows[20]+"0")), "the matrix must be square"},
		{"character 2", strings.Replace(good, "1", "2", 1), "only contain the characters 0 and 1"},
		{"letter", strings.Replace(good, "0", "x", 1), "only contain the characters 0 and 1"},
		{"space", strings.Replace(good, "0", " ", 1), "only contain the characters 0 and 1"},
		{"empty row", join(append(append([]string{}, rows[:20]...), "")), "only contain the characters 0 and 1"},
		{"newline separator", strings.ReplaceAll(good, "/", "\n"), "the matrix must have 21 + 4k rows"},
	}
	run := func(matrix string) error {
		main := "#import \"/fragments/qr.typ\": qr-code\n#qr-code([{{ .data.m }}], size: 30mm)\n"
		ctx, assets := templatetest.Context(t)
		_, err := template.Render(context.Background(), template.Request{Template: qrInline(main), Data: map[string]any{"m": matrix},
			Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{Timeout: 120 * time.Second})
		return err
	}
	if err := run(good); err != nil {
		t.Fatalf("a valid matrix must render: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.matrix)
			var terr *template.Error
			if err == nil || !asTemplateError(err, &terr) || terr.Code != template.CodeCompileFailed || !strings.Contains(terr.Message, "qr/v1: ") {
				t.Fatalf("want a compile error carrying \"qr/v1: \", got %v", err)
			}
			if !strings.Contains(terr.Message, tc.want) {
				t.Fatalf("message %q does not contain %q", terr.Message, tc.want)
			}
		})
	}
}

func repeatStr(n int, s string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

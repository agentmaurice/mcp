package template_test

// AGENT-1582 lot 2: the chart/v1 fragments (contract/fragments/charts.typ) are
// rendered by the real pinned Typst. Checks: reproducible bytes, French text and
// numbers extractable from the PDF, charter-only colours, numeric geometry read back
// from an SVG compilation (bar heights proportional to the values, signs included,
// line ordinates affine in the values), seeded faults the geometry check must catch,
// and out-of-bounds data refused with "chart/v1: ".

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template/templatetest"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst/typsttest"
)

// svgCompiler also compiles every page to SVG (same project, same flags) next to
// the PNG pass of Render, and keeps the SVG text: the project directory is removed
// when Render returns.
type svgCompiler struct {
	typst.Runner
	svgs *[]string
}

func (c svgCompiler) Compile(ctx context.Context, req template.CompileRequest) error {
	if err := c.Runner.Compile(ctx, req); err != nil {
		return err
	}
	if req.Format != "png" || c.svgs == nil {
		return nil
	}
	r2 := req
	r2.Format, r2.Out = "svg", "chart-{0p}.svg"
	if err := c.Runner.Compile(ctx, r2); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(req.OutDir, "chart-*.svg"))
	sort.Strings(files)
	*c.svgs = nil
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		*c.svgs = append(*c.svgs, string(b))
	}
	return nil
}

// dashboard renders the dashboard golden after `mutate` changed its example data.
func dashboard(t *testing.T, runner typst.Runner, mutate func(data map[string]any)) (pdf []byte, svgs []string) {
	t.Helper()
	tpl := templatetest.Load(t, "valid/dashboard")
	data := templatetest.Data(t, tpl, "default").(map[string]any)
	if mutate != nil {
		mutate(data)
	}
	ctx, assets := templatetest.Context(t)
	var pages []string
	res, err := template.Render(context.Background(), template.Request{Template: tpl, Data: data, Context: ctx, Assets: assets,
		Output: "pdf", Mode: template.ModePreview}, svgCompiler{Runner: runner, svgs: &pages}, template.Options{Timeout: 120 * time.Second})
	if err != nil {
		t.Fatalf("render dashboard: %v", err)
	}
	return res.Content, pages
}

// ---------------------------------------------------------------- PDF reading

type pdfFile struct{ obj map[int][]byte }

var objRe = regexp.MustCompile(`(?s)(?:^|\n)(\d+) 0 obj\n(.*?)endobj`)

func readPDF(t *testing.T, b []byte) pdfFile {
	t.Helper()
	f := pdfFile{obj: map[int][]byte{}}
	for _, m := range objRe.FindAllSubmatch(b, -1) {
		n, _ := strconv.Atoi(string(m[1]))
		f.obj[n] = m[2]
	}
	if len(f.obj) == 0 {
		t.Fatal("no PDF object found")
	}
	return f
}

var streamRe = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)

func (f pdfFile) stream(t *testing.T, n int) []byte {
	t.Helper()
	m := streamRe.FindSubmatch(f.obj[n])
	if m == nil {
		t.Fatalf("object %d has no stream", n)
	}
	r, err := zlib.NewReader(bytes.NewReader(m[1]))
	if err != nil {
		t.Fatalf("object %d: %v", n, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("object %d: %v", n, err)
	}
	return out
}

var refRe = regexp.MustCompile(`/(\w+)\s+(\d+) 0 R`)

func ref(dict []byte, key string) int {
	for _, m := range refRe.FindAllSubmatch(dict, -1) {
		if string(m[1]) == key {
			n, _ := strconv.Atoi(string(m[2]))
			return n
		}
	}
	return 0
}

// pageObjects lists the page objects in document order.
func (f pdfFile) pageObjects(t *testing.T) []int {
	t.Helper()
	var kids []int
	for _, body := range f.obj {
		if m := regexp.MustCompile(`/Type/Pages.*?/Kids\[([^\]]*)\]`).FindSubmatch(body); m != nil {
			for _, k := range regexp.MustCompile(`(\d+) 0 R`).FindAllSubmatch(m[1], -1) {
				n, _ := strconv.Atoi(string(k[1]))
				kids = append(kids, n)
			}
		}
	}
	if len(kids) == 0 {
		t.Fatal("no /Pages Kids")
	}
	return kids
}

var (
	bfcharRe  = regexp.MustCompile(`<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>`)
	bfrangeRe = regexp.MustCompile(`<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>`)
)

func utf16Hex(h string) string {
	var u []uint16
	for i := 0; i+4 <= len(h); i += 4 {
		v, _ := strconv.ParseUint(h[i:i+4], 16, 16)
		u = append(u, uint16(v))
	}
	return string(utf16.Decode(u))
}

// toUnicode parses the bfchar and bfrange sections of a ToUnicode CMap.
func toUnicode(cmap string) map[uint16]string {
	out := map[uint16]string{}
	for _, sec := range regexp.MustCompile(`(?s)beginbfchar(.*?)endbfchar`).FindAllStringSubmatch(cmap, -1) {
		for _, m := range bfcharRe.FindAllStringSubmatch(sec[1], -1) {
			c, _ := strconv.ParseUint(m[1], 16, 16)
			out[uint16(c)] = utf16Hex(m[2])
		}
	}
	for _, sec := range regexp.MustCompile(`(?s)beginbfrange(.*?)endbfrange`).FindAllStringSubmatch(cmap, -1) {
		for _, m := range bfrangeRe.FindAllStringSubmatch(sec[1], -1) {
			lo, _ := strconv.ParseUint(m[1], 16, 16)
			hi, _ := strconv.ParseUint(m[2], 16, 16)
			base := []rune(utf16Hex(m[3]))
			for c := lo; c <= hi && len(base) > 0; c++ {
				r := append([]rune(nil), base...)
				r[len(r)-1] += rune(c - lo)
				out[uint16(c)] = string(r)
			}
		}
	}
	return out
}

// pdfText returns the text of each page: one line per text object (BT..ET).
func pdfText(t *testing.T, doc []byte) [][]string {
	t.Helper()
	f := readPDF(t, doc)
	var pages [][]string
	for _, p := range f.pageObjects(t) {
		page := f.obj[p]
		fonts := map[string]map[uint16]string{}
		res := f.obj[ref(page, "Resources")]
		fontDict := regexp.MustCompile(`/Font<<(.*?)>>`).FindSubmatch(res)
		if fontDict == nil {
			t.Fatalf("page %d: no font resources", p)
		}
		for _, m := range refRe.FindAllSubmatch(fontDict[1], -1) {
			n, _ := strconv.Atoi(string(m[2]))
			fonts[string(m[1])] = toUnicode(string(f.stream(t, ref(f.obj[n], "ToUnicode"))))
		}
		pages = append(pages, textObjects(f.stream(t, ref(page, "Contents")), fonts))
	}
	return pages
}

var tfRe = regexp.MustCompile(`^\s*[0-9.]+\s+Tf`)

func textObjects(c []byte, fonts map[string]map[uint16]string) []string {
	var lines []string
	var cur string
	var line strings.Builder
	inBT := false
	for i := 0; i < len(c); {
		switch {
		case !inBT && bytes.HasPrefix(c[i:], []byte("BT")) && (i == 0 || c[i-1] == '\n' || c[i-1] == ' ' || c[i-1] == 'Q'):
			inBT = true
			i += 2
		case inBT && bytes.HasPrefix(c[i:], []byte("ET")):
			lines = append(lines, line.String())
			line.Reset()
			inBT = false
			i += 2
		case inBT && c[i] == '/':
			j := i + 1
			for j < len(c) && !strings.ContainsRune(" \n\r[(/<", rune(c[j])) {
				j++
			}
			if tfRe.Match(c[j:]) {
				cur = string(c[i+1 : j])
			}
			i = j
		case inBT && c[i] == '(':
			var raw []byte
			depth := 0
			i++
			for ; i < len(c); i++ {
				ch := c[i]
				if ch == '\\' {
					i++
					switch e := c[i]; {
					case e >= '0' && e <= '7':
						v := 0
						k := 0
						for k < 3 && i < len(c) && c[i] >= '0' && c[i] <= '7' {
							v = v*8 + int(c[i]-'0')
							i++
							k++
						}
						i--
						raw = append(raw, byte(v))
					case e == 'n':
						raw = append(raw, '\n')
					case e == 'r':
						raw = append(raw, '\r')
					case e == 't':
						raw = append(raw, '\t')
					case e == 'b':
						raw = append(raw, '\b')
					case e == 'f':
						raw = append(raw, '\f')
					default:
						raw = append(raw, e)
					}
					continue
				}
				if ch == '(' {
					depth++
				}
				if ch == ')' {
					if depth == 0 {
						break
					}
					depth--
				}
				raw = append(raw, ch)
			}
			i++
			for k := 0; k+1 < len(raw); k += 2 {
				code := uint16(raw[k])<<8 | uint16(raw[k+1])
				if s, ok := fonts[cur][code]; ok {
					line.WriteString(s)
				} else {
					line.WriteString("�")
				}
			}
		default:
			i++
		}
	}
	return lines
}

var colorRe = regexp.MustCompile(`([0-9.]+(?:\s+[0-9.]+){0,2})\s+(?:scn|SCN|sc|SC)\b`)

// pdfColors returns every colour set by the content streams, as upper-case hex.
func pdfColors(t *testing.T, doc []byte) map[string]int {
	t.Helper()
	f := readPDF(t, doc)
	used := map[string]int{}
	for _, p := range f.pageObjects(t) {
		for _, m := range colorRe.FindAllSubmatch(f.stream(t, ref(f.obj[p], "Contents")), -1) {
			fields := strings.Fields(string(m[1]))
			var v [3]float64
			for i, s := range fields {
				v[i], _ = strconv.ParseFloat(s, 64)
			}
			if len(fields) == 1 {
				v[1], v[2] = v[0], v[0]
			} else if len(fields) != 3 {
				used[fmt.Sprintf("odd(%s)", m[1])]++
				continue
			}
			used[fmt.Sprintf("#%02X%02X%02X", int(math.Round(v[0]*255)), int(math.Round(v[1]*255)), int(math.Round(v[2]*255)))]++
		}
	}
	return used
}

// ---------------------------------------------------------------- SVG reading

type shape struct {
	raw, fill, stroke, d string
	sw, x, y             float64
	xs, ys               string
}

var (
	pathRe      = regexp.MustCompile(`<path ([^>]*?)/>`)
	translateRe = regexp.MustCompile(`transform="translate\((-?[0-9.]+) (-?[0-9.]+)\)"`)
)

func attr(attrs, name string) string {
	m := regexp.MustCompile(`(?:^|\s)` + name + `="([^"]*)"`).FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	return m[1]
}

func parseShapes(svg string) []shape {
	var out []shape
	for _, m := range pathRe.FindAllStringSubmatch(svg, -1) {
		tr := translateRe.FindStringSubmatch(m[1])
		if tr == nil {
			continue // glyph outlines have no transform
		}
		s := shape{raw: m[0], fill: attr(m[1], "fill"), stroke: attr(m[1], "stroke"), d: attr(m[1], "d"), xs: tr[1], ys: tr[2]}
		s.sw, _ = strconv.ParseFloat(attr(m[1], "stroke-width"), 64)
		s.x, _ = strconv.ParseFloat(tr[1], 64)
		s.y, _ = strconv.ParseFloat(tr[2], 64)
		out = append(out, s)
	}
	return out
}

var barRe = regexp.MustCompile(`^M 0 0v ([0-9.]+)h ([0-9.]+)v -([0-9.]+)Z`)

func barHeight(s shape) (float64, bool) {
	m := barRe.FindStringSubmatch(s.d)
	if m == nil {
		return 0, false
	}
	h, _ := strconv.ParseFloat(m[1], 64)
	return h, true
}

// niceStep mirrors the Typst axis: 1, 2 or 5 times a power of ten.
func niceStep(v float64) float64 {
	p := math.Pow(10, math.Floor(math.Log10(v)))
	switch m := v / p; {
	case m <= 1:
		return p
	case m <= 2:
		return 2 * p
	case m <= 5:
		return 5 * p
	}
	return 10 * p
}

// axisMap anchors the checks on the drawn graduations: the grid lines (muted, 0.3 pt)
// are read from the SVG, their number and values come from an independent computation
// of the axis from the data, and the lowest and highest lines give value -> y.
func axisMap(svg string, series [][]float64, muted, axis string) (yOf func(float64) float64, top, bottom, gx, pw float64, err error) {
	vmin, vmax := 0.0, 0.0
	for _, row := range series {
		for _, v := range row {
			vmin, vmax = math.Min(vmin, v), math.Max(vmax, v)
		}
	}
	lo, hi := vmin, vmax
	if hi == lo {
		hi = 1
	}
	step := math.Max(niceStep(math.Max(hi-lo, 0.01)/5), 0.001)
	lo, hi = math.Floor(lo/step)*step, math.Ceil(hi/step)*step
	want := int(math.Round((hi-lo)/step)) + 1
	var ys []float64
	zeroAxes := 0
	var zeroY float64
	for _, s := range parseShapes(svg) {
		if s.stroke == muted && s.sw == 0.3 {
			ys = append(ys, s.y)
			dx, _ := segDelta(s.d)
			gx, pw = s.x, dx // the grid lines run over the plot area: its left edge and width
		}
		if s.stroke == axis && s.sw == 0.6 {
			zeroAxes++
			zeroY = s.y
		}
	}
	if len(ys) != want {
		return nil, 0, 0, 0, 0, fmt.Errorf("%d grid lines drawn, the data needs %d (%g to %g by %g)", len(ys), want, lo, hi, step)
	}
	sort.Float64s(ys)
	top, bottom = ys[0], ys[len(ys)-1]
	for i := 1; i < len(ys); i++ {
		if math.Abs((ys[i]-ys[i-1])-(bottom-top)/float64(want-1)) > 0.01 {
			return nil, 0, 0, 0, 0, fmt.Errorf("grid lines are not evenly spaced")
		}
	}
	yOf = func(v float64) float64 { return bottom - (v-lo)/(hi-lo)*(bottom-top) }
	if zeroAxes != 1 || math.Abs(zeroY-yOf(0)) > 0.01 {
		return nil, 0, 0, 0, 0, fmt.Errorf("the zero axis (%d drawn, y=%g) is not on the graduation of 0 (y=%g)", zeroAxes, zeroY, yOf(0))
	}
	return yOf, top, bottom, gx, pw, nil
}

// checkBars verifies that every bar starts on the graduation of 0 and ends on the
// graduation of its value (0.5% of the plot height), above the axis for positive
// values and below for negative ones, and that at least one bar is not empty.
func checkBars(svg string, series [][]float64, colors []string, muted, axis string) error {
	yOf, top, bottom, gx, pw, err := axisMap(svg, series, muted, axis)
	if err != nil {
		return err
	}
	tol := 0.005 * (bottom - top)
	slot := pw / float64(len(series[0]))
	nonzero := 0
	shapes := parseShapes(svg)
	for si, vals := range series {
		var bars []shape
		for _, s := range shapes {
			h, ok := barHeight(s)
			if ok && s.fill == colors[si] && s.y >= top-1 && s.y+h <= bottom+1 {
				bars = append(bars, s)
			}
		}
		sort.Slice(bars, func(i, j int) bool { return bars[i].x < bars[j].x })
		if len(bars) != len(vals) {
			return fmt.Errorf("series %d: %d bars for %d values", si, len(bars), len(vals))
		}
		for i, b := range bars {
			h, _ := barHeight(b)
			v := vals[i]
			// horizontal position: the groups are centred on the category slots, in order,
			// the series side by side with the same bar width
			bm := barRe.FindStringSubmatch(b.d)
			bw, _ := strconv.ParseFloat(bm[2], 64)
			groupCentre := b.x + bw/2 - (float64(si)-float64(len(series)-1)/2)*bw
			if want := gx + slot*(float64(i)+0.5); math.Abs(groupCentre-want) > 0.05 {
				return fmt.Errorf("series %d bar %d: group centred at x=%g, the category slot is centred at %g", si, i, groupCentre, want)
			}
			wantTop, wantBottom := math.Min(yOf(v), yOf(0)), math.Max(yOf(v), yOf(0))
			if math.Abs(b.y-wantTop) > tol || math.Abs(b.y+h-wantBottom) > tol {
				return fmt.Errorf("series %d bar %d: drawn from %g to %g, the graduations of 0 and %g give %g to %g", si, i, b.y, b.y+h, v, wantTop, wantBottom)
			}
			if v != 0 {
				nonzero++
			}
		}
	}
	if nonzero == 0 {
		return fmt.Errorf("no bar has a height")
	}
	return nil
}

var segRe = regexp.MustCompile(`^M 0 0(?:l (-?[0-9.]+) (-?[0-9.]+)|h (-?[0-9.]+))`)

// segDelta returns the displacement of a line segment path ("l dx dy" or "h dx").
func segDelta(d string) (dx, dy float64) {
	m := segRe.FindStringSubmatch(d)
	if m[3] != "" {
		dx, _ = strconv.ParseFloat(m[3], 64)
		return dx, 0
	}
	dx, _ = strconv.ParseFloat(m[1], 64)
	dy, _ = strconv.ParseFloat(m[2], 64)
	return dx, dy
}

// checkLines verifies, for every series, that the segments chain exactly (each one
// starts where the previous ends, in x and in y) and that every point sits on the
// graduation of its value (0.5% of the plot height).
func checkLines(svg string, series [][]float64, colors []string, muted, axis string) error {
	yOf, top, bottom, gx, pw, err := axisMap(svg, series, muted, axis)
	if err != nil {
		return err
	}
	tol := 0.005 * (bottom - top)
	slot := pw / float64(len(series[0]))
	shapes := parseShapes(svg)
	moving := false
	for si, vals := range series {
		var segs []shape
		for _, s := range shapes {
			if s.stroke == colors[si] && s.sw == 1.6 && segRe.MatchString(s.d) {
				segs = append(segs, s)
			}
		}
		sort.Slice(segs, func(i, j int) bool { return segs[i].x < segs[j].x })
		if len(segs) != len(vals)-1 {
			return fmt.Errorf("series %d: %d segments for %d values", si, len(segs), len(vals))
		}
		ys := make([]float64, 0, len(vals))
		for i, sg := range segs {
			dx, dy := segDelta(sg.d)
			if i+1 < len(segs) && (math.Abs(sg.x+dx-segs[i+1].x) > 0.01 || math.Abs(sg.y+dy-segs[i+1].y) > 0.01) {
				return fmt.Errorf("series %d: segment %d ends at (%g, %g) but segment %d starts at (%g, %g)", si, i, sg.x+dx, sg.y+dy, i+1, segs[i+1].x, segs[i+1].y)
			}
			ys = append(ys, sg.y)
		}
		last := segs[len(segs)-1]
		lastDx, dy := segDelta(last.d)
		ys = append(ys, last.y+dy)
		// abscissae: the points sit on the centres of the category slots, in order
		for i := range vals {
			x := last.x + lastDx
			if i < len(segs) {
				x = segs[i].x
			}
			if want := gx + slot*(float64(i)+0.5); math.Abs(x-want) > 0.05 {
				return fmt.Errorf("series %d point %d: drawn at x=%g, the category slot is centred at %g", si, i, x, want)
			}
		}
		for i, v := range vals {
			if math.Abs(ys[i]-yOf(v)) > tol {
				return fmt.Errorf("series %d point %d: drawn at y=%g, the graduations give %g for %g", si, i, ys[i], yOf(v), v)
			}
			if i > 0 && vals[i] != vals[i-1] {
				moving = true
			}
		}
	}
	if !moving {
		return fmt.Errorf("the values do not vary")
	}
	return nil
}

var pathTokRe = regexp.MustCompile(`([MmLlHhVvCcZz])|(-?[0-9]*\.?[0-9]+(?:e-?[0-9]+)?)`)

// pathBBox returns the bounding box of an SVG path made of M m L l H h V v C c Z z
// (control points included), relative to the path origin.
func pathBBox(d string) (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	var cx, cy float64
	var cmd byte
	var nums []float64
	add := func(x, y float64) {
		x0, y0, x1, y1 = math.Min(x0, x), math.Min(y0, y), math.Max(x1, x), math.Max(y1, y)
	}
	arity := map[byte]int{'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6}
	flush := func() {
		k := arity[cmd&^0x20]
		rel := cmd >= 'a'
		for k > 0 && len(nums) >= k {
			a := nums[:k]
			nums = nums[k:]
			switch cmd &^ 0x20 {
			case 'M', 'L':
				nx, ny := a[0], a[1]
				if rel {
					nx, ny = cx+nx, cy+ny
				}
				cx, cy = nx, ny
				add(cx, cy)
			case 'H':
				if rel {
					cx += a[0]
				} else {
					cx = a[0]
				}
				add(cx, cy)
			case 'V':
				if rel {
					cy += a[0]
				} else {
					cy = a[0]
				}
				add(cx, cy)
			case 'C':
				for i := 0; i < 6; i += 2 {
					px, py := a[i], a[i+1]
					if rel {
						px, py = cx+px, cy+py
					}
					add(px, py)
					if i == 4 {
						cx, cy = px, py
					}
				}
			}
		}
	}
	for _, m := range pathTokRe.FindAllStringSubmatch(d, -1) {
		if m[1] != "" {
			flush()
			cmd, nums = m[1][0], nil
			continue
		}
		v, _ := strconv.ParseFloat(m[2], 64)
		nums = append(nums, v)
		flush()
	}
	return
}

// ---------------------------------------------------------------- data helpers

func seriesValues(t *testing.T, chart any) (vals [][]float64) {
	t.Helper()
	for _, s := range chart.(map[string]any)["series"].([]any) {
		var row []float64
		for _, v := range s.(map[string]any)["values"].([]any) {
			row = append(row, v.(float64))
		}
		vals = append(vals, row)
	}
	return vals
}

func zeroSeries(chart any) {
	for _, s := range chart.(map[string]any)["series"].([]any) {
		vs := s.(map[string]any)["values"].([]any)
		for i := range vs {
			vs[i] = float64(0)
		}
	}
}

// brandHex returns the charter colours as the Typst palette uses them (series
// order: primary, accent, secondary, muted, text), lower-case hex.
func brandColors(t *testing.T) (series []string, text string, allowed map[string]bool) {
	t.Helper()
	ctx, _ := templatetest.Context(t)
	cols := ctx.Brand["colors"].(map[string]any)
	hex := func(k string) string { return strings.ToLower(cols[k].(string)) }
	allowed = map[string]bool{"#FFFFFF": true}
	for _, v := range cols {
		allowed[strings.ToUpper(v.(string))] = true
	}
	return []string{hex("primary"), hex("accent"), hex("secondary"), hex("muted"), hex("text")}, hex("text"), allowed
}

// ---------------------------------------------------------------- tests

func TestChartsDashboardIsReproducibleAndFrench(t *testing.T) {
	runner := typsttest.Runner(t)
	a, svgA := dashboard(t, runner, nil)
	b, svgB := dashboard(t, runner, nil)
	if !bytes.HasPrefix(a, []byte("%PDF-")) {
		t.Fatalf("not a PDF")
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("PDF not reproducible: %d vs %d bytes", len(a), len(b))
	}
	if len(svgA) != 6 || strings.Join(svgA, "") != strings.Join(svgB, "") {
		t.Fatalf("SVG pages: %d, identical=%v", len(svgA), strings.Join(svgA, "") == strings.Join(svgB, ""))
	}
	pages := pdfText(t, a)
	if len(pages) != 6 {
		t.Fatalf("%d pages, want 6", len(pages))
	}
	flat := func(p []string) string { return strings.Join(p, "\n") }
	squash := func(s string) string {
		return strings.Join(strings.Fields(strings.NewReplacer("\u202f", "<T>").Replace(s)), "")
	}
	want := [][]string{
		{"Tableau de bord Activité 2026", "Résultat mensuel", "janv.", "févr.", "avr.", "Chiffre d'affaires (k€)", "Résultat (k€)",
			"1\u202f250,5", "1\u202f380", "1\u202f904,25", "\u2212150,5", "\u221280", "220", "520", "2\u202f000", "\u2212500", "1\u202f500"},
		{"Tendance", "Visites", "Commandes", "20\u202f000", "5\u202f000", "15\u202f000"},
		{"Répartition du chiffre d'affaires", "Conseil (38\u202f%)", "Formation (24\u202f%)", "Licences (17\u202f%)", "Intégration (13\u202f%)", "Support (8\u202f%)", "38\u202f%", "24\u202f%"},
		{"Plan de projet", "Cadrage du besoin", "Ateliers utilisateurs", "Interfaces principales", "Recette métier", "Cadrage", "Construction", "Recette", "S1", "S12"},
		{"Organisation", "Hélène Marchand", "Julien Bœuf", "Directrice générale", "Directrice commerciale", "Maël Roche"},
		{"Traitement d'une demande", "Demande reçue", "Qualification", "Dossier complet ?", "Validation interne", "Signature client", "Clôture"},
	}
	for i, list := range want {
		text := flat(pages[i])
		for _, w := range list {
			if !strings.Contains(squash(text), squash(w)) {
				t.Errorf("page %d: %q not extractable; text: %q", i+1, w, text)
			}
		}
	}
	// thin non-breaking space and decimal comma are really in the text, not a dot
	if all := flat(pages[0]); strings.Contains(all, "1250.5") || strings.Contains(all, "1904.25") || strings.Contains(all, "-150") {
		t.Errorf("numbers are not formatted in French: %q", all)
	}
}

func TestChartsUseOnlyCharterColours(t *testing.T) {
	runner := typsttest.Runner(t)
	pdf, _ := dashboard(t, runner, nil)
	series, _, allowed := brandColors(t)
	used := pdfColors(t, pdf)
	if len(used) == 0 {
		t.Fatal("no colour found in the PDF content streams")
	}
	if bad := offPalette(used, allowed); len(bad) > 0 {
		t.Errorf("colours outside the charter (plus white): %v; used: %v", bad, used)
	}
	for _, s := range series {
		if used[strings.ToUpper(s)] == 0 {
			t.Errorf("series colour %s never used: %v", s, used)
		}
	}
	// seeded fault: the control must see a colour outside the palette
	used["#FF0000"] = 1
	if bad := offPalette(used, allowed); len(bad) != 1 || bad[0] != "#FF0000" {
		t.Fatalf("the palette control missed a red: %v", bad)
	}
}

func offPalette(used map[string]int, allowed map[string]bool) (bad []string) {
	for hex := range used {
		if !allowed[hex] {
			bad = append(bad, hex)
		}
	}
	sort.Strings(bad)
	return bad
}

func charterHex(t *testing.T, key string) string {
	t.Helper()
	ctx, _ := templatetest.Context(t)
	return strings.ToLower(ctx.Brand["colors"].(map[string]any)[key].(string))
}

// shiftAll moves, by dy points, every shape accepted by `pick` (the data marks of one
// page); the axes and grid lines stay where they are.
func shiftAll(svg string, dy float64, pick func(shape) bool) string {
	for _, s := range parseShapes(svg) {
		if pick(s) {
			moved := strings.Replace(s.raw, "translate("+s.xs+" "+s.ys+")", "translate("+s.xs+" "+strconv.FormatFloat(s.y+dy, 'f', 6, 64)+")", 1)
			svg = strings.Replace(svg, s.raw, moved, 1)
		}
	}
	return svg
}

func TestChartsGeometryMatchesTheValues(t *testing.T) {
	runner := typsttest.Runner(t)
	_, svgs := dashboard(t, runner, nil)
	tpl := templatetest.Load(t, "valid/dashboard")
	data := templatetest.Data(t, tpl, "default").(map[string]any)
	colors, text, _ := brandColors(t)
	muted := charterHex(t, "muted")
	bars, lines := seriesValues(t, data["bar"]), seriesValues(t, data["line"])
	if err := checkBars(svgs[0], bars, colors, muted, text); err != nil {
		t.Fatalf("bars: %v", err)
	}
	if err := checkLines(svgs[1], lines, colors, muted, text); err != nil {
		t.Fatalf("lines: %v", err)
	}

	t.Run("fault: all values multiplied by zero", func(t *testing.T) {
		_, zsvgs := dashboard(t, runner, func(d map[string]any) { zeroSeries(d["bar"]); zeroSeries(d["line"]) })
		if err := checkBars(zsvgs[0], bars, colors, muted, text); err == nil {
			t.Fatal("zeroed bars were accepted")
		}
		if err := checkLines(zsvgs[1], lines, colors, muted, text); err == nil {
			t.Fatal("zeroed lines were accepted")
		}
		// zero heights are not enough for the "no empty chart" rule either
		zeros := [][]float64{make([]float64, 6), make([]float64, 6)}
		if err := checkBars(zsvgs[0], zeros, colors, muted, text); err == nil {
			t.Fatal("a chart without any bar height was accepted")
		}
	})
	t.Run("fault: every line shifted by 30 pt", func(t *testing.T) {
		bad := shiftAll(svgs[1], 30, func(s shape) bool { return s.sw == 1.6 && s.stroke != "" })
		if err := checkLines(bad, lines, colors, muted, text); err == nil {
			t.Fatal("lines shifted as a whole were accepted")
		}
	})
	t.Run("fault: line segments squashed horizontally", func(t *testing.T) {
		bad := svgs[1]
		for _, s := range parseShapes(svgs[1]) {
			if s.sw == 1.6 && s.stroke != "" {
				_, dy := segDelta(s.d)
				squashed := strings.Replace(strings.Replace(s.raw, "translate("+s.xs+" ", "translate(100 ", 1), s.d, fmt.Sprintf("M 0 0l 0 %s", strconv.FormatFloat(dy, 'f', 6, 64)), 1)
				bad = strings.Replace(bad, s.raw, squashed, 1)
			}
		}
		if err := checkLines(bad, lines, colors, muted, text); err == nil {
			t.Fatal("squashed segments were accepted")
		}
	})
	t.Run("fault: bars squashed horizontally", func(t *testing.T) {
		bad := svgs[0]
		for _, s := range parseShapes(svgs[0]) {
			if _, ok := barHeight(s); ok && s.y > 25 && (s.fill == colors[0] || s.fill == colors[1]) {
				bad = strings.Replace(bad, s.raw, strings.Replace(s.raw, "translate("+s.xs+" ", "translate(100 ", 1), 1)
			}
		}
		if err := checkBars(bad, bars, colors, muted, text); err == nil {
			t.Fatal("squashed bars were accepted")
		}
	})
	t.Run("fault: every bar shifted by 30 pt", func(t *testing.T) {
		bad := shiftAll(svgs[0], 30, func(s shape) bool {
			_, ok := barHeight(s)
			return ok && s.y > 25 && (s.fill == colors[0] || s.fill == colors[1])
		})
		if err := checkBars(bad, bars, colors, muted, text); err == nil {
			t.Fatal("bars shifted as a whole were accepted")
		}
	})
	t.Run("fault: altered bar height", func(t *testing.T) {
		for _, s := range parseShapes(svgs[0]) {
			if h, ok := barHeight(s); ok && s.fill == colors[0] && h > 50 {
				m := barRe.FindStringSubmatch(s.d)
				bad := strings.Replace(svgs[0], s.raw, strings.Replace(s.raw, "v "+m[1]+"h", "v "+strconv.FormatFloat(h*1.02, 'f', 6, 64)+"h", 1), 1)
				bad = strings.Replace(bad, "v -"+m[3]+"Z", "v -"+strconv.FormatFloat(h*1.02, 'f', 6, 64)+"Z", 1)
				if err := checkBars(bad, bars, colors, muted, text); err == nil {
					t.Fatal("a bar 2% too tall was accepted")
				}
				return
			}
		}
		t.Fatal("no bar found to alter")
	})
	t.Run("fault: flipped sign", func(t *testing.T) {
		flipped := seriesValues(t, data["bar"])
		flipped[1][1] = -flipped[1][1] // the data says +150.5, the drawing is below the axis
		if err := checkBars(svgs[0], flipped, colors, muted, text); err == nil {
			t.Fatal("a sign error was accepted")
		}
	})
	t.Run("fault: first segment end moved by 30 pt", func(t *testing.T) {
		for _, s := range parseShapes(svgs[1]) {
			if s.stroke == colors[0] && s.sw == 1.6 && s.x < 100 {
				dx, dy := segDelta(s.d)
				bad := strings.Replace(s.raw, s.d, fmt.Sprintf("M 0 0l %s %s", strconv.FormatFloat(dx, 'f', 6, 64), strconv.FormatFloat(dy+30, 'f', 6, 64)), 1)
				if err := checkLines(strings.Replace(svgs[1], s.raw, bad, 1), lines, colors, muted, text); err == nil {
					t.Fatal("a first segment ending 30 pt off was accepted")
				}
				return
			}
		}
		t.Fatal("no first segment found")
	})
	t.Run("fault: moved line point", func(t *testing.T) {
		for _, s := range parseShapes(svgs[1]) {
			if s.stroke == colors[0] && s.sw == 1.6 {
				moved := strings.Replace(s.raw, "translate("+s.xs+" "+s.ys+")", "translate("+s.xs+" "+strconv.FormatFloat(s.y+3, 'f', 6, 64)+")", 1)
				if err := checkLines(strings.Replace(svgs[1], s.raw, moved, 1), lines, colors, muted, text); err == nil {
					t.Fatal("a point moved by 3 pt was accepted")
				}
				return
			}
		}
		t.Fatal("no line segment found")
	})
}

// chartInline renders one chart through a throwaway template that accepts any data,
// so that only the fragment checks (not the input schema) can refuse it.
func chartInline(t *testing.T, runner typst.Runner, fragment string, chart any) error {
	t.Helper()
	manifest := `{"schema":"agentmaurice.template/v1","key":"chart-bounds","version":"1.0.0","kind":"document","profile":"document",
	"source":{"engine":"typst","entry":"main.typ"},"outputs":["pdf"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":["fragments/charts.typ"]}`
	main := "#import \"/fragments/charts.typ\": bar-chart, line-chart, pie-chart, gantt-chart, org-chart, process-flow\n" +
		"#" + fragment + "([{{ .data.chart | json }}])\n"
	tpl := template.Template{Manifest: json.RawMessage(manifest), Files: map[string]string{
		"main.typ": main, "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"}}
	ctx, assets := templatetest.Context(t)
	_, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{"chart": chart},
		Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{Timeout: 120 * time.Second})
	return err
}

func repeat(n int, v any) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func obj(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func nodes(n int, parent func(i int) string) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = obj("id", fmt.Sprintf("n%d", i), "name", fmt.Sprintf("Node %d", i), "parent", parent(i))
	}
	return out
}

func TestChartsOutOfBoundsAreRefused(t *testing.T) {
	runner := typsttest.Runner(t)
	ok := obj("type", "bar", "categories", []any{"a", "b"}, "series", []any{obj("name", "s", "values", []any{1, 2})})
	if err := chartInline(t, runner, "bar-chart", ok); err != nil {
		t.Fatalf("a valid chart must render: %v", err)
	}
	// the smallest accepted magnitudes render
	for _, tc := range []struct {
		fragment string
		chart    any
	}{
		{"bar-chart", obj("type", "bar", "categories", []any{"a", "b", "c"}, "series", []any{obj("name", "s", "values", []any{0.01, -0.01, 0})})},
		{"line-chart", obj("type", "line", "categories", []any{"a", "b"}, "series", []any{obj("name", "s", "values", []any{0.01, 0.02})})},
		{"bar-chart", obj("type", "bar", "categories", []any{"a"}, "series", []any{obj("name", "s", "values", []any{0})})},
		{"pie-chart", obj("type", "pie", "parts", []any{obj("name", "a", "value", 0.01), obj("name", "b", "value", 0)})},
	} {
		if err := chartInline(t, runner, tc.fragment, tc.chart); err != nil {
			t.Fatalf("smallest values must render (%s): %v", tc.fragment, err)
		}
	}
	// the bounds themselves are accepted
	maxBar := obj("type", "bar", "categories", repeat(24, "c"), "series", repeat(5, obj("name", "s", "values", repeat(24, -1000000000000))))
	if err := chartInline(t, runner, "bar-chart", maxBar); err != nil {
		t.Fatalf("the maximum bar chart must render: %v", err)
	}
	chain := func(i int) string {
		if i == 0 {
			return ""
		}
		return fmt.Sprintf("n%d", i-1)
	}
	star := func(i int) string {
		if i == 0 {
			return ""
		}
		return "n0"
	}
	cases := []struct {
		name, fragment string
		chart          any
		want           string
	}{
		{"bar 25 categories", "bar-chart", obj("type", "bar", "categories", repeat(25, "c"), "series", []any{obj("name", "s", "values", repeat(25, 1))}), "chart/v1: bar-chart: categories must have between 1 and 24"},
		{"bar 6 series", "bar-chart", obj("type", "bar", "categories", []any{"a"}, "series", repeat(6, obj("name", "s", "values", []any{1}))), "chart/v1: bar-chart: series must have between 1 and 5"},
		{"bar value count", "bar-chart", obj("type", "bar", "categories", []any{"a", "b"}, "series", []any{obj("name", "s", "values", []any{1})}), "chart/v1: bar-chart: series `s` must have exactly 2 values"},
		{"bar huge value", "bar-chart", obj("type", "bar", "categories", []any{"a"}, "series", []any{obj("name", "s", "values", []any{2e12})}), "chart/v1: bar-chart: a value of series `s` is beyond"},
		{"bar not a number", "bar-chart", obj("type", "bar", "categories", []any{"a"}, "series", []any{obj("name", "s", "values", []any{"1"})}), "chart/v1: bar-chart: a value of series `s` must be a number"},
		{"bar underflowing value", "bar-chart", obj("type", "bar", "categories", []any{"a"}, "series", []any{obj("name", "s", "values", []any{5e-324})}), "chart/v1: bar-chart: a value of series `s` must be 0 or at least 0.01"},
		{"bar tiny values", "bar-chart", obj("type", "bar", "categories", []any{"a", "b"}, "series", []any{obj("name", "s", "values", []any{0.00000001, 0.00000002})}), "chart/v1: bar-chart: a value of series `s` must be 0 or at least 0.01"},
		{"line underflowing value", "line-chart", obj("type", "line", "categories", []any{"a", "b"}, "series", []any{obj("name", "s", "values", []any{5e-324, 0})}), "chart/v1: line-chart: a value of series `s` must be 0 or at least 0.01"},
		{"pie tiny part", "pie-chart", obj("type", "pie", "parts", []any{obj("name", "a", "value", 0.001), obj("name", "b", "value", 3)}), "chart/v1: pie-chart: value of part `a` must be 0 or at least 0.01"},
		{"gantt tiny duration", "gantt-chart", obj("type", "gantt", "weeks", 104, "phases", []any{"p"}, "tasks", []any{obj("name", "t", "start", 103.999, "duration", 0.001, "phase", 0)}), "chart/v1: gantt-chart: duration of task `t` must be 0 or at least 0.01"},
		{"bar wrong type", "bar-chart", obj("type", "line", "categories", []any{"a"}, "series", []any{obj("name", "s", "values", []any{1})}), "chart/v1: bar-chart: type must be \"bar\""},
		{"bar long label", "bar-chart", obj("type", "bar", "categories", []any{strings.Repeat("x", 61)}, "series", []any{obj("name", "s", "values", []any{1})}), "chart/v1: bar-chart: a category is longer than 60"},
		{"line missing categories", "line-chart", obj("type", "line", "series", []any{}), "chart/v1: line-chart: `categories` is missing"},
		{"line empty series name", "line-chart", obj("type", "line", "categories", []any{"a"}, "series", []any{obj("name", "", "values", []any{1})}), "chart/v1: line-chart: series name must be a non-empty string"},
		{"pie zero sum", "pie-chart", obj("type", "pie", "parts", []any{obj("name", "a", "value", 0)}), "chart/v1: pie-chart: the sum of the values must be greater than zero"},
		{"pie negative", "pie-chart", obj("type", "pie", "parts", []any{obj("name", "a", "value", -1), obj("name", "b", "value", 3)}), "chart/v1: pie-chart: value of part `a` must not be negative"},
		{"pie 11 parts", "pie-chart", obj("type", "pie", "parts", repeat(11, obj("name", "a", "value", 1))), "chart/v1: pie-chart: parts must have between 1 and 10"},
		{"gantt overflow", "gantt-chart", obj("type", "gantt", "weeks", 4, "phases", []any{"p"}, "tasks", []any{obj("name", "t", "start", 3, "duration", 2, "phase", 0)}), "chart/v1: gantt-chart: task `t` ends after week 4"},
		{"gantt 26 tasks", "gantt-chart", obj("type", "gantt", "weeks", 4, "phases", []any{"p"}, "tasks", repeat(26, obj("name", "t", "start", 0, "duration", 1, "phase", 0))), "chart/v1: gantt-chart: tasks must have between 1 and 25"},
		{"gantt phase", "gantt-chart", obj("type", "gantt", "weeks", 4, "phases", []any{"p"}, "tasks", []any{obj("name", "t", "start", 0, "duration", 1, "phase", 1)}), "chart/v1: gantt-chart: phase of task `t` must be between 0 and 0"},
		{"gantt weeks 105", "gantt-chart", obj("type", "gantt", "weeks", 105, "phases", []any{"p"}, "tasks", []any{obj("name", "t", "start", 0, "duration", 1, "phase", 0)}), "chart/v1: gantt-chart: weeks must be between 1 and 104"},
		{"org two roots", "org-chart", obj("type", "org", "nodes", nodes(2, func(int) string { return "" })), "chart/v1: org-chart: exactly one node must have an empty parent"},
		{"org missing parent", "org-chart", obj("type", "org", "nodes", nodes(2, func(i int) string {
			if i == 0 {
				return ""
			}
			return "ghost"
		})), "chart/v1: org-chart: parent `ghost` of node `n1` does not exist"},
		{"org cycle", "org-chart", obj("type", "org", "nodes", []any{
			obj("id", "r", "name", "R", "parent", ""), obj("id", "a", "name", "A", "parent", "b"), obj("id", "b", "name", "B", "parent", "a")}), "chart/v1: org-chart: every node must descend from the root"},
		{"org duplicate id", "org-chart", obj("type", "org", "nodes", []any{obj("id", "r", "name", "R", "parent", ""), obj("id", "r", "name", "R2", "parent", "r")}), "chart/v1: org-chart: duplicate node id `r`"},
		{"org 31 nodes", "org-chart", obj("type", "org", "nodes", nodes(31, star)), "chart/v1: org-chart: nodes must have between 1 and 30"},
		{"org 9 leaves", "org-chart", obj("type", "org", "nodes", nodes(10, star)), "chart/v1: org-chart: 9 leaves is above 8"},
		{"org depth 7", "org-chart", obj("type", "org", "nodes", nodes(8, chain)), "chart/v1: org-chart: depth 7 is above 6"},
		{"process 13 steps", "process-flow", obj("type", "process", "steps", repeat(13, obj("label", "s", "kind", "step"))), "chart/v1: process-flow: steps must have between 1 and 12"},
		{"process kind", "process-flow", obj("type", "process", "steps", []any{obj("label", "s", "kind", "loop")}), "chart/v1: process-flow: step kind must be start, step, decision or end"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := chartInline(t, runner, tc.fragment, tc.chart)
			if err == nil {
				t.Fatal("out-of-bounds data rendered")
			}
			var terr *template.Error
			if !asTemplateError(err, &terr) || terr.Code != template.CodeCompileFailed || !strings.Contains(terr.Message, "chart/v1: ") {
				t.Fatalf("want a compile error carrying \"chart/v1: \", got %v", err)
			}
			if !strings.Contains(terr.Message, tc.want) {
				t.Fatalf("message %q does not contain %q", terr.Message, tc.want)
			}
		})
	}
}

func asTemplateError(err error, target **template.Error) bool {
	for err != nil {
		if e, ok := err.(*template.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// chartInlineSVG is chartInline that also returns the SVG pages.
func chartInlineSVG(t *testing.T, runner typst.Runner, fragment string, chart any) []string {
	t.Helper()
	manifest := `{"schema":"agentmaurice.template/v1","key":"chart-svg","version":"1.0.0","kind":"document","profile":"document",
	"source":{"engine":"typst","entry":"main.typ"},"outputs":["pdf"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":["fragments/charts.typ"]}`
	main := "#import \"/fragments/charts.typ\": bar-chart, line-chart, pie-chart, gantt-chart, org-chart, process-flow\n#" + fragment + "([{{ .data.chart | json }}])\n"
	tpl := template.Template{Manifest: json.RawMessage(manifest), Files: map[string]string{
		"main.typ": main, "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"}}
	ctx, assets := templatetest.Context(t)
	var pages []string
	if _, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{"chart": chart},
		Context: ctx, Assets: assets, Output: "pdf"}, svgCompiler{Runner: runner, svgs: &pages}, template.Options{Timeout: 120 * time.Second}); err != nil {
		t.Fatal(err)
	}
	return pages
}

// An accepted Gantt task is drawn from its real start to its real end, inside the grid.
// A task shorter than the 0.2 mm marker starts at its real start and only moves back to
// stay in the grid.
func TestGanttBarsStayInsideTheGrid(t *testing.T) {
	runner := typsttest.Runner(t)
	colors, _, _ := brandColors(t)
	background := charterHex(t, "background")
	type tk struct{ start, dur float64 }
	const minMarker = 0.2 * 72 / 25.4 // 0.2 mm in pt
	const tol = 0.05 * 72 / 25.4      // 0.05 mm in pt
	for _, tc := range []struct {
		name  string
		weeks int
		tasks []tk
	}{
		{"end of a 104-week grid", 104, []tk{{103.99, 0.01}}},
		{"smallest duration at the start", 104, []tk{{0, 0.01}}},
		{"ends exactly at the last week", 4, []tk{{3.5, 0.5}, {0, 4}}},
		{"ordinary tasks", 26, []tk{{0, 2}, {1.5, 3.25}, {10, 0.5}, {24, 2}, {13, 0.01}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tasks []any
			for i, k := range tc.tasks {
				tasks = append(tasks, obj("name", fmt.Sprintf("t%d", i), "start", k.start, "duration", k.dur, "phase", 0))
			}
			svgs := chartInlineSVG(t, runner, "gantt-chart", obj("type", "gantt", "weeks", tc.weeks, "phases", []any{"p"}, "tasks", tasks))
			shapes := parseShapes(svgs[0])
			var grid *shape
			var gw float64
			for i, s := range shapes {
				if h, ok := barHeight(s); ok && s.fill == background && h > 1 {
					m := barRe.FindStringSubmatch(s.d)
					w, _ := strconv.ParseFloat(m[2], 64)
					if grid == nil || w > gw {
						grid, gw = &shapes[i], w
					}
				}
			}
			if grid == nil {
				t.Fatal("grid not found")
			}
			var bars []shape
			for _, s := range shapes {
				if s.fill == colors[0] && s.y > grid.y { // legend swatches sit above the grid
					bars = append(bars, s)
				}
			}
			sort.Slice(bars, func(i, j int) bool { return bars[i].y < bars[j].y })
			if len(bars) != len(tc.tasks) {
				t.Fatalf("%d bars for %d tasks", len(bars), len(tc.tasks))
			}
			ws := gw / float64(tc.weeks)
			for i, s := range bars {
				x0, _, x1, _ := pathBBox(s.d)
				left, right := s.x+x0, s.x+x1
				if left < grid.x-0.01 || right > grid.x+gw+0.01 {
					t.Errorf("task %d spans [%g, %g], grid spans [%g, %g]", i, left, right, grid.x, grid.x+gw)
				}
				wantL := grid.x + ws*tc.tasks[i].start
				wantR := grid.x + ws*(tc.tasks[i].start+tc.tasks[i].dur)
				marker := wantR-wantL < minMarker
				if marker {
					if width := right - left; math.Abs(width-minMarker) > 0.01 {
						t.Errorf("task %d: marker %g pt wide, want %g", i, width, minMarker)
					}
					if wantL+minMarker <= grid.x+gw+0.01 && math.Abs(left-wantL) > tol {
						t.Errorf("task %d: marker starts at %g, real start %g", i, left, wantL)
					}
					continue
				}
				if math.Abs(left-wantL) > tol || math.Abs(right-wantR) > tol {
					t.Errorf("task %d: drawn [%g, %g], real [%g, %g]", i, left, right, wantL, wantR)
				}
			}
		})
	}
}

// Integers written as decimals (1.0) are integers for JSON Schema: the fragment takes
// them, and still refuses a fractional part.
// Optional fields that are present must be valid (an empty title is refused), and every
// length bound counts code points like JSON Schema, not UTF-8 bytes.
func TestChartsOptionalFieldsAndCodePointLengths(t *testing.T) {
	runner := typsttest.Runner(t)
	org := func(id, name, title any) any {
		n := obj("id", id, "name", name, "parent", "")
		if title != nil {
			n["title"] = title
		}
		return obj("type", "org", "nodes", []any{n})
	}
	acc := func(n int) string { return strings.Repeat("é", n) }
	for _, c := range []any{org("r", "Racine", nil), org(acc(32), acc(60), acc(60))} {
		if err := chartInline(t, runner, "org-chart", c); err != nil {
			t.Fatalf("must render: %v", err)
		}
	}
	for _, tc := range []struct {
		name  string
		chart any
		want  string
	}{
		{"empty title", org("r", "Racine", ""), "chart/v1: org-chart: title of node `r` must be a non-empty string"},
		{"title of 61", org("r", "Racine", acc(61)), "chart/v1: org-chart: title of node `r` is longer than 60"},
		{"name of 61", org("r", acc(61), nil), "is longer than 60"},
		{"title not a string", org("r", "Racine", 3), "chart/v1: org-chart: title of node `r` must be a non-empty string"},
		{"id of 33", org(acc(33), "Racine", nil), "chart/v1: org-chart: a node id must be a string of 1 to 32 characters"},
		{"empty id", org("", "Racine", nil), "chart/v1: org-chart: a node id must be a string of 1 to 32 characters"},
	} {
		err := chartInline(t, runner, "org-chart", tc.chart)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", tc.name, tc.want, err)
		}
	}
	// other bounded strings, at 60 and 61 accented characters
	label := func(n int) any {
		return obj("type", "bar", "categories", []any{acc(n)}, "series", []any{obj("name", acc(n), "values", []any{1})})
	}
	if err := chartInline(t, runner, "bar-chart", label(60)); err != nil {
		t.Fatalf("60 accented characters must render: %v", err)
	}
	if err := chartInline(t, runner, "bar-chart", label(61)); err == nil || !strings.Contains(err.Error(), "is longer than 60") {
		t.Fatalf("61 accented characters must be refused: %v", err)
	}
}

func TestChartsIntegersWrittenAsDecimals(t *testing.T) {
	runner := typsttest.Runner(t)
	gantt := func(weeks, phase any) any {
		return obj("type", "gantt", "weeks", weeks, "phases", []any{"p"}, "tasks", []any{obj("name", "t", "start", 0, "duration", 1, "phase", phase)})
	}
	for _, c := range []any{gantt(1.0, 0), gantt(1, 0.0), gantt(2.0, 0.0)} {
		if err := chartInline(t, runner, "gantt-chart", c); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
	}
	for _, tc := range []struct {
		chart any
		want  string
	}{
		{gantt(1.5, 0), "chart/v1: gantt-chart: weeks must be an integer"},
		{gantt(2, 0.5), "chart/v1: gantt-chart: phase of task `t` must be an integer"},
	} {
		err := chartInline(t, runner, "gantt-chart", tc.chart)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q, got %v", tc.want, err)
		}
	}
}

// ---------------------------------------------------------------- SVG scene (org chart)

type affine [6]float64

func (m affine) then(n affine) affine { // m applied after n
	return affine{m[0]*n[0] + m[2]*n[1], m[1]*n[0] + m[3]*n[1], m[0]*n[2] + m[2]*n[3], m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4], m[1]*n[4] + m[3]*n[5] + m[5]}
}
func (m affine) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

var (
	gTagRe      = regexp.MustCompile(`<g(?: [^>]*)?>|</g>|<use [^>]*/>|<path [^>]*/>`)
	transformRe = regexp.MustCompile(`transform="(translate|matrix)\(([^)]*)\)"`)
	symbolRe    = regexp.MustCompile(`<symbol id="([^"]+)"[^>]*><path d="([^"]*)"`)
)

func parseAffine(tag string) affine {
	id := affine{1, 0, 0, 1, 0, 0}
	m := transformRe.FindStringSubmatch(tag)
	if m == nil {
		return id
	}
	var v []float64
	for _, f := range strings.Fields(m[2]) {
		x, _ := strconv.ParseFloat(f, 64)
		v = append(v, x)
	}
	if m[1] == "translate" {
		if len(v) == 1 {
			v = append(v, 0) // translate(x): y is 0
		}
		return affine{1, 0, 0, 1, v[0], v[1]}
	}
	return affine{v[0], v[1], v[2], v[3], v[4], v[5]}
}

type bbox struct{ x0, y0, x1, y1 float64 }

type pItem struct {
	bbox
	fill, stroke, width string
}
type gItem struct {
	bbox
	fill string
}

// svgItems returns, in page coordinates, the bounding box of every path (with its fill,
// stroke and stroke width) and of every glyph drawn (with its fill).
func svgItems(svg string) (paths []pItem, glyphs []gItem) {
	body := regexp.MustCompile(`(?s)<defs>.*?</defs>`).ReplaceAllString(svg, "")
	symbols := map[string]bbox{}
	for _, m := range symbolRe.FindAllStringSubmatch(svg, -1) {
		x0, y0, x1, y1 := pathBBox(m[2])
		if !math.IsInf(x0, 0) {
			symbols[m[1]] = bbox{x0, y0, x1, y1}
		}
	}
	stack := []affine{{1, 0, 0, 1, 0, 0}}
	box := func(m affine, x0, y0, x1, y1 float64) bbox {
		ax, ay := m.apply(x0, y0)
		bx, by := m.apply(x1, y1)
		return bbox{math.Min(ax, bx), math.Min(ay, by), math.Max(ax, bx), math.Max(ay, by)}
	}
	for _, tag := range gTagRe.FindAllString(body, -1) {
		cur := stack[len(stack)-1]
		switch {
		case tag == "</g>":
			stack = stack[:len(stack)-1]
		case strings.HasPrefix(tag, "<g"):
			stack = append(stack, cur.then(parseAffine(tag)))
		case strings.HasPrefix(tag, "<use"):
			g, ok := symbols[strings.TrimPrefix(attr(tag, "xlink:href"), "#")]
			if !ok {
				continue
			}
			ux, _ := strconv.ParseFloat(attr(tag, "x"), 64)
			uy, _ := strconv.ParseFloat(attr(tag, "y"), 64)
			glyphs = append(glyphs, gItem{box(cur.then(affine{1, 0, 0, 1, ux, uy}), g.x0, g.y0, g.x1, g.y1), attr(tag, "fill")})
		default: // path
			x0, y0, x1, y1 := pathBBox(attr(tag, "d"))
			if math.IsInf(x0, 0) {
				continue
			}
			paths = append(paths, pItem{box(cur.then(parseAffine(tag)), x0, y0, x1, y1), attr(tag, "fill"), attr(tag, "stroke"), attr(tag, "stroke-width")})
		}
	}
	return paths, glyphs
}

// svgScene returns the node outlines (stroke 0.6 in `outline`) and every glyph box.
func svgScene(svg, outline string) (nodes, glyphs []bbox) {
	paths, gl := svgItems(svg)
	for _, p := range paths {
		if p.stroke == outline && p.width == "0.6" {
			nodes = append(nodes, p.bbox)
		}
	}
	for _, g := range gl {
		glyphs = append(glyphs, g.bbox)
	}
	return nodes, glyphs
}

// checkOrgScene verifies that every glyph lies inside the outline of one node, and that
// the nodes of one level (same top) do not overlap.
func checkOrgScene(svg, outline string, wantNodes int) error {
	nodes, glyphs := svgScene(svg, outline)
	if len(nodes) != wantNodes {
		return fmt.Errorf("%d node outlines, want %d", len(nodes), wantNodes)
	}
	if len(glyphs) == 0 {
		return fmt.Errorf("no glyph found")
	}
	const eps = 0.05
	for i, g := range glyphs {
		inside := false
		for _, n := range nodes {
			cx, cy := (g.x0+g.x1)/2, (g.y0+g.y1)/2
			if cx >= n.x0 && cx <= n.x1 && cy >= n.y0 && cy <= n.y1 {
				inside = g.x0 >= n.x0-eps && g.x1 <= n.x1+eps && g.y0 >= n.y0-eps && g.y1 <= n.y1+eps
				break
			}
		}
		if !inside {
			return fmt.Errorf("glyph %d at [%.2f %.2f %.2f %.2f] is not contained in a node", i, g.x0, g.y0, g.x1, g.y1)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].x0 < nodes[j].x0 })
	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			a, b := nodes[i], nodes[j]
			if a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1 {
				return fmt.Errorf("nodes %v and %v overlap", a, b)
			}
		}
	}
	return nil
}

func orgOf(n int, name, title string, parent func(i int) string) any {
	out := make([]any, n)
	for i := range out {
		nd := obj("id", fmt.Sprintf("n%d", i), "name", name, "parent", parent(i))
		if title != "" {
			nd["title"] = title
		}
		out[i] = nd
	}
	return obj("type", "org", "nodes", out)
}

// The height of a node follows its measured content: long names and words wider than
// the box stay inside their node, and nodes of one level never overlap.
func TestOrgNodesContainTheirText(t *testing.T) {
	runner := typsttest.Runner(t)
	outline := charterHex(t, "secondary")
	star := func(i int) string {
		if i == 0 {
			return ""
		}
		return "n0"
	}
	chain3 := func(i int) string { // n0 -> n1 -> {n2..n5}
		switch {
		case i == 0:
			return ""
		case i == 1:
			return "n0"
		}
		return "n1"
	}
	ws := strings.Repeat("W", 60)
	acc := strings.Repeat("é", 60)
	words := "Éléonore-Ségolène Châtelain-Dupont de la Rochefoucauld Bœuf"
	if n := len([]rune(words)); n > 60 {
		t.Fatalf("test setup: %d characters", n)
	}
	for _, tc := range []struct {
		name  string
		chart any
		nodes int
	}{
		{"60 W and a title, root and 8 children", orgOf(9, ws, "Responsable", star), 9},
		{"60 accented letters in one word", orgOf(9, acc, acc, star), 9},
		{"accented words of 60 characters", orgOf(9, words, words, star), 9},
		{"a deeper tree", orgOf(6, words, "Responsable", chain3), 6},
		{"short names", orgOf(3, "Alice", "", star), 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svgs := chartInlineSVG(t, runner, "org-chart", tc.chart)
			if err := checkOrgScene(svgs[0], outline, tc.nodes); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("fault: a glyph moved out of its node", func(t *testing.T) {
		svgs := chartInlineSVG(t, runner, "org-chart", orgOf(3, "Alice", "", star))
		// move the first text run 150 pt to the right
		i := strings.Index(svgs[0], `<g transform="matrix(1 0 0 -1 `)
		bad := svgs[0][:i] + `<g transform="translate(150 0)">` + svgs[0][i:]
		j := strings.Index(bad[i:], "</g>") + i
		bad = bad[:j+4] + "</g>" + bad[j+4:]
		if err := checkOrgScene(bad, outline, 3); err == nil {
			t.Fatal("a glyph outside its node was accepted")
		}
	})
	t.Run("too tall for a page is refused", func(t *testing.T) {
		nodes := []any{}
		for i := 0; i < 7; i++ { // chain of depth 6
			p := ""
			if i > 0 {
				p = fmt.Sprintf("n%d", i-1)
			}
			nodes = append(nodes, obj("id", fmt.Sprintf("n%d", i), "name", ws, "title", "Responsable", "parent", p))
		}
		for i := 7; i < 14; i++ { // 7 more leaves under n5: 8 leaves at depth 6
			nodes = append(nodes, obj("id", fmt.Sprintf("n%d", i), "name", ws, "title", "Responsable", "parent", "n5"))
		}
		err := chartInline(t, runner, "org-chart", obj("type", "org", "nodes", nodes))
		if err == nil || !strings.Contains(err.Error(), "chart/v1: org-chart: the names and titles need") {
			t.Fatalf("want a height refusal, got %v", err)
		}
	})
}

// ---------------------------------------------------------------- process flow

func insideRounded(x, y float64, b bbox, r float64) bool {
	const eps = 0.05
	if x < b.x0-eps || x > b.x1+eps || y < b.y0-eps || y > b.y1+eps {
		return false
	}
	cx := math.Min(math.Max(x, b.x0+r), b.x1-r)
	cy := math.Min(math.Max(y, b.y0+r), b.y1-r)
	return math.Hypot(x-cx, y-cy) <= r+eps
}

func insideDiamond(x, y float64, b bbox) bool {
	cx, cy := (b.x0+b.x1)/2, (b.y0+b.y1)/2
	return math.Abs(x-cx)/((b.x1-b.x0)/2)+math.Abs(y-cy)/((b.y1-b.y0)/2) <= 1+0.01
}

// checkProcessScene verifies that every white glyph (the labels) lies inside the shape
// of its step: the four corners of the glyph box inside the diamond, the rounded
// rectangle or the pill, and that the shapes do not overlap.
func checkProcessScene(svg string, primary, secondary, accent string, steps int) error {
	paths, glyphs := svgItems(svg)
	type shapeItem struct {
		bbox
		fill string
	}
	var shapes []shapeItem
	for _, p := range paths {
		if (p.fill == primary || p.fill == secondary || p.fill == accent) && p.stroke == "" && p.x1-p.x0 > 20 {
			shapes = append(shapes, shapeItem{p.bbox, p.fill})
		}
	}
	if len(shapes) != steps {
		return fmt.Errorf("%d step shapes, want %d", len(shapes), steps)
	}
	const mm = 72 / 25.4
	n := 0
	for gi, g := range glyphs {
		if g.fill != "#ffffff" {
			continue // step numbers (muted) sit above their shape
		}
		n++
		cx, cy := (g.x0+g.x1)/2, (g.y0+g.y1)/2
		ok := false
		for _, sh := range shapes {
			if cx < sh.x0 || cx > sh.x1 || cy < sh.y0 || cy > sh.y1 {
				continue
			}
			ok = true
			for _, pt := range [][2]float64{{g.x0, g.y0}, {g.x1, g.y0}, {g.x0, g.y1}, {g.x1, g.y1}} {
				switch sh.fill {
				case accent:
					ok = ok && insideDiamond(pt[0], pt[1], sh.bbox)
				case primary:
					ok = ok && insideRounded(pt[0], pt[1], sh.bbox, 6*mm)
				default:
					ok = ok && insideRounded(pt[0], pt[1], sh.bbox, 1.2*mm)
				}
			}
			break
		}
		if !ok {
			return fmt.Errorf("glyph %d at [%.2f %.2f %.2f %.2f] is not inside the shape of its step", gi, g.x0, g.y0, g.x1, g.y1)
		}
	}
	if n == 0 {
		return fmt.Errorf("no label glyph found")
	}
	for i := range shapes {
		for j := i + 1; j < len(shapes); j++ {
			a, b := shapes[i], shapes[j]
			if a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1 {
				return fmt.Errorf("steps %v and %v overlap", a.bbox, b.bbox)
			}
		}
	}
	return nil
}

func TestProcessStepsContainTheirText(t *testing.T) {
	runner := typsttest.Runner(t)
	primary, accent, secondary := charterHex(t, "primary"), charterHex(t, "accent"), charterHex(t, "secondary")
	ws := strings.Repeat("W", 60)
	acc := strings.Repeat("é", 60)
	words := "Éléonore-Ségolène Châtelain-Dupont de la Rochefoucauld Bœuf"
	if n := len([]rune(words)); n > 60 {
		t.Fatalf("test setup: %d characters", n)
	}
	kinds := []string{"start", "step", "decision", "end"}
	steps := func(n int, label func(i int) string, kind func(i int) string) any {
		out := make([]any, n)
		for i := range out {
			out[i] = obj("label", label(i), "kind", kind(i))
		}
		return obj("type", "process", "steps", out)
	}
	same := func(l string) func(int) string { return func(int) string { return l } }
	cycle := func(i int) string { return kinds[i%4] }
	for _, tc := range []struct {
		name  string
		chart any
		n     int
	}{
		{"60 W in a decision, 5 steps", steps(5, same(ws), same("decision")), 5},
		{"60 W in every shape", steps(5, same(ws), cycle), 5},
		{"accented sentences in the four shapes", steps(4, same(words), cycle), 4},
		{"60 accented letters in one word, four shapes", steps(4, same(acc), cycle), 4},
		{"12 steps, three rows", steps(12, same(words), cycle), 12},
		{"12 mixed steps", steps(12, func(i int) string {
			return []string{"A", words, "Demande reçue", ws}[i%4]
		}, cycle), 12},
		{"short labels", steps(3, same("Oui"), cycle), 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svgs := chartInlineSVG(t, runner, "process-flow", tc.chart)
			if err := checkProcessScene(svgs[0], primary, secondary, accent, tc.n); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("fault: a label moved out of its shape", func(t *testing.T) {
		svgs := chartInlineSVG(t, runner, "process-flow", steps(3, same("Oui"), same("step")))
		i := strings.Index(svgs[0], `<g transform="matrix(1 0 0 -1 `)
		bad := svgs[0][:i] + `<g transform="translate(0 150)">` + svgs[0][i:]
		j := strings.Index(bad[i:], "</g>") + i
		bad = bad[:j+4] + "</g>" + bad[j+4:]
		if err := checkProcessScene(bad, primary, secondary, accent, 3); err == nil {
			t.Fatal("a label outside its shape was accepted")
		}
	})
	t.Run("too tall for a page is refused", func(t *testing.T) {
		err := chartInline(t, runner, "process-flow", steps(12, same(ws), same("decision")))
		if err == nil || !strings.Contains(err.Error(), "chart/v1: process-flow: the labels need") {
			t.Fatalf("want a height refusal, got %v", err)
		}
	})
	t.Run("a label is one line", func(t *testing.T) {
		twenty := strings.Repeat("A\n", 19) + "A" // 39 code points, 20 lines
		for fragment, chart := range map[string]any{
			"process-flow": steps(2, same(twenty), same("step")),
			"bar-chart":    obj("type", "bar", "categories", []any{"a\nb"}, "series", []any{obj("name", "s", "values", []any{1})}),
			"org-chart":    obj("type", "org", "nodes", []any{obj("id", "r", "name", "a\tb", "parent", "")}),
			"pie-chart":    obj("type", "pie", "parts", []any{obj("name", "a\rb", "value", 1)}),
			"gantt-chart":  obj("type", "gantt", "weeks", 2, "phases", []any{"p\nq"}, "tasks", []any{obj("name", "t", "start", 0, "duration", 1, "phase", 0)}),
		} {
			err := chartInline(t, runner, fragment, chart)
			if err == nil || !strings.Contains(err.Error(), "chart/v1: "+fragment+": ") || !strings.Contains(err.Error(), "must not contain a line break or another control character") {
				t.Errorf("%s: want a control character refusal, got %v", fragment, err)
			}
		}
	})
}

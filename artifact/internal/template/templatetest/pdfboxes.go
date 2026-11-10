package templatetest

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// PageBoxes are the page boxes of one PDF page, in points; nil when absent.
type PageBoxes struct {
	Media, Trim, Bleed []float64
}

var (
	reStartxref = regexp.MustCompile(`startxref\s+(\d+)\s*%%EOF\s*$`)
	reRef       = regexp.MustCompile(`(\d+)\s+(\d+)\s+R`)
	reKids      = regexp.MustCompile(`/Kids\s*\[([^\]]*)\]`)
	rePageType  = regexp.MustCompile(`/Type\s*/Page\b`)
	reNum       = `([-+0-9.]+)`
	reBox       = func(name string) *regexp.Regexp {
		sp := `\s+`
		return regexp.MustCompile(`/` + name + `\s*\[\s*` + reNum + sp + reNum + sp + reNum + sp + reNum + `\s*\]`)
	}
)

// PDFPageBoxes reads the MediaBox, TrimBox and BleedBox of every page of a PDF with a
// classic xref table, in page order. It follows the /Prev chain of incremental updates
// the way a PDF reader does (the newest section wins), so a page object that the last
// update did not register is read in its former version, without boxes.
func PDFPageBoxes(t testing.TB, pdf []byte) []PageBoxes {
	t.Helper()
	m := reStartxref.FindSubmatch(pdf)
	if m == nil {
		t.Fatal("no startxref")
	}
	offsets := map[int]int{}
	rootRef := ""
	at, _ := strconv.Atoi(string(m[1]))
	seen := map[int]bool{}
	for first := true; ; first = false {
		if seen[at] || at <= 0 || at >= len(pdf) {
			t.Fatalf("bad xref offset %d", at)
		}
		seen[at] = true
		end := bytes.Index(pdf[at:], []byte("startxref"))
		if end < 0 {
			t.Fatal("xref section without startxref")
		}
		lines := strings.Split(strings.ReplaceAll(string(pdf[at:at+end]), "\r", ""), "\n")
		if strings.TrimSpace(lines[0]) != "xref" {
			t.Fatalf("xref expected at %d, got %q", at, lines[0])
		}
		trailer := ""
		for i := 1; i < len(lines); i++ {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			if line == "trailer" {
				trailer = strings.Join(lines[i+1:], "\n")
				break
			}
			f := strings.Fields(line)
			if len(f) != 2 {
				t.Fatalf("bad xref subsection %q", line)
			}
			start, _ := strconv.Atoi(f[0])
			count, _ := strconv.Atoi(f[1])
			for k := 0; k < count; k++ {
				i++
				e := strings.Fields(lines[i])
				if len(e) != 3 || len(lines[i]) < 18 {
					t.Fatalf("bad xref entry %q", lines[i])
				}
				if _, ok := offsets[start+k]; !ok && e[2] == "n" {
					off, _ := strconv.Atoi(e[0])
					offsets[start+k] = off
				}
			}
		}
		if first {
			r := regexp.MustCompile(`/Root\s+(\d+)\s+\d+\s+R`).FindStringSubmatch(trailer)
			if r == nil {
				t.Fatal("no /Root in the newest trailer")
			}
			rootRef = r[1]
		}
		p := regexp.MustCompile(`/Prev\s+(\d+)`).FindStringSubmatch(trailer)
		if p == nil {
			break
		}
		at, _ = strconv.Atoi(p[1])
	}
	object := func(num string) string {
		n, _ := strconv.Atoi(num)
		off, ok := offsets[n]
		if !ok {
			t.Fatalf("object %s not in the cross-reference table", num)
		}
		head := regexp.MustCompile(`^` + num + `\s+\d+\s+obj`).Find(pdf[off:min(off+32, len(pdf))])
		if head == nil {
			t.Fatalf("object %s is not at offset %d", num, off)
		}
		end := bytes.Index(pdf[off:], []byte("endobj"))
		return string(pdf[off+len(head) : off+end])
	}
	var pages []PageBoxes
	var walk func(num string)
	walk = func(num string) {
		body := object(num)
		if rePageType.MatchString(body) {
			pb := PageBoxes{}
			for name, dst := range map[string]*[]float64{"MediaBox": &pb.Media, "TrimBox": &pb.Trim, "BleedBox": &pb.Bleed} {
				if b := reBox(name).FindStringSubmatch(body); b != nil {
					for _, s := range b[1:] {
						v, err := strconv.ParseFloat(s, 64)
						if err != nil {
							t.Fatal(err)
						}
						*dst = append(*dst, v)
					}
				}
			}
			pages = append(pages, pb)
			return
		}
		k := reKids.FindStringSubmatch(body)
		if k == nil {
			t.Fatalf("object %s is neither a page nor a page tree", num)
		}
		for _, ref := range reRef.FindAllStringSubmatch(k[1], -1) {
			walk(ref[1])
		}
	}
	catalog := object(rootRef)
	pr := regexp.MustCompile(`/Pages\s+(\d+)\s+\d+\s+R`).FindStringSubmatch(catalog)
	if pr == nil {
		t.Fatal("no /Pages in the catalog")
	}
	walk(pr[1])
	return pages
}

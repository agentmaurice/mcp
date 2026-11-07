package template_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template/templatetest"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst/typsttest"
)

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

func render(t *testing.T, c template.Compiler, name, example, output string) *template.Result {
	t.Helper()
	tpl := templatetest.Load(t, "valid/"+name)
	ctx, assets := templatetest.Context(t)
	res, err := template.Render(context.Background(), template.Request{
		Template: tpl, Data: templatetest.Data(t, tpl, example), Context: ctx, Assets: assets,
		Output: output, Mode: template.ModePreview,
	}, c, template.Options{Timeout: 120 * time.Second})
	if err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return res
}

func TestGoldensRenderToPDFDeterministically(t *testing.T) {
	runner := typsttest.Runner(t)
	for _, tc := range []struct{ name, example string }{
		{"quote", "default"}, {"meeting-minutes", "default"}, {"meeting-minutes", "minimal"},
	} {
		t.Run(tc.name+"/"+tc.example, func(t *testing.T) {
			start := time.Now()
			a := render(t, runner, tc.name, tc.example, "pdf")
			elapsed := time.Since(start)
			b := render(t, runner, tc.name, tc.example, "pdf")
			if !bytes.HasPrefix(a.Content, []byte("%PDF-")) {
				t.Fatalf("not a PDF: %q", a.Content[:8])
			}
			if a.Pages < 1 || len(a.Previews) != a.Pages {
				t.Fatalf("pages=%d previews=%d", a.Pages, len(a.Previews))
			}
			for _, p := range a.Previews {
				if !bytes.HasPrefix(p.PNG, pngMagic) {
					t.Fatalf("preview %d is not a PNG", p.Number)
				}
			}
			if !bytes.Equal(a.Content, b.Content) {
				t.Fatalf("PDF not reproducible: %d vs %d bytes", len(a.Content), len(b.Content))
			}
			if a.Provenance != b.Provenance {
				t.Fatalf("provenance differs: %+v vs %+v", a.Provenance, b.Provenance)
			}
			if a.Provenance.Engine.Version != template.TypstVersion || a.Provenance.Template.Key != tc.name {
				t.Fatalf("provenance: %+v", a.Provenance)
			}
			t.Logf("%s/%s: %d pages, %d bytes PDF, first render (compile PNG+PDF) %s", tc.name, tc.example, a.Pages, len(a.Content), elapsed.Round(time.Millisecond))
		})
	}
}

func TestPNGOutputIsFirstPage(t *testing.T) {
	runner := typsttest.Runner(t)
	tpl := templatetest.Load(t, "valid/quote")
	var m map[string]any
	_ = json.Unmarshal(tpl.Manifest, &m)
	m["outputs"] = []any{"pdf", "png"}
	tpl.Manifest, _ = json.Marshal(m)
	ctx, assets := templatetest.Context(t)
	res, err := template.Render(context.Background(), template.Request{Template: tpl, Data: templatetest.Data(t, tpl, ""),
		Context: ctx, Assets: assets, Output: "png"}, runner, template.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Content, res.Previews[0].PNG) || res.MimeType != "image/png" {
		t.Fatal("png output must be the first page")
	}
}

// The contract's injection cases (tests/test_contract.py INJECTION_CASES). A
// value must never reach the Typst source: either the validation refuses the
// template, or it compiles to at most one page.
var injectionCases = []struct {
	name, main string
	data       map[string]any
}{
	{"code string", "#{ let x = \"{{ .data.x }}\"; x }\n", map[string]any{"x": "\"; panic(\"INJECT\"); let y = \""}},
	{"hash glued", "#{{ .data.x }}\n", map[string]any{"x": "pagebreak()"}},
	{"hash glued ident", "#text{{ .data.x }}\n", map[string]any{"x": "[#pagebreak() INJECT]"}},
	{"hash plus if", "#{{ if true }}{{ .data.x }}{{ end }}\n", map[string]any{"x": "pagebreak()"}},
	{"backslash", "\\{{ .data.x }}\n", map[string]any{"x": "#pagebreak()"}},
	{"range keys", "[{{ range $k, $v := .data }}{{ $k }}{{ end }}]\n", map[string]any{"#pagebreak() #text[INJECT]": 1}},
	{"printf", "[{{ printf \"%q\" .data.x }}]\n", map[string]any{"x": "#pagebreak() INJECT"}},
	{"markup", "[{{ .data.x }}]\n", map[string]any{"x": "*#pagebreak()* _INJECT_ $x$ `y` <l> @r"}},
}

func inlineTypst(t *testing.T, main string) template.Template {
	t.Helper()
	manifest := `{"schema":"agentmaurice.template/v1","key":"inject-test","version":"1.0.0","kind":"document","profile":"document",
	"source":{"engine":"typst","entry":"main.typ"},"outputs":["pdf"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":[]}`
	return template.Template{Manifest: json.RawMessage(manifest), Files: map[string]string{
		"main.typ": main, "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"}}
}

func TestInjectionCases(t *testing.T) {
	runner := typsttest.Runner(t)
	ctx, assets := templatetest.Context(t)
	for _, tc := range injectionCases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := template.Render(context.Background(), template.Request{Template: inlineTypst(t, tc.main), Data: tc.data,
				Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{Timeout: 60 * time.Second})
			var terr *template.Error
			if errors.As(err, &terr) {
				switch terr.Code {
				case template.CodeTypstCodeContext, template.CodeTypstEscapedAction, template.CodeCompileFailed:
					return // refused, or at worst the compilation fails: never an execution
				}
				t.Fatalf("unexpected error %v", err)
			}
			if res.Pages != 1 {
				t.Fatalf("injection produced %d pages", res.Pages)
			}
		})
	}
}

func TestInvalidGoldens(t *testing.T) {
	// Codes this renderer enforces; the others (static guard analysis,
	// reference resolution) belong to `maurice template check` (AGENT-1783).
	enforced := map[string]bool{
		"manifest_schema": true, "entry_missing": true, "input_schema_invalid": true, "reserved_namespace": true,
		"forbidden_function": true, "unknown_function": true, "template_syntax": true, "fragment_undeclared": true,
		"fragment_missing": true, "limits_exceeded": true, "typst_code_context": true,
	}
	dirs, _ := os.ReadDir(filepath.Join(templatetest.Root(), "invalid"))
	checked := 0
	for _, d := range dirs {
		raw, _ := os.ReadFile(filepath.Join(templatetest.Root(), "invalid", d.Name(), "expected_error.txt"))
		want := strings.TrimSpace(string(raw))
		if !enforced[want] {
			t.Logf("%s: %s is left to the static check", d.Name(), want)
			continue
		}
		t.Run(d.Name(), func(t *testing.T) {
			tpl := templatetest.Load(t, "invalid/"+d.Name())
			var data any = map[string]any{}
			if _, ok := tpl.Files["examples/default.json"]; ok {
				data = templatetest.Data(t, tpl, "")
			}
			_, err := template.Validate(tpl, data, "pdf", 0)
			var terr *template.Error
			if !errors.As(err, &terr) || terr.Code != want {
				t.Fatalf("want %s, got %v", want, err)
			}
		})
		checked++
	}
	if checked < 10 {
		t.Fatalf("only %d invalid goldens checked", checked)
	}
}

func TestInvalidDataAndContext(t *testing.T) {
	tpl := templatetest.Load(t, "valid/quote")
	ctx, assets := templatetest.Context(t)
	_, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{"reference": 1},
		Context: ctx, Assets: assets, Output: "pdf"}, nil, template.Options{})
	var terr *template.Error
	if !errors.As(err, &terr) || terr.Code != template.CodeDataInvalid {
		t.Fatalf("want data_invalid, got %v", err)
	}
	ctx.Doc = map[string]any{"number": "x"}
	_, err = template.Render(context.Background(), template.Request{Template: tpl, Data: templatetest.Data(t, tpl, ""),
		Context: ctx, Assets: assets, Output: "pdf"}, nil, template.Options{})
	if !errors.As(err, &terr) || terr.Code != template.CodeContextInvalid {
		t.Fatalf("want context_invalid, got %v", err)
	}
}

func TestOutputAndEngineGuards(t *testing.T) {
	tpl := templatetest.Load(t, "valid/quote")
	ctx, assets := templatetest.Context(t)
	for _, output := range []string{"html", "png", "txt"} { // quote declares pdf only
		_, err := template.Render(context.Background(), template.Request{Template: tpl, Data: templatetest.Data(t, tpl, ""),
			Context: ctx, Assets: assets, Output: output}, nil, template.Options{})
		var terr *template.Error
		if !errors.As(err, &terr) || terr.Code != template.CodeOutputUnsupported {
			t.Fatalf("%s: want output_unsupported, got %v", output, err)
		}
	}
	if _, err := template.Render(context.Background(), template.Request{Template: tpl, Data: templatetest.Data(t, tpl, ""),
		Context: ctx, Assets: assets, Output: "pdf", Mode: "bogus"}, nil, template.Options{}); err == nil {
		t.Fatal("bogus mode accepted")
	}
}

func TestPackageImportAndTemplateSizeRefused(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	do := func(tpl template.Template, opts template.Options) error {
		_, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}, nil, opts)
		return err
	}
	var terr *template.Error
	if err := do(inlineTypst(t, "#import \"@preview/cetz:0.3.0\"\n"), template.Options{}); !errors.As(err, &terr) || terr.Code != template.CodePackageImport {
		t.Fatalf("want package_import_forbidden, got %v", err)
	}
	if err := do(inlineTypst(t, strings.Repeat("a", 5000)), template.Options{MaxTemplateBytes: 1000}); !errors.As(err, &terr) || terr.Code != template.CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if err := do(inlineTypst(t, "[{{ .data.missing }}]"), template.Options{}); !errors.As(err, &terr) || terr.Code != template.CodeTemplateExec {
		t.Fatalf("want template_exec for an unguarded absent value, got %v", err)
	}
}

func TestHTMLContextualEscaping(t *testing.T) {
	manifest := `{"schema":"agentmaurice.template/v1","key":"html-test","version":"1.0.0","kind":"page","profile":"web",
	"source":{"engine":"html","entry":"main.html"},"outputs":["html"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":[]}`
	tpl := template.Template{Manifest: json.RawMessage(manifest), Files: map[string]string{
		"main.html":         `<a href="{{ .data.u }}" title="{{ .data.t }}">{{ .data.x }}</a>`,
		"input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"}}
	ctx, assets := templatetest.Context(t)
	res, err := template.Render(context.Background(), template.Request{Template: tpl, Context: ctx, Assets: assets, Output: "html",
		Data: map[string]any{"u": "javascript:alert(1)", "t": `"><script>`, "x": "<b>&"}}, nil, template.Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Content)
	if strings.Contains(out, "<script>") || strings.Contains(out, "javascript:") || !strings.Contains(out, "&lt;b&gt;&amp;") {
		t.Fatalf("html not escaped contextually: %s", out)
	}
	if res.Filename != "main.html" || res.Provenance.Engine.Name != "html" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

type fakeCompiler struct {
	version string
	block   bool
}

func (f fakeCompiler) Version(context.Context) (string, error) { return f.version, nil }
func (f fakeCompiler) Compile(ctx context.Context, _ template.CompileRequest) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func TestEngineVersionAndTimeout(t *testing.T) {
	tpl := templatetest.Load(t, "valid/quote")
	ctx, assets := templatetest.Context(t)
	req := template.Request{Template: tpl, Data: templatetest.Data(t, tpl, ""), Context: ctx, Assets: assets, Output: "pdf"}
	var terr *template.Error
	if _, err := template.Render(context.Background(), req, fakeCompiler{version: "0.14.0"}, template.Options{}); !errors.As(err, &terr) || terr.Code != template.CodeEngineVersion {
		t.Fatalf("want engine_version_mismatch, got %v", err)
	}
	if _, err := template.Render(context.Background(), req, fakeCompiler{version: template.TypstVersion, block: true}, template.Options{Timeout: 50 * time.Millisecond}); !errors.As(err, &terr) || terr.Code != template.CodeRenderTimeout {
		t.Fatalf("want render_timeout, got %v", err)
	}
}

func TestMaxPages(t *testing.T) {
	runner := typsttest.Runner(t)
	tpl := inlineTypst(t, "page one\n#pagebreak()\npage two\n")
	ctx, assets := templatetest.Context(t)
	req := template.Request{Template: tpl, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}
	res, err := template.Render(context.Background(), req, runner, template.Options{MaxPages: 2})
	if err != nil || res.Pages != 2 {
		t.Fatalf("pages=%v err=%v", res, err)
	}
	var terr *template.Error
	if _, err := template.Render(context.Background(), req, runner, template.Options{MaxPages: 1}); !errors.As(err, &terr) || terr.Code != template.CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
}

func TestCompileFailureIsReported(t *testing.T) {
	runner := typsttest.Runner(t)
	ctx, assets := templatetest.Context(t)
	_, err := template.Render(context.Background(), template.Request{Template: inlineTypst(t, "#nofunction()\n"), Data: map[string]any{},
		Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{})
	var terr *template.Error
	if !errors.As(err, &terr) || terr.Code != template.CodeCompileFailed || !strings.Contains(terr.Message, "nofunction") {
		t.Fatalf("want compile_failed naming the cause, got %v", err)
	}
}

func textTemplate(main string) template.Template {
	manifest := `{"schema":"agentmaurice.template/v1","key":"text-test","version":"1.0.0","kind":"page","profile":"web",
	"source":{"engine":"text","entry":"main.txt"},"outputs":["txt"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":[]}`
	return template.Template{Manifest: json.RawMessage(manifest), Files: map[string]string{
		"main.txt": main, "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"}}
}

// Finding 1: local files of the folder reach Typst, and are checked like the entry.
func TestLocalTypstFilesAreIncluded(t *testing.T) {
	runner := typsttest.Runner(t)
	ctx, assets := templatetest.Context(t)
	tpl := inlineTypst(t, "#include \"parts/body.typ\"\n")
	tpl.Files["parts/body.typ"] = "Bonjour {{ .data.x }}\n"
	res, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{"x": "#pagebreak()"},
		Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{})
	if err != nil || res.Pages != 1 {
		t.Fatalf("include failed: %v", err)
	}
	var terr *template.Error
	tpl.Files["parts/body.typ"] = "{{ now }}"
	if _, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{}); !errors.As(err, &terr) || terr.Code != template.CodeForbiddenFunction {
		t.Fatalf("included file must be checked: %v", err)
	}
	tpl.Files["parts/body.typ"] = "x"
	tpl.Files["notes.md"] = "x"
	if _, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{}); !errors.As(err, &terr) || terr.Code != template.CodeFileExtension {
		t.Fatalf("want file_extension_forbidden: %v", err)
	}
}

// Finding 2: template execution is bounded by the deadline and by the output size.
func TestExecutionTimeoutAndOutputLimit(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	items := make([]any, 10000)
	for i := range items {
		items[i] = float64(i)
	}
	data := map[string]any{"a": items}
	loop := textTemplate(`{{range .data.a}}{{range $.data.a}}{{ "" }}{{end}}{{end}}`)
	start := time.Now()
	_, err := template.Render(context.Background(), template.Request{Template: loop, Data: data, Context: ctx, Assets: assets, Output: "txt"},
		nil, template.Options{Timeout: time.Second})
	var terr *template.Error
	if !errors.As(err, &terr) || terr.Code != template.CodeRenderTimeout {
		t.Fatalf("want render_timeout, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout took %s", time.Since(start))
	}
	big := textTemplate(`{{range .data.a}}{{range $.data.a}}{{ . }}{{end}}{{end}}`)
	_, err = template.Render(context.Background(), template.Request{Template: big, Data: data, Context: ctx, Assets: assets, Output: "txt"},
		nil, template.Options{MaxOutputBytes: 1 << 20, Timeout: 30 * time.Second})
	if !errors.As(err, &terr) || terr.Code != template.CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded for the output size, got %v", err)
	}
}

// Finding 4: names written by the platform are reserved.
func TestReservedFileNames(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	var terr *template.Error
	for _, name := range []string{"brand.typ", "values.json", "out.pdf", "page-1.png", "fragments/header.typ"} {
		tpl := inlineTypst(t, "x")
		tpl.Files[name] = "x"
		_, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}, nil, template.Options{})
		if !errors.As(err, &terr) || terr.Code != template.CodeReservedFileName {
			t.Fatalf("%s: want reserved_file_name, got %v", name, err)
		}
	}
	tpl := inlineTypst(t, "x")
	_, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{}, Context: ctx,
		Assets: map[string][]byte{"main.typ": []byte("x")}, Output: "pdf"}, nil, template.Options{})
	if !errors.As(err, &terr) || terr.Code != template.CodeAssetInvalid {
		t.Fatalf("asset with a template name: got %v", err)
	}
}

// Pass 2, finding 1: paths are normalized and duplicates after normalization refused.
func TestPathNormalization(t *testing.T) {
	var terr *template.Error
	dup := inlineTypst(t, "#include \"parts/body.typ\"\n")
	dup.Files["parts/body.typ"] = "a"
	dup.Files["parts/./body.typ"] = "b"
	if _, err := template.Validate(dup, map[string]any{}, "pdf", 0); !errors.As(err, &terr) || terr.Code != template.CodeFilePathInvalid {
		t.Fatalf("duplicate after normalization: got %v", err)
	}
	for _, bad := range []string{"../x.typ", "/abs.typ", "a/../b.typ"} {
		tpl := inlineTypst(t, "x")
		tpl.Files[bad] = "x"
		if _, err := template.Validate(tpl, map[string]any{}, "pdf", 0); !errors.As(err, &terr) || terr.Code != template.CodeFilePathInvalid {
			t.Fatalf("%q: got %v", bad, err)
		}
	}
	one := inlineTypst(t, "#include \"parts/body.typ\"\n")
	one.Files["parts/./body.typ"] = "a"
	p, err := template.Validate(one, map[string]any{}, "pdf", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Files["parts/body.typ"]; !ok {
		t.Fatalf("path not normalized: %v", p.Files)
	}
}

// bigFileCompiler writes one compiled file larger than the limit.
type bigFileCompiler struct{}

func (bigFileCompiler) Version(context.Context) (string, error) { return template.TypstVersion, nil }
func (bigFileCompiler) Compile(_ context.Context, req template.CompileRequest) error {
	name := "out.pdf"
	if req.Format == "png" {
		name = "page-1.png"
	}
	return os.WriteFile(filepath.Join(req.OutDir, name), make([]byte, 3<<20), 0o644)
}

// Pass 2, finding 4: the size limit applies to every compiled file.
func TestCompiledFileSizeLimit(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	var terr *template.Error
	for _, output := range []string{"pdf"} {
		_, err := template.Render(context.Background(), template.Request{Template: inlineTypst(t, "x"), Data: map[string]any{},
			Context: ctx, Assets: assets, Output: output}, bigFileCompiler{}, template.Options{MaxOutputBytes: 1 << 20})
		if !errors.As(err, &terr) || terr.Code != template.CodeLimitsExceeded {
			t.Fatalf("%s: want limits_exceeded, got %v", output, err)
		}
	}
}

// valuesCompiler records values.json and the PDF it "compiles" from it.
type valuesCompiler struct{ seen *[]string }

func (valuesCompiler) Version(context.Context) (string, error) { return template.TypstVersion, nil }
func (v valuesCompiler) Compile(_ context.Context, req template.CompileRequest) error {
	vals, _ := os.ReadFile(filepath.Join(req.Dir, "values.json"))
	if req.Format == "pdf" {
		*v.seen = append(*v.seen, string(vals))
		return os.WriteFile(filepath.Join(req.OutDir, "out.pdf"), vals, 0o644)
	}
	return os.WriteFile(filepath.Join(req.OutDir, "page-1.png"), []byte("png"), 0o644)
}

// Pass 3, finding 1: file order does not depend on map iteration, so the value
// table (values.json) and the output are identical on every call.
func TestDeterministicValuesOrder(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	tpl := inlineTypst(t, "#repr(json(\"/values.json\"))\n")
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		tpl.Files[name+".typ"] = "{{ .data." + name + " }}"
	}
	data := map[string]any{"a": "A", "b": "B", "c": "C", "d": "D", "e": "E", "f": "F"}
	var seen []string
	var first *template.Result
	for i := 0; i < 20; i++ {
		res, err := template.Render(context.Background(), template.Request{Template: tpl, Data: data, Context: ctx, Assets: assets, Output: "pdf"},
			valuesCompiler{&seen}, template.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = res
		} else if !bytes.Equal(res.Content, first.Content) || res.Provenance != first.Provenance {
			t.Fatalf("call %d differs: %s vs %s", i, res.Content, first.Content)
		}
	}
	if string(first.Content) != `["A","B","C","D","E","F"]` {
		t.Fatalf("values must follow sorted paths: %s", first.Content)
	}
	// Real Typst: the PDF itself is identical across calls.
	runner := typsttest.Runner(t)
	var sums [][]byte
	for i := 0; i < 3; i++ {
		res, err := template.Render(context.Background(), template.Request{Template: tpl, Data: data, Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{})
		if err != nil {
			t.Fatal(err)
		}
		sums = append(sums, res.Content)
	}
	if !bytes.Equal(sums[0], sums[1]) || !bytes.Equal(sums[1], sums[2]) {
		t.Fatal("PDF differs across calls")
	}
}

// Pass 4, finding 2: loops without any output stop at the deadline and the
// execution goroutine ends (the template engine cannot be interrupted from outside).
func TestSilentLoopsStopAtDeadline(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	items := make([]any, 10000)
	for i := range items {
		items[i] = float64(i)
	}
	tpl := textTemplate(`{{range .data.a}}{{range $.data.a}}{{range $.data.a}}{{range $.data.a}}{{ $x := . }}{{end}}{{end}}{{end}}{{end}}`)
	before := runtime.NumGoroutine()
	start := time.Now()
	_, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{"a": items}, Context: ctx, Assets: assets, Output: "txt"},
		nil, template.Options{Timeout: time.Second})
	var terr *template.Error
	if !errors.As(err, &terr) || terr.Code != template.CodeRenderTimeout {
		t.Fatalf("want render_timeout, got %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("took %s", d)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("execution goroutine leaked: %d before, %d after", before, after)
	}
}

// Pass 6, finding 2: the source fingerprint covers every resolved file.
func TestSourceDigestCoversIncludedFiles(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	digest := func(body string, assets map[string][]byte) string {
		tpl := inlineTypst(t, "#include \"parts/body.typ\"\n")
		tpl.Files["parts/body.typ"] = body
		res, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"},
			valuesCompiler{&[]string{}}, template.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return res.Provenance.SourceSHA256
	}
	a, b := digest("Bonjour", assets), digest("Au revoir", assets)
	if a == b {
		t.Fatal("included file not covered by the source fingerprint")
	}
	if a != digest("Bonjour", assets) {
		t.Fatal("identical inputs must give the same fingerprint")
	}
	if a == digest("Bonjour", map[string][]byte{"logo.svg": []byte("<svg/>")}) {
		t.Fatal("assets must be covered")
	}
}

// Pass 8, finding 2: every provided asset is written into the Typst project.
func TestAllAssetsAreAvailableToTypst(t *testing.T) {
	runner := typsttest.Runner(t)
	ctx, assets := templatetest.Context(t)
	svg := assets["logo.svg"]
	all := map[string][]byte{"logo.svg": svg, "photo.svg": svg, "img/./pic.svg": svg}
	res, err := template.Render(context.Background(), template.Request{
		Template: inlineTypst(t, "#image(\"/photo.svg\", width: 20mm)\n#image(\"/img/pic.svg\", width: 20mm)\n"),
		Data:     map[string]any{}, Context: ctx, Assets: all, Output: "pdf"}, runner, template.Options{})
	if err != nil || res.Pages != 1 || !bytes.HasPrefix(res.Content, []byte("%PDF-")) {
		t.Fatalf("render with extra assets: %v", err)
	}
	var terr *template.Error
	for name, assets := range map[string]map[string][]byte{
		"duplicate": {"a.svg": svg, "./a.svg": svg},
		"parent":    {"../a.svg": svg},
		"absolute":  {"/a.svg": svg},
		"reserved":  {"page-1.png": svg},
		"extension": {"a.exe": svg},
	} {
		_, err := template.Render(context.Background(), template.Request{Template: inlineTypst(t, "x"), Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}, nil, template.Options{})
		if !errors.As(err, &terr) {
			t.Errorf("%s: want a stable error, got %v", name, err)
		}
	}
	tpl := inlineTypst(t, "x")
	tpl.Files["photo.svg"] = "x"
	if _, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{}, Context: ctx, Assets: map[string][]byte{"photo.svg": svg}, Output: "pdf"}, nil, template.Options{}); !errors.As(err, &terr) {
		t.Errorf("collision with a template file: %v", err)
	}
}

// Pass 14: a file count limit comes first, and validation runs under the deadline.
func TestFileCountLimitAndValidationDeadline(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	var terr *template.Error
	many := inlineTypst(t, "x")
	for i := 0; i < 40000; i++ {
		many.Files[fmt.Sprintf("f%d.typ", i)] = ""
	}
	start := time.Now()
	_, err := template.Render(context.Background(), template.Request{Template: many, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"},
		nil, template.Options{Timeout: time.Second})
	if !errors.As(err, &terr) || terr.Code != template.CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the count must be refused at once, took %s", time.Since(start))
	}

	slow := inlineTypst(t, "x")
	for i := 0; i < 200; i++ {
		slow.Files[fmt.Sprintf("f%d.typ", i)] = "x"
	}
	template.TestValidateHook = func() { time.Sleep(10 * time.Millisecond) } // 200 files: about 2 s
	t.Cleanup(func() { template.TestValidateHook = nil })
	start = time.Now()
	_, err = template.Render(context.Background(), template.Request{Template: slow, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"},
		nil, template.Options{Timeout: 300 * time.Millisecond})
	if !errors.As(err, &terr) || terr.Code != template.CodeRenderTimeout {
		t.Fatalf("want render_timeout, got %v", err)
	}
	if d := time.Since(start); d > 300*time.Millisecond+3*time.Second {
		t.Fatalf("took %s", d)
	}
}

// Pass 21: Typst packages are refused statically and, with the filter off, fail at runtime.
func TestTypstPackageImportsFail(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	src := "#import (\"@preview/cetz:0.3.0\"): *\n"
	req := template.Request{Template: inlineTypst(t, src), Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}
	var terr *template.Error
	if _, err := template.Render(context.Background(), req, nil, template.Options{}); !errors.As(err, &terr) || terr.Code != template.CodePackageImport {
		t.Fatalf("static refusal expected, got %v", err)
	}

	runner := typsttest.Runner(t)
	home, _ := os.UserHomeDir()
	cache, _ := os.UserCacheDir()
	outside := []string{filepath.Join(cache, "typst"), filepath.Join(home, ".cache", "typst"), filepath.Join(home, ".local", "share", "typst"), filepath.Join(home, ".typst")}
	existed := map[string]bool{}
	for _, d := range outside {
		_, err := os.Stat(d)
		existed[d] = err == nil
	}
	template.TestSkipPackageFilter = true
	t.Cleanup(func() { template.TestSkipPackageFilter = false })
	start := time.Now()
	_, err := template.Render(context.Background(), req, runner, template.Options{Timeout: 20 * time.Second})
	if !errors.As(err, &terr) || terr.Code != template.CodeCompileFailed {
		t.Fatalf("runtime failure expected (compile_failed), got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s", d)
	}
	for _, d := range outside {
		if _, statErr := os.Stat(d); statErr == nil && !existed[d] {
			t.Fatalf("%s was created outside the temporary project", d)
		}
	}
	t.Logf("package import failed at runtime: %.200s", terr.Message)
}

// Pass 25: string sizes enter the validation work; literal values are not references.
func TestValidationWorkCountsStringBytes(t *testing.T) {
	prop := map[string]any{"type": "string", "pattern": "^a+$"}
	copies := make([]any, 4000)
	for i := range copies {
		copies[i] = prop
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{"s": map[string]any{"allOf": copies}}})
	tpl := template.Template{Manifest: json.RawMessage(`{"schema":"agentmaurice.template/v1","key":"work-bytes","version":"1.0.0","kind":"page","profile":"web",
	"source":{"engine":"text","entry":"main.txt"},"outputs":["txt"],"default_locale":"fr","input_schema":"input.schema.json",
	"examples":[{"name":"default","data":"examples/default.json"}],"fragments":[]}`),
		Files: map[string]string{"main.txt": "x", "input.schema.json": string(schema), "examples/default.json": "{}"}}
	start := time.Now()
	_, err := template.Validate(tpl, map[string]any{"s": strings.Repeat("a", 2_000_000)}, "txt", 0)
	var terr *template.Error
	if !errors.As(err, &terr) || terr.Code != template.CodeLimitsExceeded {
		t.Fatalf("want limits_exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	// measured work of the goldens (schema cost x data work), all far under the budget
	for _, name := range []string{"quote", "meeting-minutes"} {
		g := templatetest.Load(t, "valid/"+name)
		p, err := template.Validate(g, templatetest.Data(t, g, ""), "pdf", 0)
		if err != nil {
			t.Fatal(err)
		}
		cost, _ := template.SchemaCostForTest(p.InputSchema)
		work := template.DataWorkForTest(templatetest.Data(t, g, ""))
		t.Logf("%s: schema cost %d x data work %d = %d (budget %d)", name, cost, work, cost*work, template.MaxValidationWork)
		if cost*work > template.MaxValidationWork {
			t.Fatalf("%s exceeds the budget", name)
		}
	}
}

func TestLiteralValuesAreNotReferences(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	mk := func(schema string) template.Template {
		return template.Template{Manifest: json.RawMessage(`{"schema":"agentmaurice.template/v1","key":"literal","version":"1.0.0","kind":"page","profile":"web",
		"source":{"engine":"text","entry":"main.txt"},"outputs":["txt"],"default_locale":"fr","input_schema":"input.schema.json",
		"examples":[{"name":"default","data":"examples/default.json"}],"fragments":[]}`),
			Files: map[string]string{"main.txt": "{{ json .data }}", "input.schema.json": schema, "examples/default.json": "{}"}}
	}
	data := map[string]any{"a": map[string]any{"$ref": "https://example.com/x"}}
	res, err := template.Render(context.Background(), template.Request{
		Template: mk(`{"type":"object","properties":{"a":{"const":{"$ref":"https://example.com/x"},"default":{"$ref":"https://x/y"},"enum":[{"$ref":"https://example.com/x"}]}}}`),
		Data:     data, Context: ctx, Assets: assets, Output: "txt"}, nil, template.Options{})
	if err != nil {
		t.Fatalf("a literal that looks like a reference must pass: %v", err)
	}
	if string(res.Content) != "{\n  \"a\": {\n    \"$ref\": \"https://example.com/x\"\n  }\n}" {
		t.Fatalf("unexpected output %q", res.Content)
	}
	var terr *template.Error
	for _, schema := range []string{
		`{"type":"object","properties":{"a":{"x-sub":{"$ref":"https://example.com/x"}}}}`,
		`{"type":"object","properties":{"const":{"x-sub":{"$ref":"https://example.com/x"}}}}`,
	} {
		_, err = template.Render(context.Background(), template.Request{Template: mk(schema), Data: map[string]any{}, Context: ctx, Assets: assets, Output: "txt"}, nil, template.Options{})
		if !errors.As(err, &terr) || terr.Code != template.CodeInputSchemaInvalid {
			t.Fatalf("a non local reference outside a literal must stay refused: %v", err)
		}
	}
}

// File and directory collisions among files, assets and generated names are refused, stably.
func TestProjectPathCollisions(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	svg := assets["logo.svg"]
	render := func(tpl template.Template, assets map[string][]byte) error {
		_, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}, nil, template.Options{})
		return err
	}
	var terr *template.Error
	msgs := map[string]bool{}
	for i := 0; i < 20; i++ {
		err := render(inlineTypst(t, "x"), map[string][]byte{"image.svg": svg, "image.svg/photo.png": svg})
		if !errors.As(err, &terr) || terr.Code != template.CodeFilePathInvalid {
			t.Fatalf("want file_path_invalid, got %v", err)
		}
		msgs[terr.Message] = true
	}
	if len(msgs) != 1 {
		t.Fatalf("%d distinct messages: %v", len(msgs), msgs)
	}
	tpl := inlineTypst(t, "x")
	tpl.Files["pics/a.svg"] = "x"
	if err := render(tpl, map[string][]byte{"pics/a.svg/b.svg": svg}); !errors.As(err, &terr) || terr.Code != template.CodeFilePathInvalid {
		t.Fatalf("template file vs asset directory: %v", err)
	}
	if err := render(inlineTypst(t, "x"), map[string][]byte{".typst-pkg/x.svg": svg}); !errors.As(err, &terr) || terr.Code != template.CodeFilePathInvalid {
		t.Fatalf(".typst-pkg/x: want file_path_invalid, got %v", err)
	}
	for _, name := range []string{"brand.typ/x.svg", "values.json/x.svg", "page-1.png/x.svg"} {
		if err := render(inlineTypst(t, "x"), map[string][]byte{name: svg}); !errors.As(err, &terr) || (terr.Code != template.CodeFilePathInvalid && terr.Code != template.CodeAssetInvalid) {
			t.Fatalf("%s: want a path refusal, got %v", name, err)
		}
	}
	if err := render(inlineTypst(t, "x"), map[string][]byte{"img/a.svg": svg, "img/b.svg": svg}); err != nil && !errors.As(err, &terr) {
		t.Fatalf("siblings in one directory must not collide: %v", err)
	} else if terr != nil && terr.Code == template.CodeFilePathInvalid {
		t.Fatalf("siblings refused: %v", err)
	}
}

// Fingerprints use an unambiguous framing: file boundaries cannot be imitated.
func TestFingerprintsAreUnambiguous(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	digest := func(files map[string]string) (string, string) {
		tpl := inlineTypst(t, "#include \"a.typ\"\n")
		delete(tpl.Files, "examples/default.json")
		for k, v := range files {
			tpl.Files[k] = v
		}
		res, err := template.Render(context.Background(), template.Request{Template: tpl, Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"},
			valuesCompiler{&[]string{}}, template.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return res.Provenance.Template.SHA256, res.Provenance.SourceSHA256
	}
	t1, s1 := digest(map[string]string{"a.typ": "x\x00file\x00b.typ\x00y"})
	t2, s2 := digest(map[string]string{"a.typ": "x", "b.typ": "y"})
	if t1 == t2 || s1 == s2 {
		t.Fatalf("different templates share a fingerprint: template %v source %v", t1 == t2, s1 == s2)
	}
	t3, s3 := digest(map[string]string{"a.typ": "x", "b.typ": "y"})
	if t2 != t3 || s2 != s3 {
		t.Fatal("identical inputs must give identical fingerprints")
	}
}

// rootRecorder records the files of the project root at every compilation.
type rootRecorder struct{ roots *[][]string }

func (rootRecorder) Version(context.Context) (string, error) { return template.TypstVersion, nil }
func (r rootRecorder) Compile(_ context.Context, req template.CompileRequest) error {
	var names []string
	_ = filepath.WalkDir(req.Dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(req.Dir, p)
			names = append(names, rel)
		}
		return nil
	})
	sort.Strings(names)
	*r.roots = append(*r.roots, names)
	name := "out.pdf"
	if req.Format == "png" {
		name = "page-1.png"
	}
	if strings.HasPrefix(req.OutDir, req.Dir) {
		return errors.New("the output directory must be outside the project root")
	}
	return os.WriteFile(filepath.Join(req.OutDir, name), []byte("x"), 0o644)
}

// Typst outputs never land under the root, so the root is identical between the
// passes and a template cannot read an earlier output.
func TestOutputsStayOutsideTheRoot(t *testing.T) {
	ctx, assets := templatetest.Context(t)
	var roots [][]string
	_, err := template.Render(context.Background(), template.Request{Template: inlineTypst(t, "x"), Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"},
		rootRecorder{&roots}, template.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || !reflect.DeepEqual(roots[0], roots[1]) {
		t.Fatalf("the root must be identical at both passes: %v", roots)
	}
	for _, name := range roots[0] {
		if strings.HasPrefix(name, "page-") || name == "out.pdf" {
			t.Fatalf("an output is in the root: %v", roots[0])
		}
	}
	// with real Typst: reading an output from the root fails at compilation
	runner := typsttest.Runner(t)
	for _, src := range []string{"#read(\"/page-1.png\")\n", "#read(\"/out.pdf\")\n"} {
		_, err = template.Render(context.Background(), template.Request{Template: inlineTypst(t, src), Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{})
		var terr *template.Error
		if !errors.As(err, &terr) || terr.Code != template.CodeCompileFailed {
			t.Fatalf("%q: want compile_failed, got %v", src, err)
		}
	}
	// the previews and the PDF come from the same project: same number of pages
	res, err := template.Render(context.Background(), template.Request{Template: inlineTypst(t, "a\n#pagebreak()\nb\n#pagebreak()\nc\n"), Data: map[string]any{}, Context: ctx, Assets: assets, Output: "pdf"}, runner, template.Options{})
	if err != nil || res.Pages != 3 || len(res.Previews) != 3 {
		t.Fatalf("pages=%v err=%v", res, err)
	}
	n := len(regexp.MustCompile(`/Type\s*/Page\b`).FindAll(res.Content, -1))
	t.Logf("PDF page objects found in the raw bytes: %d", n)
	if n != 3 {
		t.Fatalf("the PDF has %d pages, previews 3", n)
	}
}

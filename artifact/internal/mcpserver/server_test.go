package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template/templatetest"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst/typsttest"
)

type toolResult struct {
	IsError bool
	Content map[string]any
}

// call runs a tool through the JSON-RPC entry point of the MCP server.
func call(t *testing.T, deps Deps, tool string, args map[string]any) toolResult {
	t.Helper()
	srv := NewWithDeps(deps).Legacy()
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	raw, err := json.Marshal(srv.HandleMessage(context.Background(), msg))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("JSON-RPC error: %s", resp.Error.Message)
	}
	return toolResult{IsError: resp.Result.IsError, Content: resp.Result.StructuredContent}
}

func goldenArgs(t *testing.T, name, example, output, mode string) map[string]any {
	t.Helper()
	tpl := templatetest.Load(t, "valid/"+name)
	ctx, assets := templatetest.Context(t)
	enc := map[string]string{}
	for k, v := range assets {
		enc[k] = base64.StdEncoding.EncodeToString(v)
	}
	var manifest map[string]any
	_ = json.Unmarshal(tpl.Manifest, &manifest)
	return map[string]any{
		"template": map[string]any{"manifest": manifest, "files": tpl.Files},
		"data":     templatetest.Data(t, tpl, example),
		"context":  map[string]any{"company": ctx.Company, "brand": ctx.Brand, "doc": ctx.Doc, "i18n": ctx.I18n},
		"assets":   enc, "output": output, "mode": mode,
	}
}

func TestRenderGoldensThroughTheTool(t *testing.T) {
	deps := Deps{Compiler: typsttest.Runner(t)}
	for _, name := range []string{"quote", "meeting-minutes"} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			a := call(t, deps, "artifact_render_v1", goldenArgs(t, name, "", "pdf", "final"))
			elapsed := time.Since(start)
			b := call(t, deps, "artifact_render_v1", goldenArgs(t, name, "", "pdf", "final"))
			if a.IsError || b.IsError {
				t.Fatalf("error: %v %v", a.Content, b.Content)
			}
			file := a.Content["file"].(map[string]any)
			pdf, err := base64.StdEncoding.DecodeString(file["content_base64"].(string))
			if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
				t.Fatalf("not a PDF (%v)", err)
			}
			pages := int(a.Content["pages"].(float64))
			previews := a.Content["previews"].([]any)
			if pages < 1 || len(previews) != pages {
				t.Fatalf("pages=%d previews=%d", pages, len(previews))
			}
			for _, p := range previews {
				png, _ := base64.StdEncoding.DecodeString(p.(map[string]any)["content_base64"].(string))
				if !bytes.HasPrefix(png, []byte("\x89PNG")) {
					t.Fatal("preview is not a PNG")
				}
			}
			if file["sha256"] != b.Content["file"].(map[string]any)["sha256"] || file["sha256"] != hexSHA(pdf) {
				t.Fatalf("SHA-256 not stable: %v vs %v", file["sha256"], b.Content["file"])
			}
			prov := a.Content["provenance"].(map[string]any)
			if prov["engine"].(map[string]any)["version"] != template.TypstVersion || prov["template"].(map[string]any)["key"] != name {
				t.Fatalf("provenance: %v", prov)
			}
			if a.Content["artifact"].(map[string]any)["manifest"].(map[string]any)["sha256"] != file["sha256"] {
				t.Fatal("legacy manifest out of sync")
			}
			t.Logf("%s: %d pages, %d bytes PDF, sha256 %s, render (PNG+PDF) %s", name, pages, len(pdf), file["sha256"], elapsed.Round(time.Millisecond))
		})
	}
}

func TestRenderErrorsAreStable(t *testing.T) {
	args := goldenArgs(t, "quote", "", "pdf", "preview")
	args["data"] = map[string]any{"reference": 7}
	r := call(t, Deps{}, "artifact_render_v1", args)
	if !r.IsError || r.Content["error"] != template.CodeDataInvalid {
		t.Fatalf("want data_invalid: %v", r.Content)
	}
	args = goldenArgs(t, "quote", "", "pdf", "preview")
	r = call(t, Deps{}, "artifact_render_v1", args) // no engine configured
	if !r.IsError || r.Content["error"] != template.CodeEngineUnavailable {
		t.Fatalf("want engine_unavailable: %v", r.Content)
	}
	delete(args, "output")
	if r = call(t, Deps{}, "artifact_render_v1", args); !r.IsError || r.Content["error"] != "invalid_arguments" {
		t.Fatalf("want invalid_arguments: %v", r.Content)
	}
}

func TestV01CallStillWorks(t *testing.T) {
	r := call(t, Deps{}, "artifact_render_v1", map[string]any{"format": "markdown", "content": "# Hello", "output_format": "pdf"})
	if r.IsError {
		t.Fatalf("%v", r.Content)
	}
	art := r.Content["artifact"].(map[string]any)
	pdf, _ := base64.StdEncoding.DecodeString(art["content_base64"].(string))
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || art["manifest"].(map[string]any)["renderer_version"] != "0.1.0" {
		t.Fatalf("unexpected v0.1 result: %v", art["manifest"])
	}
}

func TestHTMLRenderThroughTheTool(t *testing.T) {
	r := call(t, Deps{}, "artifact_render_v1", map[string]any{
		"template": map[string]any{
			"manifest": map[string]any{"schema": "agentmaurice.template/v1", "key": "page-test", "version": "1.0.0", "kind": "page", "profile": "web",
				"source": map[string]any{"engine": "html", "entry": "main.html"}, "outputs": []any{"html"}, "default_locale": "fr",
				"input_schema": "input.schema.json", "examples": []any{map[string]any{"name": "default", "data": "examples/default.json"}}},
			"files": map[string]any{"main.html": "<p>{{ .data.x }}</p>", "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"},
		},
		"data":    map[string]any{"x": "<i>"},
		"context": goldenArgs(t, "quote", "", "pdf", "")["context"],
		"output":  "html",
	})
	if r.IsError {
		t.Fatalf("%v", r.Content)
	}
	body, _ := base64.StdEncoding.DecodeString(r.Content["file"].(map[string]any)["content_base64"].(string))
	if string(body) != "<p>&lt;i&gt;</p>" || r.Content["mode"] != "preview" {
		t.Fatalf("body=%q mode=%v", body, r.Content["mode"])
	}
}

func TestBufferReferenceIsReturned(t *testing.T) {
	var uploads []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		uploads = append(uploads, m)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"reference":{"type":"buffer","key":"artifact:k` + string(rune('0'+len(uploads))) + `"}}`))
	}))
	defer srv.Close()
	deps := Deps{Compiler: typsttest.Runner(t), Buffer: &buffer.Client{URL: srv.URL, Token: "tok", Namespace: "artifact", TTL: 60, HTTP: srv.Client()}}
	r := call(t, deps, "artifact_render_v1", goldenArgs(t, "quote", "", "pdf", "preview"))
	if r.IsError {
		t.Fatalf("%v", r.Content)
	}
	file := r.Content["file"].(map[string]any)
	if file["ref"] == nil || file["content_base64"] != nil {
		t.Fatalf("file must be a buffer reference: %v", file)
	}
	outs := r.Content["outputs"].([]any)
	pages := int(r.Content["pages"].(float64))
	if len(outs) != 1+pages || outs[0].(map[string]any)["ref"] != file["ref"] || outs[0].(map[string]any)["mime_type"] != "application/pdf" {
		t.Fatalf("outputs must list the file and every preview: %v", outs)
	}
	refs := map[string]bool{}
	for i, o := range outs {
		m := o.(map[string]any)
		ref, _ := m["ref"].(string)
		if !strings.HasPrefix(ref, "buffer://") || refs[ref] || m["bytes"] == nil || m["sha256"] == nil || m["metadata"].(map[string]any)["filename"] == nil {
			t.Fatalf("output %d malformed or duplicated: %v", i, m)
		}
		refs[ref] = true
		if i > 0 && m["mime_type"] != "image/png" {
			t.Fatalf("preview %d mime: %v", i, m["mime_type"])
		}
	}
	if len(uploads) != 1+pages || uploads[0]["namespace"] != "artifact" {
		t.Fatalf("uploads: %v", uploads)
	}
}

func TestCapabilitiesDeclareEngines(t *testing.T) {
	r := call(t, Deps{Compiler: typsttest.Runner(t)}, "artifact_capabilities_v1", nil)
	engines := r.Content["engines"].([]any)
	first := engines[0].(map[string]any)
	if first["name"] != "typst" || first["version"] != "0.15.1" || first["available"] != true || r.Content["template_contract"] != "agentmaurice.template/v1" {
		t.Fatalf("capabilities: %v", r.Content)
	}
}

// bigPreviewCompiler writes three 2 MiB "PNG" pages: more than the inline preview budget.
type bigPreviewCompiler struct{}

func (bigPreviewCompiler) Version(context.Context) (string, error) { return template.TypstVersion, nil }
func (bigPreviewCompiler) Compile(_ context.Context, req template.CompileRequest) error {
	if req.Format == "pdf" {
		return os.WriteFile(filepath.Join(req.OutDir, "out.pdf"), []byte("%PDF-1.7"), 0o644)
	}
	for i := 1; i <= 3; i++ {
		if err := os.WriteFile(filepath.Join(req.OutDir, fmt.Sprintf("page-%d.png", i)), make([]byte, 2<<20), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Finding 3: required previews are never silently dropped.
func TestPreviewsOverBudgetFailWithoutBuffer(t *testing.T) {
	r := call(t, Deps{Compiler: bigPreviewCompiler{}}, "artifact_render_v1", goldenArgs(t, "quote", "", "pdf", "preview"))
	if !r.IsError || r.Content["error"] != "storage_required" {
		t.Fatalf("want storage_required, got %v", r.Content)
	}
}

// Pass 2, finding 3: html and txt have no preview, and the contract says so.
func TestHTMLHasNoPreviewByContract(t *testing.T) {
	caps := call(t, Deps{}, "artifact_capabilities_v1", nil)
	for _, e := range caps.Content["engines"].([]any) {
		m := e.(map[string]any)
		want := "none"
		if m["name"] == "typst" {
			want = "png_per_page"
		}
		if m["previews"] != want {
			t.Fatalf("engine %v: previews=%v, want %s", m["name"], m["previews"], want)
		}
	}
	args := goldenArgs(t, "quote", "", "html", "preview")
	args["template"] = map[string]any{
		"manifest": map[string]any{"schema": "agentmaurice.template/v1", "key": "page-test", "version": "1.0.0", "kind": "page", "profile": "web",
			"source": map[string]any{"engine": "html", "entry": "main.html"}, "outputs": []any{"html"}, "default_locale": "fr",
			"input_schema": "input.schema.json", "examples": []any{map[string]any{"name": "default", "data": "examples/default.json"}}},
		"files": map[string]any{"main.html": "<p>x</p>", "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"},
	}
	args["data"] = map[string]any{}
	r := call(t, Deps{}, "artifact_render_v1", args)
	if r.IsError || r.Content["pages"].(float64) != 0 || len(r.Content["previews"].([]any)) != 0 {
		t.Fatalf("html result: %v", r.Content)
	}
}

// Pass 10, finding 3: an empty render is a valid result with inline empty content.
func TestEmptyRenderCarriesEmptyContent(t *testing.T) {
	args := goldenArgs(t, "quote", "", "txt", "preview")
	args["template"] = map[string]any{
		"manifest": map[string]any{"schema": "agentmaurice.template/v1", "key": "text-test", "version": "1.0.0", "kind": "page", "profile": "web",
			"source": map[string]any{"engine": "text", "entry": "main.txt"}, "outputs": []any{"txt"}, "default_locale": "fr",
			"input_schema": "input.schema.json", "examples": []any{map[string]any{"name": "default", "data": "examples/default.json"}}},
		"files": map[string]any{"main.txt": "{{ if .data.flag }}x{{ end }}", "input.schema.json": `{"type":"object"}`, "examples/default.json": "{}"},
	}
	args["data"] = map[string]any{"flag": false}
	r := call(t, Deps{}, "artifact_render_v1", args)
	if r.IsError {
		t.Fatalf("%v", r.Content)
	}
	file := r.Content["file"].(map[string]any)
	content, present := file["content_base64"]
	if !present || content != "" || file["bytes"].(float64) != 0 ||
		file["sha256"] != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty result must carry empty inline content: %v", file)
	}
	if r.Content["artifact"].(map[string]any)["content_base64"] != "" {
		t.Fatalf("legacy artifact lost its empty content: %v", r.Content["artifact"])
	}
}

// Pass 11, finding 2: the deadline covers the Buffer uploads.
func TestDeadlineCoversBufferUploads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // a slow Buffer: 400 ms per upload
		case <-time.After(400 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"reference":{"type":"buffer","key":"artifact:k"}}`))
	}))
	defer srv.Close()
	deps := Deps{Compiler: bigPreviewCompiler{}, Options: template.Options{Timeout: 700 * time.Millisecond},
		Buffer: &buffer.Client{URL: srv.URL, Token: "tok", Namespace: "artifact", TTL: 60, HTTP: &http.Client{Timeout: 30 * time.Second}}}
	start := time.Now()
	r := call(t, deps, "artifact_render_v1", goldenArgs(t, "quote", "", "pdf", "preview"))
	elapsed := time.Since(start)
	if !r.IsError || r.Content["error"] != template.CodeRenderTimeout {
		t.Fatalf("want render_timeout, got %v", r.Content)
	}
	if elapsed > 700*time.Millisecond+3*time.Second {
		t.Fatalf("took %s", elapsed)
	}
}

// A v0.1 call without template keeps the response of origin/main: same
// structure, fields, bytes and error codes (testdata/v01_origin_main.json).
func TestV01ResponsesMatchOriginMain(t *testing.T) {
	var calls []struct {
		Name string         `json:"name"`
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	raw, err := os.ReadFile("../../testdata/v01_calls.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &calls); err != nil {
		t.Fatal(err)
	}
	var golden map[string]json.RawMessage
	raw, err = os.ReadFile("../../testdata/v01_origin_main.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	srv := NewWithDeps(Deps{}).Legacy()
	for i, c := range calls {
		key := fmt.Sprintf("%02d-%s", i, c.Name)
		msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": c.Tool, "arguments": c.Args}})
		got, _ := json.Marshal(srv.HandleMessage(context.Background(), msg))
		var a, b any
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal(golden[key], &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s differs from origin/main:\n got  %.400s\n want %.400s", key, got, golden[key])
		}
	}
}

// AGENT-1789: every error code emitted by the code is declared in
// artifact_capabilities_v1, and every declared code is emitted, both ways.
// The packages internal/template and internal/mcpserver are type-checked
// (go/types). The code argument of every error constructor (failure(), errf()
// and every template.Error literal, resolved by type, and every use of its Code field other than a read or a simple checked assignment) must be a constant, whatever the file
// that declares it; the test fails naming the call otherwise, unless the
// expression is one of the explicit, resolved exceptions of checkCode:
//   - the parameter `code` (types.Object) of the function errf of errors.go,
//     used in its body;
//   - the parameter `code` (types.Object) of the flag closure of typstctx.go,
//     whose call sites are checked;
//   - the selector terr.Code (field Code of template.Error) inside
//     (*handlers).renderTemplate, which forwards a *template.Error built by errf().
// A local variable named code, or terr.Code elsewhere, is refused.

// scanImporter resolves internal/template from the parsed sources and the
// rest through the source importer; unresolvable imports become empty packages
// (their errors are ignored, only constants matter here).
type scanImporter struct {
	std  types.Importer
	pkgs map[string]*types.Package
}

func (i scanImporter) Import(path string) (*types.Package, error) {
	if p, ok := i.pkgs[path]; ok {
		return p, nil
	}
	if p, err := i.std.Import(path); err == nil {
		return p, nil
	}
	p := types.NewPackage(path, filepath.Base(path))
	p.MarkComplete()
	return p, nil
}

// isErrorConstructor reports whether obj is the errf function of internal/template
// or the failure function of internal/mcpserver.
func isErrorConstructor(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return (fn.Name() == "errf" && fn.Pkg().Name() == "template") || (fn.Name() == "failure" && fn.Pkg().Name() == "mcpserver")
}

// isTemplateErrorValue reports whether typ is template.Error itself (not a pointer).
func isTemplateErrorValue(typ types.Type) bool {
	if typ == nil {
		return false
	}
	if _, ok := typ.(*types.Pointer); ok {
		return false
	}
	return isTemplateError(typ)
}

// isTemplateError reports whether typ is template.Error or a pointer to it.
func isTemplateError(typ types.Type) bool {
	if p, ok := typ.(*types.Pointer); ok {
		typ = p.Elem()
	}
	n, ok := typ.(*types.Named)
	return ok && n.Obj().Name() == "Error" && n.Obj().Pkg() != nil && strings.HasSuffix(n.Obj().Pkg().Path(), "/internal/template")
}

func TestErrorCodeCatalogIsExhaustive(t *testing.T) {
	const tplPath = "github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	emitted := map[string]bool{}
	fset := token.NewFileSet()
	imp := scanImporter{std: importer.ForCompiler(fset, "source", nil), pkgs: map[string]*types.Package{}}
	for _, d := range []struct{ dir, path string }{{"../template", tplPath}, {".", tplPath + "/../mcpserver"}} {
		pkgs, err := parser.ParseDir(fset, d.dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, pkg := range pkgs {
			var files []*ast.File
			for _, f := range pkg.Files {
				files = append(files, f)
			}
			info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
			conf := types.Config{Importer: imp, Error: func(error) {}}
			tp, _ := conf.Check(d.path, fset, files, info)
			imp.pkgs[d.path] = tp
			// Explicit exceptions, bound to resolved objects and enclosing functions.
			allowedParams := map[types.Object]bool{}
			type span struct{ from, to token.Pos }
			var renderTemplateSpan []span
			addParams := func(ft *ast.FuncType) {
				for _, fl := range ft.Params.List {
					for _, id := range fl.Names {
						if id.Name == "code" {
							allowedParams[info.Defs[id]] = true
						}
					}
				}
			}
			for _, f := range files {
				base := filepath.Base(fset.Position(f.Pos()).Filename)
				ast.Inspect(f, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.FuncDecl:
						if base == "errors.go" && x.Name.Name == "errf" && x.Recv == nil {
							addParams(x.Type)
						}
						if base == "render_template.go" && x.Name.Name == "renderTemplate" && x.Recv != nil && x.Body != nil {
							renderTemplateSpan = append(renderTemplateSpan, span{x.Body.Pos(), x.Body.End()})
						}
					case *ast.AssignStmt:
						if base == "typstctx.go" && len(x.Lhs) == 1 && len(x.Rhs) == 1 {
							if id, ok := x.Lhs[0].(*ast.Ident); ok && id.Name == "flag" {
								if fl, ok := x.Rhs[0].(*ast.FuncLit); ok {
									addParams(fl.Type)
								}
							}
						}
					}
					return true
				})
			}
			check := func(arg ast.Expr, what string) {
				if tv, ok := info.Types[arg]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
					emitted[constant.StringVal(tv.Value)] = true
					return
				}
				switch e := ast.Unparen(arg).(type) {
				case *ast.Ident:
					if allowedParams[info.Uses[e]] {
						return
					}
				case *ast.SelectorExpr:
					if sl, ok := info.Selections[e]; ok && sl.Obj().Name() == "Code" && isTemplateError(sl.Recv()) {
						for _, sp := range renderTemplateSpan {
							if e.Pos() >= sp.from && e.End() <= sp.to {
								return
							}
						}
					}
				}
				var sb strings.Builder
				_ = printer.Fprint(&sb, fset, arg)
				t.Errorf("%s: the code of %s is not a constant: %s (use a constant; the only exceptions are listed in the comment of this test)", fset.Position(arg.Pos()), what, sb.String())
			}
			// isCodeField: the selector designates the Code field of template.Error
			// (also through an embedding), resolved by go/types.
			isCodeField := func(e ast.Expr) bool {
				sel, ok := e.(*ast.SelectorExpr)
				if !ok {
					return false
				}
				s, ok := info.Selections[sel]
				if !ok {
					return false
				}
				v, ok := s.Obj().(*types.Var)
				return ok && v.IsField() && v.Name() == "Code" && v.Pkg() != nil && strings.HasSuffix(v.Pkg().Path(), "/internal/template")
			}
			directCalls := map[*ast.Ident]bool{}
			defer func() {
				// Any other use (function value assigned, passed, stored) escapes the check.
				for id, obj := range info.Uses {
					if isErrorConstructor(obj) && !directCalls[id] {
						t.Errorf("%s: %s is used other than by a direct call (function value): its code argument cannot be checked", fset.Position(id.Pos()), id.Name)
					}
				}
			}()
			for _, f := range files {
				ast.Inspect(f, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						// By resolved object: (errf)("x"), parenthesized or not.
						var id *ast.Ident
						switch fn := ast.Unparen(x.Fun).(type) {
						case *ast.Ident:
							id = fn
						case *ast.SelectorExpr:
							id = fn.Sel
						}
						if id != nil && id.Name == "new" && len(x.Args) == 1 && isTemplateErrorValue(info.Types[x.Args[0]].Type) {
							t.Errorf("%s: new(template.Error) creates an empty code", fset.Position(x.Pos()))
						}
						// Forwarder: the flag closure of typstctx.go passes its third argument to errf.
						if id != nil && id.Name == "flag" && filepath.Base(fset.Position(id.Pos()).Filename) == "typstctx.go" && len(x.Args) == 4 {
							check(x.Args[2], "flag()")
						}
						if id != nil && isErrorConstructor(info.Uses[id]) {
							directCalls[id] = true
							if len(x.Args) > 0 {
								check(x.Args[0], id.Name+"()")
							}
						}
					case *ast.CompositeLit:
						// Resolved type, not the shape of the AST: Error{}, &Error{}, template.Error{}.
						if !isTemplateError(info.Types[x].Type) {
							return true
						}
						hasCode := false
						for i, e := range x.Elts {
							if kv, ok := e.(*ast.KeyValueExpr); ok {
								if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Code" {
									hasCode = true
									check(kv.Value, "an Error literal")
								}
							} else if i == 0 { // unkeyed literal: Code is the first field
								hasCode = true
								check(e, "an Error literal")
							}
						}
						if !hasCode {
							t.Errorf("%s: a template.Error literal without Code (empty code)", fset.Position(x.Pos()))
						}
					case *ast.ValueSpec:
						// var e Error: zero value, not followed: refused (use a pointer or a literal with Code).
						if len(x.Values) == 0 && x.Type != nil && isTemplateErrorValue(info.Types[x.Type].Type) {
							t.Errorf("%s: a zero template.Error value is declared (empty code)", fset.Position(x.Pos()))
						}
					case *ast.AssignStmt:
						for i, lhs := range x.Lhs {
							if !isCodeField(ast.Unparen(lhs)) {
								continue
							}
							if x.Tok != token.ASSIGN || len(x.Lhs) != len(x.Rhs) {
								t.Errorf("%s: Error.Code is assigned other than by a simple single assignment (%s)", fset.Position(lhs.Pos()), x.Tok)
								continue
							}
							check(x.Rhs[i], "an assignment to Error.Code")
						}
					case *ast.IncDecStmt:
						if isCodeField(ast.Unparen(x.X)) {
							t.Errorf("%s: Error.Code is modified by %s", fset.Position(x.Pos()), x.Tok)
						}
					case *ast.RangeStmt:
						for _, e := range []ast.Expr{x.Key, x.Value} {
							if e != nil && isCodeField(ast.Unparen(e)) {
								t.Errorf("%s: Error.Code is a range variable", fset.Position(e.Pos()))
							}
						}
					case *ast.UnaryExpr:
						if x.Op == token.AND && isCodeField(ast.Unparen(x.X)) {
							t.Errorf("%s: the address of Error.Code is taken: it could be written through a pointer", fset.Position(x.Pos()))
						}
					}
					return true
				})
			}
		}
	}
	// Only constants used by a checked call, literal or assignment are emitted: a
	// declared but unused Code* constant is not.
	if len(emitted) < 30 {
		t.Fatalf("only %d emitted codes found, the scan is broken: %v", len(emitted), emitted)
	}
	caps := call(t, Deps{}, "artifact_capabilities_v1", nil)
	declared := map[string]bool{}
	for _, c := range caps.Content["error_codes"].([]any) {
		declared[c.(string)] = true
	}
	for c := range emitted {
		if !declared[c] {
			t.Errorf("code %q is emitted but missing from error_codes", c)
		}
	}
	for c := range declared {
		if !emitted[c] {
			t.Errorf("code %q is declared in error_codes but never emitted", c)
		}
	}
	if !declared["typst_escaped_action"] {
		t.Error("typst_escaped_action must be declared")
	}
}

// The catalogue test checks the codes at every call of failure. This test checks the other half: failure emits exactly the
// code it is given (AGENT-1789).
func TestFailureKeepsTheCodeItIsGiven(t *testing.T) {
	for _, code := range []string{"probe_code_b", "invalid_arguments"} {
		res := failure(code, "msg")
		m, _ := res.StructuredContent.(map[string]interface{})
		if !res.IsError || m["error"] != code || m["message"] != "msg" {
			t.Fatalf("failure(%q) = %+v, want IsError and the same code", code, res)
		}
	}
}

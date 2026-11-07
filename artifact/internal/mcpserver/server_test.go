package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

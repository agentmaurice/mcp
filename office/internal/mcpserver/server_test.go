package mcpserver

import (
	"archive/zip"
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
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/office/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/office/internal/engine"
)

type toolResult struct {
	IsError bool
	Content map[string]any
}

func fakeCLI(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../testdata/fake-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func testDeps(t *testing.T) (Deps, string) {
	t.Helper()
	root := t.TempDir()
	r := engine.New(fakeCLI(t), fakeCLI(t), 20*time.Second, 2)
	r.TempRoot = root
	return Deps{Engine: r, MaxInputBytes: 1 << 20, EngineVersion: "0.12.852"}, root
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

func zipB64(extra string) string {
	return base64.StdEncoding.EncodeToString([]byte("PK\x03\x04minimal" + extra))
}

func file(extra string) map[string]any { return map[string]any{"content_base64": zipB64(extra)} }

func mustOK(t *testing.T, r toolResult) map[string]any {
	t.Helper()
	if r.IsError {
		t.Fatalf("unexpected error: %v", r.Content)
	}
	if r.Content["status"] != "ok" {
		t.Fatalf("status: %v", r.Content)
	}
	return r.Content
}

func mustFail(t *testing.T, r toolResult, code string) map[string]any {
	t.Helper()
	if !r.IsError || r.Content["error"] != code {
		t.Fatalf("want error %q, got isError=%v %v", code, r.IsError, r.Content)
	}
	if _, ok := r.Content["message"].(string); !ok {
		t.Fatalf("error without message: %v", r.Content)
	}
	return r.Content
}

func decodeFile(t *testing.T, content map[string]any) []byte {
	t.Helper()
	f := content["file"].(map[string]any)
	data, err := base64.StdEncoding.DecodeString(f["content_base64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if int(f["bytes"].(float64)) != len(data) || f["sha256"] != hexSHA(data) {
		t.Fatalf("file metadata out of sync: %v", f)
	}
	return data
}

func TestHealthAndCapabilities(t *testing.T) {
	deps, _ := testDeps(t)
	h := mustOK(t, call(t, deps, "office_health_v1", nil))
	_ = h
	r := call(t, deps, "office_health_v1", nil)
	if r.Content["version"] != serviceVersion {
		t.Fatalf("health: %v", r.Content)
	}
	c := call(t, deps, "office_capabilities_v1", nil).Content
	eng := c["engine"].(map[string]any)
	if eng["name"] != "GenOffice" || eng["license"] != "Apache-2.0" || eng["available"] != true || eng["version"] != "0.12.852" {
		t.Fatalf("engine: %v", eng)
	}
	f := c["formats"].(map[string]any)
	for k, want := range map[string]string{"create": "docx,xlsx", "edit": "docx,xlsx", "compare": "docx", "inspect": "docx,xlsx"} {
		var got []string
		for _, v := range f[k].([]any) {
			got = append(got, v.(string))
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("formats.%s = %v, want %s", k, got, want)
		}
	}
	if c["limits"].(map[string]any)["max_ops"].(float64) != 200 {
		t.Fatalf("limits: %v", c["limits"])
	}
}

func TestCapabilitiesWithoutEngineOrSidecar(t *testing.T) {
	deps := Deps{Engine: engine.New("/nonexistent/cli", "", time.Second, 1)}
	c := call(t, deps, "office_capabilities_v1", nil).Content
	if c["engine"].(map[string]any)["available"] != false {
		t.Fatalf("engine: %v", c["engine"])
	}
	if got := c["formats"].(map[string]any)["edit"].([]any); len(got) != 1 || got[0] != "docx" {
		t.Fatalf("edit formats without sidecar: %v", got)
	}
	mustFail(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "# x"}), "engine_unavailable")
	mustFail(t, call(t, Deps{}, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "# x"}), "engine_unavailable")
	mustFail(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "xlsx", "file": file("")}), "unsupported_format")
}

// The engine name appears in office_capabilities_v1 only.
func TestToolDescriptionsDoNotNameTheEngine(t *testing.T) {
	deps, _ := testDeps(t)
	srv := NewWithDeps(deps).Legacy()
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	raw, _ := json.Marshal(srv.HandleMessage(context.Background(), msg))
	if strings.Contains(strings.ToLower(string(raw)), "genoffice") || strings.Contains(strings.ToLower(string(raw)), "gen office") {
		t.Fatalf("tool list names the engine: %s", raw)
	}
	for _, name := range []string{"office_health_v1", "office_capabilities_v1", "office_create_v1", "office_edit_v1", "office_compare_v1", "office_inspect_v1"} {
		if !strings.Contains(string(raw), `"`+name+`"`) {
			t.Fatalf("tool %s missing", name)
		}
	}
}

func TestCreate(t *testing.T) {
	deps, root := testDeps(t)
	r := mustOK(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "# Hello", "filename": "../../etc/Quote.docx"}))
	data := decodeFile(t, r)
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) || !strings.Contains(string(data), "create-docx") {
		t.Fatalf("content: %q", data)
	}
	f := r["file"].(map[string]any)
	if f["filename"] != "Quote.docx" || f["mime_type"] != mimeDocx {
		t.Fatalf("file: %v", f)
	}
	x := mustOK(t, call(t, deps, "office_create_v1", map[string]any{"format": "xlsx", "source_format": "json", "content": `[["a",1]]`}))
	if x["file"].(map[string]any)["filename"] != "document.xlsx" || x["file"].(map[string]any)["mime_type"] != mimeXlsx || x["sheets"].(float64) != 1 {
		t.Fatalf("xlsx: %v", x)
	}
	h := mustOK(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "html", "content": "<h1>x</h1>"}))
	_ = h
	// the working directory is removed after each call
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("working directories left behind: %v", entries)
	}
}

func TestCreateValidation(t *testing.T) {
	deps, _ := testDeps(t)
	bad := func(args map[string]any, code string) {
		t.Helper()
		mustFail(t, call(t, deps, "office_create_v1", args), code)
	}
	bad(map[string]any{"format": "docx", "source_format": "json", "content": "{}"}, "unsupported_format")
	bad(map[string]any{"format": "xlsx", "source_format": "markdown", "content": "| a |"}, "unsupported_format")
	bad(map[string]any{"format": "pptx", "source_format": "json", "content": "{}"}, "unsupported_format")
	bad(map[string]any{"format": "xlsx", "source_format": "json", "content": "{not json"}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "source_format": "markdown", "content": "   "}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "source_format": "markdown", "content": 12}, "invalid_arguments")
	deps.MaxContentBytes = 10
	bad(map[string]any{"format": "docx", "source_format": "markdown", "content": strings.Repeat("a", 11)}, "file_too_large")
}

func TestEditDocx(t *testing.T) {
	deps, _ := testDeps(t)
	ops := []any{map[string]any{"op": "findReplace", "find": "30", "replace": "45"}, map[string]any{"op": "deleteBlocks", "target": map[string]any{"blockIndexes": []int{3}}}}
	r := mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": realDocx(t, "minimal.docx"), "ops": ops, "track": true, "author": "Alice\n", "filename": "devis-v2"}))
	if r["applied"].(float64) != 2 {
		t.Fatalf("applied: %v", r["applied"])
	}
	if n, err := trackedChangeTotal(decodeFile(t, r)); err != nil || n == 0 {
		t.Fatalf("tracked docx has no revisions: %d %v", n, err)
	}
	data := string(decodeFile(t, r))
	if !strings.Contains(data, "docs-apply-track1-Alice") {
		t.Fatalf("track and author not passed: %q", data)
	}
	if r["file"].(map[string]any)["filename"] != "devis-v2.docx" {
		t.Fatalf("filename: %v", r["file"])
	}
	r = mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": file(""), "ops": ops}))
	if !strings.Contains(string(decodeFile(t, r)), "docs-apply-track0-") {
		t.Fatal("track must default to false")
	}
}

func TestEditValidation(t *testing.T) {
	deps, _ := testDeps(t)
	one := []any{map[string]any{"op": "findReplace", "find": "a", "replace": "b"}}
	bad := func(args map[string]any, code string) map[string]any {
		t.Helper()
		return mustFail(t, call(t, deps, "office_edit_v1", args), code)
	}
	many := make([]any, 201)
	for i := range many {
		many[i] = one[0]
	}
	bad(map[string]any{"format": "docx", "file": file(""), "ops": many}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "file": file(""), "ops": []any{}}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "file": file(""), "ops": []any{map[string]any{"op": "insert_image", "url": "https://example.com/a.png"}}}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "file": file(""), "ops": []any{map[string]any{"op": "remove_parts"}}}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "file": file(""), "ops": []any{map[string]any{"op": "insert_content", "html": `<p>x</p><IMG src="http://example.com/x.png">`}}}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "file": file(""), "ops": []any{map[string]any{"find": "a"}}}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "file": file(""), "ops": one, "track": "yes"}, "invalid_arguments")
	bad(map[string]any{"format": "xlsx", "file": file(""), "ops": one, "track": true}, "invalid_arguments")
	bad(map[string]any{"format": "pptx", "file": file(""), "ops": one}, "unsupported_format")
	// a file needs exactly one of content_base64 or ref
	bad(map[string]any{"format": "docx", "file": map[string]any{}, "ops": one}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "file": map[string]any{"content_base64": zipB64(""), "ref": "buffer://k"}, "ops": one}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "file": map[string]any{"content_base64": "***"}, "ops": one}, "invalid_arguments")
	bad(map[string]any{"format": "docx", "file": map[string]any{"content_base64": base64.StdEncoding.EncodeToString([]byte("hello"))}, "ops": one}, "invalid_document")
	bad(map[string]any{"format": "docx", "file": map[string]any{"ref": "buffer://k"}, "ops": one}, "buffer_unavailable")
	deps.MaxInputBytes = 16
	bad(map[string]any{"format": "docx", "file": file(strings.Repeat("a", 64)), "ops": one}, "file_too_large")
}

func TestEditRejectionIsStableAndScrubbed(t *testing.T) {
	deps, root := testDeps(t)
	r := call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": file(""), "ops": []any{map[string]any{"op": "findReplace", "find": "REJECT", "replace": "x"}}})
	c := mustFail(t, r, "op_rejected")
	raw, _ := json.Marshal(c)
	s := strings.ToLower(string(raw))
	if strings.Contains(s, "genoffice") || strings.Contains(string(raw), root) || strings.Contains(s, "suggestion") {
		t.Fatalf("error leaks engine internals: %s", raw)
	}
	rejected := c["details"].(map[string]any)["rejected_ops"].([]any)
	if len(rejected) != 1 || rejected[0].(map[string]any)["reason"] != "target_not_found" || !strings.Contains(rejected[0].(map[string]any)["message"].(string), "<work>") {
		t.Fatalf("details: %v", c["details"])
	}
}

func TestEditXlsx(t *testing.T) {
	deps, _ := testDeps(t)
	cells := []any{map[string]any{"cell": "A30", "value": "hello"}, map[string]any{"cell": "E30", "formula": "=B30*2"}}
	r := mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": file(""), "ops": cells}))
	if r["file"].(map[string]any)["mime_type"] != mimeXlsx || !strings.Contains(string(decodeFile(t, r)), "sheet-apply") {
		t.Fatalf("result: %v", r)
	}
	dsl := []any{map[string]any{"op": "set_cell", "address": "A1", "value": 3}}
	r = mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": file(""), "ops": dsl}))
	if r["applied"].(float64) != 1 {
		t.Fatalf("applied: %v", r)
	}
	mustFail(t, call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": file(""), "ops": append(cells, dsl...)}), "invalid_arguments")
	mustFail(t, call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": file(""), "ops": []any{map[string]any{"op": "add_image"}}}), "invalid_arguments")
}

func TestCompare(t *testing.T) {
	deps, _ := testDeps(t)
	r := mustOK(t, call(t, deps, "office_compare_v1", map[string]any{"format": "docx", "before": file("a"), "after": file("b"), "author": "Bob"}))
	ch := r["changes"].(map[string]any)
	if ch["insertions"].(float64) != 2 || ch["deletions"].(float64) != 3 {
		t.Fatalf("changes: %v", ch)
	}
	if !strings.Contains(string(decodeFile(t, r)), "compare-Bob") {
		t.Fatal("author not passed")
	}
	r = mustOK(t, call(t, deps, "office_compare_v1", map[string]any{"before": file("a"), "after": file("b")}))
	if !strings.Contains(string(decodeFile(t, r)), "compare-Office") {
		t.Fatal("default author must not name the engine")
	}
	mustFail(t, call(t, deps, "office_compare_v1", map[string]any{"format": "xlsx", "before": file(""), "after": file("")}), "unsupported_format")
	mustFail(t, call(t, deps, "office_compare_v1", map[string]any{"before": file(""), "after": map[string]any{}}), "invalid_arguments")
}

func TestInspect(t *testing.T) {
	deps, _ := testDeps(t)
	d := mustOK(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "docx", "file": file("")}))
	if !strings.Contains(d["text"].(string), "Hello world") {
		t.Fatalf("text: %v", d["text"])
	}
	st := d["structure"].(map[string]any)
	if st["format"] != "docx" || st["blocks"].(float64) != 4 || len(st["headings"].([]any)) != 1 {
		t.Fatalf("structure: %v", st)
	}
	if _, leaked := st["properties"]; leaked {
		t.Fatal("document properties (application name) must not be returned")
	}
	if issues := d["issues"].([]any); len(issues) != 1 || issues[0].(map[string]any)["check"] != "placeholder_left" {
		t.Fatalf("issues: %v", d["issues"])
	}
	x := mustOK(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "xlsx", "file": file("")}))
	if !strings.Contains(x["text"].(string), "## Devis") || !strings.Contains(x["text"].(string), "L01\t900") {
		t.Fatalf("xlsx text: %q", x["text"])
	}
	sheets := x["structure"].(map[string]any)["sheets"].([]any)
	if len(sheets) != 2 || sheets[0].(map[string]any)["formulas"].(float64) != 1 {
		t.Fatalf("sheets: %v", sheets)
	}
	if len(x["issues"].([]any)) != 0 {
		t.Fatalf("issues: %v", x["issues"])
	}
}

func TestEngineEnvironmentIsMinimal(t *testing.T) {
	t.Setenv("OFFICE_TEST_SECRET_KEY", "must-not-leak")
	t.Setenv("OFFICE_BUFFER_TOKEN", "must-not-leak")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	deps, _ := testDeps(t)
	r := mustOK(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "ENVDUMP"}))
	dump := string(decodeFile(t, r))
	if strings.Contains(dump, "must-not-leak") || strings.Contains(dump, "proxy.invalid") {
		t.Fatalf("secret or proxy leaked into the engine environment:\n%s", dump)
	}
	var pwd, home, roots string
	for _, line := range strings.Split(dump, "\n") {
		switch {
		case strings.HasPrefix(line, "PWD="):
			pwd = strings.TrimPrefix(line, "PWD=")
		case strings.HasPrefix(line, "HOME="):
			home = strings.TrimPrefix(line, "HOME=")
		case strings.HasPrefix(line, "GENOFFICE_ALLOWED_ROOTS="):
			roots = strings.TrimPrefix(line, "GENOFFICE_ALLOWED_ROOTS=")
		}
	}
	if pwd == "" || !strings.HasPrefix(home, pwd) || roots != pwd {
		t.Fatalf("directory isolation: pwd=%q home=%q roots=%q", pwd, home, roots)
	}
	for _, want := range []string{"ELECTRON_SKIP_BINARY_DOWNLOAD=1", "DO_NOT_TRACK=1", "XLSX_SIDECAR_PATH=" + fakeCLI(t)} {
		if !strings.Contains(dump, want) {
			t.Fatalf("missing %s in the engine environment", want)
		}
	}
}

func TestEngineFailuresAreTranslated(t *testing.T) {
	deps, _ := testDeps(t)
	create := func(content string) toolResult {
		return call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": content})
	}
	mustFail(t, create("CRASH"), "engine_failed")
	mustFail(t, create("GARBAGE"), "engine_failed")
	// an exit code 2 (file) is an invalid document
	mustFail(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": file("MISSING"), "ops": []any{map[string]any{"op": "findReplace", "find": "a", "replace": "b"}}}), "invalid_document")
}

func TestEngineTimeoutKillsTheProcess(t *testing.T) {
	deps, root := testDeps(t)
	deps.Engine.Timeout = 1 * time.Second
	start := time.Now()
	r := call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "SLEEP"})
	mustFail(t, r, "engine_timeout")
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the call outlived its deadline: %s", time.Since(start))
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("working directory left behind: %v", entries)
	}
}

func TestLargeOutputNeedsStorage(t *testing.T) {
	deps, _ := testDeps(t)
	deps.MaxInlineBytes = 100
	mustFail(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "x"}), "storage_required")
}

// bufferStub is a minimal Buffer service: POST stores, GET /<key> loads.
type bufferStub struct {
	store map[string][]byte
	posts int
}

func newBufferStub(t *testing.T) (*bufferStub, *buffer.Client) {
	t.Helper()
	stub := &bufferStub{store: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var in struct {
				Text, ToolName, MimeType string
				Metadata                 map[string]any
			}
			body, _ := io.ReadAll(r.Body)
			var raw map[string]any
			_ = json.Unmarshal(body, &raw)
			in.Text, _ = raw["text"].(string)
			in.Metadata, _ = raw["metadata"].(map[string]any)
			if in.Metadata["encoding"] != "base64" || in.Metadata["filename"] == "" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			data, _ := base64.StdEncoding.DecodeString(in.Text)
			stub.posts++
			key := "office/k" + string(rune('0'+stub.posts))
			stub.store[key] = data
			_ = json.NewEncoder(w).Encode(map[string]any{"reference": map[string]any{"key": key}})
		case http.MethodGet:
			key, _ := strings.CutPrefix(r.URL.EscapedPath(), "/")
			key = strings.ReplaceAll(key, "%2F", "/")
			data, ok := stub.store[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"content": map[string]any{"binary": base64.StdEncoding.EncodeToString(data)}})
		}
	}))
	t.Cleanup(srv.Close)
	return stub, &buffer.Client{URL: srv.URL, Token: "tok", Namespace: "office", TTL: 60, HTTP: srv.Client()}
}

func TestBufferInputAndOutput(t *testing.T) {
	deps, _ := testDeps(t)
	stub, client := newBufferStub(t)
	deps.Buffer = client
	stub.store["office/in"] = []byte("PK\x03\x04from-buffer")
	r := mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": map[string]any{"ref": "buffer://office/in"}, "ops": []any{map[string]any{"op": "findReplace", "find": "a", "replace": "b"}}}))
	f := r["file"].(map[string]any)
	if _, inline := f["content_base64"]; inline || !strings.HasPrefix(f["ref"].(string), "buffer://office/") {
		t.Fatalf("file: %v", f)
	}
	outs := r["outputs"].([]any)
	if len(outs) != 1 {
		t.Fatalf("outputs: %v", outs)
	}
	o := outs[0].(map[string]any)
	if o["ref"] != f["ref"] || o["mime_type"] != mimeDocx || o["sha256"] != f["sha256"] || o["metadata"].(map[string]any)["filename"] != "document.docx" {
		t.Fatalf("output entry: %v", o)
	}
	stored := stub.store[strings.TrimPrefix(f["ref"].(string), "buffer://")]
	if hexSHA(stored) != f["sha256"] {
		t.Fatal("stored bytes differ from the reported sha256")
	}
	// large outputs are fine with a Buffer
	deps.MaxInlineBytes = 100
	mustOK(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "x"}))
	// unknown and invalid references
	mustFail(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "docx", "file": map[string]any{"ref": "buffer://office/gone"}}), "buffer_not_found")
	mustFail(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "docx", "file": map[string]any{"ref": "https://example.com/x.docx"}}), "invalid_arguments")
	stub.store["office/text"] = []byte("not a package")
	mustFail(t, call(t, deps, "office_inspect_v1", map[string]any{"format": "docx", "file": map[string]any{"ref": "buffer://office/text"}}), "invalid_document")
}

func TestBufferFromEnv(t *testing.T) {
	t.Setenv("OFFICE_BUFFER_SERVICE_URL", "")
	t.Setenv("BUFFER_SERVICE_URL", "")
	t.Setenv("BUFFER_TOKEN", "")
	if buffer.FromEnv() != nil {
		t.Fatal("must be nil when not configured")
	}
	t.Setenv("BUFFER_SERVICE_URL", "http://buffer.test/api")
	t.Setenv("BUFFER_TOKEN", "t")
	c := buffer.FromEnv()
	if c == nil || c.Namespace != "office" || c.TTL != 3600 {
		t.Fatalf("client: %+v", c)
	}
	t.Setenv("OFFICE_BUFFER_NAMESPACE", "ns")
	if buffer.FromEnv().Namespace != "ns" {
		t.Fatal("OFFICE_BUFFER_NAMESPACE ignored")
	}
}

func TestCleanFilename(t *testing.T) {
	for in, want := range map[string]string{
		"": "document.docx", "Report": "Report.docx", "Report.docx": "Report.docx", "REPORT.DOCX": "REPORT.docx",
		"../../x/y.docx": "y.docx", `a\b\c`: "c.docx", "bad:na*me?.docx": "badname.docx", "..": "document.docx", "con\x00trol": "control.docx",
	} {
		if got := cleanFilename(in, "docx", "document"); got != want {
			t.Errorf("cleanFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlotWaitIsBoundedByTheCallDeadline(t *testing.T) {
	root := t.TempDir()
	r := engine.New(fakeCLI(t), fakeCLI(t), 3*time.Second, 1)
	r.TempRoot = root
	deps := Deps{Engine: r}
	done := make(chan toolResult)
	go func() {
		done <- call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "SLEEP"})
	}()
	time.Sleep(500 * time.Millisecond)
	r.Timeout = 500 * time.Millisecond // the second call budget: wait + run
	start := time.Now()
	mustFail(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "# x"}), "engine_busy")
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("busy answer took %s", time.Since(start))
	}
	mustFail(t, <-done, "engine_timeout")
}

func TestXlsxSidecarMustBeAnExecutableFile(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	_ = os.WriteFile(plain, []byte("x"), 0o644)
	for name, path := range map[string]string{"missing": filepath.Join(dir, "nope"), "directory": dir, "not executable": plain} {
		deps := Deps{Engine: engine.New(fakeCLI(t), path, time.Second, 1)}
		c := call(t, deps, "office_capabilities_v1", nil).Content
		if got := c["formats"].(map[string]any)["inspect"].([]any); len(got) != 1 {
			t.Fatalf("%s: xlsx inspect still offered: %v", name, got)
		}
		mustFail(t, call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": file(""), "ops": []any{map[string]any{"cell": "A1", "value": 1}}}), "unsupported_format")
	}
}

func TestValidationErrorsDoNotEchoValues(t *testing.T) {
	deps, _ := testDeps(t)
	secret := "GenOffice-/opt/secret-op"
	r := call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": file(""), "ops": []any{map[string]any{"op": secret}}})
	c := mustFail(t, r, "invalid_arguments")
	if strings.Contains(c["message"].(string), "secret") || !strings.Contains(c["message"].(string), "index 0") {
		t.Fatalf("message: %v", c["message"])
	}
	r = call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": secret, "content": "x"})
	if strings.Contains(r.Content["message"].(string), "secret") {
		t.Fatalf("message echoes the value: %v", r.Content)
	}
}

func TestScrubMasksPathsAndEngineName(t *testing.T) {
	for in, want := range map[string]string{
		"cannot execute /opt/office-engine/xlsx-sidecar": "cannot execute <path>",
		`failed at C:\Users\x\a.xlsx now`:                "failed at <path> now",
		"GENOFFICE and Gen Office failed (/tmp/a b)":     "engine and engine failed (<path> b)",
		"open '/var/x': not found, a/b and or stay":      "open '<path>': not found, a/b and or stay",
		"cannot open `/opt/private/document.docx`":       "cannot open `<path>`",
		"see </srv/a.docx> or {/etc/x} or ;/root/y":      "see <<path>> or {<path>} or ;<path>",
	} {
		if got := engine.Scrub(in); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInvalidEngineOutputIsNeverReportedAsADocument(t *testing.T) {
	deps, _ := testDeps(t)
	one := []any{map[string]any{"op": "findReplace", "find": "INVALID", "replace": "x"}}
	mustFail(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "INVALID"}), "invalid_output")
	mustFail(t, call(t, deps, "office_create_v1", map[string]any{"format": "xlsx", "source_format": "json", "content": `["INVALID"]`}), "invalid_output")
	mustFail(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": file(""), "ops": one}), "invalid_output")
	mustFail(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "BADXML"}), "invalid_output")
	mustFail(t, call(t, deps, "office_compare_v1", map[string]any{"before": file("INVALID"), "after": file("")}), "invalid_output")
}

func TestCheckOutput(t *testing.T) {
	good, err := os.ReadFile("../../testdata/minimal.docx")
	if err != nil {
		t.Fatal(err)
	}
	xl, _ := os.ReadFile("../../testdata/minimal.xlsx")
	bad, _ := os.ReadFile("../../testdata/badxml.docx")
	if checkOutput("docx", good, 1<<20) != nil || checkOutput("xlsx", xl, 1<<20) != nil {
		t.Fatal("valid packages rejected")
	}
	for name, c := range map[string]struct {
		format string
		data   []byte
		max    int
	}{
		"empty": {"docx", nil, 100}, "text": {"docx", []byte("document invalide"), 100},
		"too large": {"docx", good, 10}, "truncated zip": {"docx", good[:len(good)/2], 1 << 20},
		"wrong format": {"xlsx", good, 1 << 20}, "invalid xml parts": {"docx", bad, 1 << 20}, "magic only": {"docx", []byte("PK\x03\x04abc"), 100},
	} {
		err := checkOutput(c.format, c.data, c.max)
		if te, ok := err.(*toolError); !ok || te.code != "invalid_output" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestInspectRejectsEmptyEngineAnswers(t *testing.T) {
	deps, _ := testDeps(t)
	for _, format := range []string{"docx", "xlsx"} {
		r := call(t, deps, "office_inspect_v1", map[string]any{"format": format, "file": file("EMPTYANSWER")})
		mustFail(t, r, "invalid_output")
	}
}

func TestAppliedIsReportedNotInvented(t *testing.T) {
	deps, _ := testDeps(t)
	edit := func(find string) toolResult {
		return call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": file(""), "ops": []any{map[string]any{"op": "findReplace", "find": find, "replace": "x"}}})
	}
	// failed:1 with exit code 0 is refused, with the reported counts
	c := mustFail(t, edit("FAILED1"), "op_rejected")
	d := c["details"].(map[string]any)
	if d["failed"].(float64) != 1 || d["applied"].(float64) != 0 || d["skipped"].(float64) != 0 {
		t.Fatalf("details: %v", d)
	}
	c = mustFail(t, edit("SKIPPED1"), "op_rejected")
	if c["details"].(map[string]any)["skipped"].(float64) != 1 {
		t.Fatalf("details: %v", c["details"])
	}
	if _, has := c["file"]; has {
		t.Fatal("no file may be returned for a partial batch")
	}
	// nothing reported: never a success, never the number of requested ops
	mustFail(t, edit("NOBATCH"), "invalid_output")
	mustFail(t, call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": file(""), "ops": []any{map[string]any{"op": "set_cell", "address": "NOBATCH", "value": 1}}}), "invalid_output")
	if mustOK(t, edit("a"))["applied"].(float64) != 1 {
		t.Fatal("reported count lost")
	}
	// workbook cell entries report the cells written
	x := mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": file(""), "ops": []any{map[string]any{"cell": "A1", "value": 1}}}))
	if x["applied"].(float64) != 1 {
		t.Fatalf("xlsx applied: %v", x["applied"])
	}
}

// Every engine answer is typed and checked: a mistyped or incomplete answer is
// invalid_output, never an empty or invented result.
func TestEngineAnswersAreStrictlyValidated(t *testing.T) {
	deps, _ := testDeps(t)
	one := []any{map[string]any{"op": "findReplace", "find": "a", "replace": "b"}}
	cell := []any{map[string]any{"cell": "A1", "value": 1}}
	cases := map[string]func(marker string) toolResult{
		"create": func(m string) toolResult {
			return call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": m})
		},
		"docx info": func(m string) toolResult {
			return call(t, deps, "office_inspect_v1", map[string]any{"format": "docx", "file": file(m)})
		},
		"docx check": func(m string) toolResult {
			return call(t, deps, "office_inspect_v1", map[string]any{"format": "docx", "file": file(m)})
		},
		"xlsx info": func(m string) toolResult {
			return call(t, deps, "office_inspect_v1", map[string]any{"format": "xlsx", "file": file(m)})
		},
		"xlsx check": func(m string) toolResult {
			return call(t, deps, "office_inspect_v1", map[string]any{"format": "xlsx", "file": file(m)})
		},
		"sheet read": func(m string) toolResult {
			return call(t, deps, "office_inspect_v1", map[string]any{"format": "xlsx", "file": file(m)})
		},
		"docs apply": func(m string) toolResult {
			return call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": file(m), "ops": one})
		},
		"sheet apply": func(m string) toolResult {
			return call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": file(m), "ops": cell})
		},
		"compare": func(m string) toolResult {
			return call(t, deps, "office_compare_v1", map[string]any{"before": file(m), "after": file("")})
		},
	}
	markers := map[string][2]string{
		"create": {"BADCREATE", "INCCREATE"}, "docx info": {"BADINFO", "INCINFO"}, "docx check": {"BADCHECK", "INCCHECK"},
		"xlsx info": {"BADINFO", "INCINFO"}, "xlsx check": {"BADCHECK", "INCCHECK"}, "sheet read": {"BADREAD", "INCREAD"},
		"docs apply": {"BADAPPLY", "INCAPPLY"}, "sheet apply": {"BADAPPLY", "INCAPPLY"}, "compare": {"BADTYPE", "INCOMPLETE"},
	}
	for name, run := range cases {
		for i, kind := range []string{"mistyped", "incomplete"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				want := "invalid_output"
				// an inconsistent batch is a partially applied one
				if i == 1 && (name == "docs apply" || name == "sheet apply") {
					want = "op_rejected"
				}
				mustFail(t, run(markers[name][i]), want)
			})
		}
	}
}

func TestCompareChecksTheProducedRevisions(t *testing.T) {
	deps, _ := testDeps(t)
	cmp := func(before string) toolResult {
		return call(t, deps, "office_compare_v1", map[string]any{"before": file(before), "after": file("")})
	}
	// announced changes but no w:ins or w:del in the file
	mustFail(t, cmp("NOREVS"), "invalid_output")
	// zero announced and none present is a valid, empty comparison
	r := mustOK(t, cmp("SAMEDOC"))
	if r["changes"].(map[string]any)["insertions"].(float64) != 0 {
		t.Fatalf("changes: %v", r["changes"])
	}
	// announced changes present in the file
	mustOK(t, cmp(""))
}

func TestVersionAndStatusAreValidated(t *testing.T) {
	dir := t.TempDir()
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, body := range map[string]string{"noversion": `echo "no version here"`, "empty": `true`} {
		deps := Deps{Engine: engine.New(script(name, body), "", 5*time.Second, 1)}
		c := call(t, deps, "office_capabilities_v1", nil).Content
		if c["engine"].(map[string]any)["available"] != false {
			t.Fatalf("%s: engine reported available: %v", name, c["engine"])
		}
	}
	// a `{}` answer (no status) is not a success for any command
	deps := Deps{Engine: engine.New(script("empty-json", `echo '{}'`), "", 5*time.Second, 1)}
	mustFail(t, call(t, deps, "office_create_v1", map[string]any{"format": "docx", "source_format": "markdown", "content": "x"}), "invalid_output")
}

func TestPartiallyAppliedBatchIsRejected(t *testing.T) {
	deps, _ := testDeps(t)
	ops := []any{map[string]any{"op": "findReplace", "find": "PARTIAL", "replace": "x"}, map[string]any{"op": "deleteBlocks"}}
	c := mustFail(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": file(""), "ops": ops}), "op_rejected")
	d := c["details"].(map[string]any)
	if d["requested"].(float64) != 2 || d["applied"].(float64) != 1 || d["total"].(float64) != 1 {
		t.Fatalf("details: %v", d)
	}
	if _, has := c["file"]; has {
		t.Fatal("no file for a partial batch")
	}
}

func TestTrackRequiresTrackedChangesInTheFile(t *testing.T) {
	deps, _ := testDeps(t)
	edit := func(find string) toolResult {
		return call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": realDocx(t, "minimal.docx"), "track": true,
			"ops": []any{map[string]any{"op": "findReplace", "find": find, "replace": "x"}}})
	}
	mustFail(t, edit("NOREVISIONS"), "invalid_output")
	mustOK(t, edit("a"))
}

func trackedChangeTotal(b []byte) (int, error) {
	rc, err := trackedCounts(b)
	return rc.Total(), err
}

func realDocx(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"content_base64": base64.StdEncoding.EncodeToString(b)}
}

func TestXlsxCellCountMustMatchTheRequest(t *testing.T) {
	deps, _ := testDeps(t)
	edit := func(value string) toolResult {
		return call(t, deps, "office_edit_v1", map[string]any{"format": "xlsx", "file": file(""),
			"ops": []any{map[string]any{"cell": "A1", "value": value}, map[string]any{"cell": "B1", "value": 2}}})
	}
	c := mustFail(t, edit("CELLSOFF"), "op_rejected")
	if d := c["details"].(map[string]any); d["requested"].(float64) != 2 || d["cells"].(float64) != 3 {
		t.Fatalf("details: %v", d)
	}
	mustFail(t, edit("NOCELLS"), "invalid_output")
	if mustOK(t, edit("ok"))["applied"].(float64) != 2 {
		t.Fatal("exact count must pass")
	}
}

func TestCompareChecksInsertionsAndDeletionsSeparately(t *testing.T) {
	deps, _ := testDeps(t)
	cmp := func(marker string) toolResult {
		return call(t, deps, "office_compare_v1", map[string]any{"before": file(marker), "after": file("")})
	}
	mustFail(t, cmp("INSZERO"), "invalid_output") // announces 0 insertions, file has w:ins
	mustFail(t, cmp("DELZERO"), "invalid_output") // announces 0 deletions, file has w:del
	mustFail(t, cmp("INSONLY"), "invalid_output") // announces deletions, file has only w:ins
	mustOK(t, cmp(""))
}

func TestTrackMustIncreaseTheTrackedChanges(t *testing.T) {
	deps, _ := testDeps(t)
	one := []any{map[string]any{"op": "findReplace", "find": "a", "replace": "b"}}
	// the input already holds 2 revisions and the output holds the same 2: nothing was tracked
	mustFail(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": realDocx(t, "tracked.docx"), "ops": one, "track": true}), "invalid_output")
	mustOK(t, call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": realDocx(t, "minimal.docx"), "ops": one, "track": true}))
}

func TestTrackWithAcceptAndRejectOps(t *testing.T) {
	deps, _ := testDeps(t)
	edit := func(ops ...any) toolResult {
		return call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": realDocx(t, "tracked.docx"), "track": true, "ops": ops})
	}
	accept := func(author string) any { return map[string]any{"op": "accept_changes", "author": author} }
	reject := map[string]any{"op": "reject_changes", "all": true}
	replace := map[string]any{"op": "findReplace", "find": "a", "replace": "b"}
	// accept + edit: the total does not grow (2 -> 2) but the output holds revisions: valid
	mustOK(t, edit(accept("x"), replace))
	mustOK(t, edit(reject, replace))
	// accept + edit with an output holding no revision at all: invalid
	mustFail(t, edit(accept("NOREVISIONS"), replace), "invalid_output")
	// only accept or reject ops: no revision is required
	mustOK(t, edit(accept("NOREVISIONS")))
	mustOK(t, edit(accept("NOREVISIONS"), reject))
}

func zipWithBody(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	_, _ = w.Write([]byte(body))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRevisionsAreCountedByNamespaceNotPrefix(t *testing.T) {
	const ns = `xmlns:%s="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	count := func(body string) revisionCounts {
		rc, err := trackedCounts(zipWithBody(t, body))
		if err != nil {
			t.Fatal(err)
		}
		return rc
	}
	// a different prefix for the same namespace is counted
	got := count(`<x:document ` + fmt.Sprintf(ns, "x") + `><x:p><x:ins/><x:del/><x:rPrChange/><x:moveFrom/></x:p></x:document>`)
	if got.Ins != 1 || got.Del != 1 || got.Other != 2 {
		t.Fatalf("prefix x: %+v", got)
	}
	// the w: prefix bound to another namespace, text and comments mentioning w:ins are not
	got = count(`<w:document xmlns:w="urn:other"><w:ins/><!-- <w:ins/> --><w:t>&lt;w:ins/&gt;</w:t><w:del/></w:document>`)
	if got.Total() != 0 {
		t.Fatalf("foreign namespace counted: %+v", got)
	}
	if _, err := trackedCounts(zipWithBody(t, "<unclosed")); err == nil {
		t.Fatal("malformed XML must fail")
	}
}

// Format changes are tracked changes: a tracked setFont passes; the format ops
// are counted (rPrChange, pPrChange, sectPrChange, tblPrChange, trPrChange,
// tcPrChange, numberingChange, moveFrom, moveTo).
func TestTrackedFormatChangeCounts(t *testing.T) {
	deps, _ := testDeps(t)
	r := call(t, deps, "office_edit_v1", map[string]any{"format": "docx", "file": realDocx(t, "minimal.docx"), "track": true,
		"ops": []any{map[string]any{"op": "setFont", "target": map[string]any{"blockIndexes": []int{0}}, "bold": true, "note": "FORMATONLY"}}})
	mustOK(t, r)
	for _, name := range []string{"rPrChange", "pPrChange", "sectPrChange", "tblPrChange", "trPrChange", "tcPrChange", "numberingChange", "moveFrom", "moveTo"} {
		rc := func() revisionCounts {
			x, err := trackedCounts(zipWithBody(t, `<w:document xmlns:w="`+wordNS+`"><w:`+name+`/></w:document>`))
			if err != nil {
				t.Fatal(err)
			}
			return x
		}()
		if rc.Other != 1 {
			t.Errorf("%s not counted: %+v", name, rc)
		}
	}
}

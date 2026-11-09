package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/form/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/form/internal/pdfform"
	"github.com/agentmaurice/mcpchatui/mcp/form/internal/testpdf"
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

// mustFail checks a business error: isError is true, a stable code and a message.
func mustFail(t *testing.T, r toolResult, code string) map[string]any {
	t.Helper()
	if !r.IsError || r.Content["error"] != code {
		t.Fatalf("want error %q with isError=true, got isError=%v %v", code, r.IsError, r.Content)
	}
	if _, ok := r.Content["message"].(string); !ok {
		t.Fatalf("error without message: %v", r.Content)
	}
	return r.Content
}

func b64(b []byte) map[string]any {
	return map[string]any{"content_base64": base64.StdEncoding.EncodeToString(b)}
}

func formPDF(t *testing.T) []byte {
	t.Helper()
	data, err := testpdf.Form()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func outBytes(t *testing.T, content map[string]any) []byte {
	t.Helper()
	f := content["file"].(map[string]any)
	data, err := base64.StdEncoding.DecodeString(f["content_base64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if int(f["bytes"].(float64)) != len(data) || f["sha256"] != pdfform.SHA(data) || f["mime_type"] != "application/pdf" {
		t.Fatalf("file metadata out of sync: %v", f)
	}
	return data
}

func TestHealthAndCapabilities(t *testing.T) {
	d := Deps{MaxInputBytes: 1 << 20}
	if h := mustOK(t, call(t, d, "form_health_v1", nil)); h["version"] != "0.1.0" {
		t.Fatalf("health: %v", h)
	}
	c := mustOK(t, call(t, d, "form_capabilities_v1", nil))
	eng := c["engine"].(map[string]any)
	if eng["name"] != "pdfcpu" || eng["license"] != "Apache-2.0" || eng["version"] == "" {
		t.Fatalf("engine: %v", eng)
	}
	if c["buffer_configured"] != false || c["limits"].(map[string]any)["max_input_bytes"].(float64) != 1<<20 {
		t.Fatalf("capabilities: %v", c)
	}
	codes := strings.Join(toStrings(c["error_codes"]), " ")
	for _, want := range []string{"form_xfa_unsupported", "form_encrypted", "form_signed", "form_unknown_field", "form_value_too_long", "form_unknown_option", "form_type_mismatch"} {
		if !strings.Contains(codes, want) {
			t.Fatalf("missing code %s in %s", want, codes)
		}
	}
	if len(toStrings(c["tools"])) != 4 {
		t.Fatalf("tools: %v", c["tools"])
	}
}

func toStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestInspect(t *testing.T) {
	d := Deps{MaxInputBytes: 20 << 20}
	r := mustOK(t, call(t, d, "form_inspect_v1", map[string]any{"file": b64(formPDF(t))}))
	if r["fillable"] != true || r["field_count"].(float64) != 11 || r["pages"].(float64) != 2 || r["encrypted"] != false || r["xfa"] != false || r["signed"] != false {
		t.Fatalf("inspect: %v", r)
	}
	fields := r["fields"].([]any)
	byName := map[string]map[string]any{}
	for _, f := range fields {
		m := f.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if byName["vat"]["max_length"].(float64) != 8 || byName["locked_ref"]["read_only"] != true || byName["size"]["type"] != "radio" || byName["certs"]["type"] != "listbox" {
		t.Fatalf("fields: %v", byName)
	}
	if len(r["input_sha256"].(string)) != 64 {
		t.Fatal("input sha")
	}
}

func TestInspectReportsRefusalsWithoutFailing(t *testing.T) {
	form := formPDF(t)
	xfa, _ := testpdf.XFA(form)
	signed, _ := testpdf.SignedField(form)
	enc, _ := testpdf.Encrypted(form, "pw")
	nf, _ := testpdf.NoForm()
	for name, tc := range map[string]struct {
		data []byte
		code string
		flag string
	}{"xfa": {xfa, "form_xfa_unsupported", "xfa"}, "signed": {signed, "form_signed", "signed"}, "encrypted": {enc, "form_encrypted", "encrypted"}, "noform": {nf, "form_no_fields", ""}} {
		t.Run(name, func(t *testing.T) {
			r := mustOK(t, call(t, Deps{}, "form_inspect_v1", map[string]any{"file": b64(tc.data)}))
			if r["fillable"] != false || r["refusal_code"] != tc.code {
				t.Fatalf("inspect: %v", r)
			}
			if tc.flag != "" && r[tc.flag] != true {
				t.Fatalf("flag %s: %v", tc.flag, r)
			}
		})
	}
}

func TestFillInline(t *testing.T) {
	form := formPDF(t)
	r := mustOK(t, call(t, Deps{}, "form_fill_v1", map[string]any{
		"file": b64(form), "filename": "../evil/answers.PDF",
		"values": map[string]any{"company": "ACME", "gdpr": true, "size": "Medium", "certs": []any{"HDS"}},
	}))
	if r["outcome"] != "filled" || r["flattened"] != false {
		t.Fatalf("fill: %v", r)
	}
	if r["file"].(map[string]any)["filename"] != "answers.pdf" {
		t.Fatalf("filename: %v", r["file"])
	}
	in := r["integrity"].(map[string]any)
	if in["read_back_verified"] != true || in["untouched_fields_unchanged"] != true || in["validated"] != true || in["pages_before"].(float64) != 2 || in["pages_after"].(float64) != 2 {
		t.Fatalf("integrity: %v", in)
	}
	out := outBytes(t, r)
	if in["input_sha256"] != pdfform.SHA(form) || in["output_sha256"] != pdfform.SHA(out) {
		t.Fatalf("sha: %v", in)
	}
	// The produced file reads back through the inspect tool.
	ins := mustOK(t, call(t, Deps{}, "form_inspect_v1", map[string]any{"file": b64(out)}))
	for _, f := range ins["fields"].([]any) {
		m := f.(map[string]any)
		if m["name"] == "company" && m["value"] != "ACME" {
			t.Fatalf("company: %v", m)
		}
		if m["name"] == "certs" && !(len(m["value"].([]any)) == 1 && m["value"].([]any)[0] == "HDS") {
			t.Fatalf("certs: %v", m)
		}
	}
}

func TestFillFlatten(t *testing.T) {
	r := mustOK(t, call(t, Deps{}, "form_fill_v1", map[string]any{"file": b64(formPDF(t)), "values": map[string]any{"company": "Flat"}, "flatten": true}))
	if r["flattened"] != true || r["integrity"].(map[string]any)["flattened"] != true {
		t.Fatalf("flatten: %v", r)
	}
	ins := mustOK(t, call(t, Deps{}, "form_inspect_v1", map[string]any{"file": b64(outBytes(t, r))}))
	if ins["field_count"].(float64) != 0 || ins["fillable"] != false {
		t.Fatalf("flattened file: %v", ins)
	}
}

func TestFillUnchangedOutcome(t *testing.T) {
	r := mustOK(t, call(t, Deps{}, "form_fill_v1", map[string]any{"file": b64(formPDF(t)), "values": map[string]any{"company": "Initial Corp"}}))
	if r["outcome"] != "unchanged" || r["integrity"].(map[string]any)["unchanged"] != true {
		t.Fatalf("outcome: %v", r)
	}
}

func TestBusinessErrorsAreIsError(t *testing.T) {
	form := formPDF(t)
	d := Deps{MaxInputBytes: 20 << 20}
	for _, tc := range []struct {
		name   string
		values map[string]any
		code   string
		field  string
	}{
		{"unknown", map[string]any{"ghost": "x"}, "form_unknown_field", "ghost"},
		{"type", map[string]any{"vat": true}, "form_type_mismatch", "vat"},
		{"option", map[string]any{"size": "XXL"}, "form_unknown_option", "size"},
		{"length", map[string]any{"vat": "0123456789"}, "form_value_too_long", "vat"},
		{"readonly", map[string]any{"locked_ref": "x"}, "form_field_readonly", "locked_ref"},
		{"number value", map[string]any{"vat": 12}, "form_type_mismatch", "vat"},
		{"null value", map[string]any{"vat": nil}, "form_type_mismatch", "vat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vals := map[string]any{"company": "Fine"}
			for k, v := range tc.values {
				vals[k] = v
			}
			r := mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": b64(form), "values": vals}), tc.code)
			if r["details"].(map[string]any)["field"] != tc.field {
				t.Fatalf("details: %v", r)
			}
			if _, has := r["file"]; has {
				t.Fatal("an atomic refusal must not return a file")
			}
		})
	}
	xfa, _ := testpdf.XFA(form)
	signed, _ := testpdf.SignedField(form)
	enc, _ := testpdf.Encrypted(form, "pw")
	for code, data := range map[string][]byte{"form_xfa_unsupported": xfa, "form_signed": signed, "form_encrypted": enc} {
		mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": b64(data), "values": map[string]any{"company": "x"}, "flatten": true}), code)
	}
	nf, _ := testpdf.NoForm()
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": b64(nf), "values": map[string]any{"a": "b"}}), "form_no_fields")
}

func TestArgumentValidation(t *testing.T) {
	form := formPDF(t)
	d := Deps{MaxInputBytes: 1 << 20, MaxValues: 3}
	ok := map[string]any{"company": "x"}
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"values": ok}), "invalid_arguments")
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": map[string]any{}, "values": ok}), "invalid_arguments")
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": map[string]any{"content_base64": "x", "ref": "buffer://a"}, "values": ok}), "invalid_arguments")
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": map[string]any{"content_base64": "***"}, "values": ok}), "invalid_arguments")
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": b64(form)}), "invalid_arguments")
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": b64(form), "values": map[string]any{}}), "invalid_arguments")
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": b64(form), "values": ok, "unknown_arg": 1}), "invalid_arguments")
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": b64(form), "values": map[string]any{"a": "1", "b": "1", "c": "1", "d": "1"}}), "invalid_arguments")
	mustFail(t, call(t, d, "form_inspect_v1", map[string]any{"file": b64([]byte("hello"))}), "invalid_pdf")
	mustFail(t, call(t, d, "form_inspect_v1", map[string]any{"file": map[string]any{"ref": "https://example.com/a.pdf"}}), "buffer_unavailable")
	huge := strings.Repeat("A", maxValuesBytes+10)
	mustFail(t, call(t, Deps{MaxInputBytes: 20 << 20}, "form_fill_v1", map[string]any{"file": b64(form), "values": map[string]any{"company": huge}}), "invalid_arguments")
}

func TestInjectedFieldsAreIgnored(t *testing.T) {
	inj := map[string]any{"deployment_id": "d", "organization_id": "o", "user_id": "u", "space_id": "s", "session_id": "x"}
	with := func(m map[string]any) map[string]any {
		for k, v := range inj {
			m[k] = v
		}
		return m
	}
	mustOK(t, call(t, Deps{}, "form_health_v1", with(map[string]any{})))
	mustOK(t, call(t, Deps{}, "form_capabilities_v1", with(map[string]any{})))
	form := formPDF(t)
	mustOK(t, call(t, Deps{}, "form_inspect_v1", with(map[string]any{"file": b64(form)})))
	mustOK(t, call(t, Deps{}, "form_fill_v1", with(map[string]any{"file": b64(form), "values": map[string]any{"company": "Z"}})))
}

func TestSizeBounds(t *testing.T) {
	form := formPDF(t)
	small := Deps{MaxInputBytes: len(form) - 1}
	mustFail(t, call(t, small, "form_inspect_v1", map[string]any{"file": b64(form)}), "file_too_large")
	mustFail(t, call(t, small, "form_fill_v1", map[string]any{"file": b64(form), "values": map[string]any{"company": "x"}}), "file_too_large")
	// The base64 text is bounded before it is decoded.
	mustFail(t, call(t, Deps{MaxInputBytes: 1000}, "form_inspect_v1", map[string]any{"file": map[string]any{"content_base64": strings.Repeat("A", 4000)}}), "file_too_large")
	// Exactly at the limit is accepted.
	mustOK(t, call(t, Deps{MaxInputBytes: len(form)}, "form_inspect_v1", map[string]any{"file": b64(form)}))
	// An output above the inline limit needs a Buffer.
	mustFail(t, call(t, Deps{MaxInlineBytes: 100}, "form_fill_v1", map[string]any{"file": b64(form), "values": map[string]any{"company": "x"}}), "storage_required")
}

type bufferStub struct {
	store map[string][]byte
	posts int
	last  map[string]any
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
			body, _ := io.ReadAll(r.Body)
			var raw map[string]any
			_ = json.Unmarshal(body, &raw)
			meta, _ := raw["metadata"].(map[string]any)
			if meta["encoding"] != "base64" || meta["filename"] == "" || raw["mime_type"] != "application/pdf" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			data, _ := base64.StdEncoding.DecodeString(raw["text"].(string))
			stub.posts++
			stub.last = raw
			key := "form/k" + string(rune('0'+stub.posts))
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
	return stub, &buffer.Client{URL: srv.URL, Token: "tok", Namespace: "form", TTL: 60, HTTP: srv.Client()}
}

func TestBufferInputAndOutput(t *testing.T) {
	stub, client := newBufferStub(t)
	form := formPDF(t)
	stub.store["form/in"] = form
	d := Deps{Buffer: client, MaxInputBytes: 20 << 20}

	r := mustOK(t, call(t, d, "form_fill_v1", map[string]any{"file": map[string]any{"ref": "buffer://form/in"}, "values": map[string]any{"company": "Buffered"}}))
	// outcome is reported with a Buffer output, next to the reference.
	if r["outcome"] != "filled" {
		t.Fatalf("outcome: %v", r)
	}
	f := r["file"].(map[string]any)
	ref, _ := f["ref"].(string)
	if !strings.HasPrefix(ref, "buffer://form/") || f["content_base64"] != nil {
		t.Fatalf("file: %v", f)
	}
	outs := r["outputs"].([]any)
	if len(outs) != 1 || outs[0].(map[string]any)["ref"] != ref || outs[0].(map[string]any)["mime_type"] != "application/pdf" {
		t.Fatalf("outputs: %v", outs)
	}
	stored := stub.store[strings.TrimPrefix(ref, "buffer://")]
	if f["sha256"] != pdfform.SHA(stored) || r["integrity"].(map[string]any)["output_sha256"] != pdfform.SHA(stored) {
		t.Fatal("stored bytes differ from the reported hash")
	}
	// The produced reference can be inspected.
	ins := mustOK(t, call(t, d, "form_inspect_v1", map[string]any{"file": map[string]any{"ref": ref}}))
	if ins["field_count"].(float64) != 11 {
		t.Fatalf("inspect: %v", ins)
	}
	// An unchanged outcome also goes through the Buffer.
	u := mustOK(t, call(t, d, "form_fill_v1", map[string]any{"file": map[string]any{"ref": "buffer://form/in"}, "values": map[string]any{"company": "Initial Corp"}}))
	if u["outcome"] != "unchanged" || !strings.HasPrefix(u["file"].(map[string]any)["ref"].(string), "buffer://") {
		t.Fatalf("unchanged: %v", u)
	}
	mustFail(t, call(t, d, "form_inspect_v1", map[string]any{"file": map[string]any{"ref": "buffer://form/missing"}}), "buffer_not_found")
	mustFail(t, call(t, d, "form_inspect_v1", map[string]any{"file": map[string]any{"ref": "file:///etc/passwd"}}), "invalid_arguments")
	stub.store["form/notpdf"] = []byte("plain text")
	mustFail(t, call(t, d, "form_inspect_v1", map[string]any{"file": map[string]any{"ref": "buffer://form/notpdf"}}), "invalid_pdf")
	small := Deps{Buffer: client, MaxInputBytes: 500}
	mustFail(t, call(t, small, "form_inspect_v1", map[string]any{"file": map[string]any{"ref": "buffer://form/in"}}), "file_too_large")
}

func TestBufferFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 500) }))
	defer srv.Close()
	d := Deps{Buffer: &buffer.Client{URL: srv.URL, Token: "tok", Namespace: "form", TTL: 60, HTTP: srv.Client()}}
	mustFail(t, call(t, d, "form_fill_v1", map[string]any{"file": b64(formPDF(t)), "values": map[string]any{"company": "x"}}), "buffer_failed")
}

func TestCheckOutput(t *testing.T) {
	good := append([]byte("%PDF-1.7\n"), []byte("body\n%%EOF\n")...)
	if err := checkOutput(good, 1000); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"empty": nil, "header": []byte("hello %%EOF"), "eof": []byte("%PDF-1.7 body"), "big": good} {
		max := 1000
		if name == "big" {
			max = 5
		}
		if err := checkOutput(b, max); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestCleanFilename(t *testing.T) {
	for in, want := range map[string]string{"": "filled.pdf", "a.pdf": "a.pdf", "../x/y.PDF": "y.pdf", `c:\d\e.pdf`: "e.pdf", "  ": "filled.pdf", "..": "filled.pdf", "a?b*c": "abc.pdf"} {
		if got := cleanFilename(in, "filled"); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
	if got := cleanFilename(strings.Repeat("x", 300), "filled"); len(got) != maxFilenameLength+4 {
		t.Fatalf("long name: %d", len(got))
	}
}

func TestEnvironmentConfiguration(t *testing.T) {
	t.Setenv("FORM_MAX_INPUT_BYTES", "12345")
	t.Setenv("FORM_MAX_VALUES", "7")
	t.Setenv("BUFFER_SERVICE_URL", "http://127.0.0.1:1/api/v1/buffer")
	t.Setenv("BUFFER_TOKEN", "t")
	d := DepsFromEnv()
	if d.MaxInputBytes != 12345 || d.MaxValues != 7 || d.Buffer == nil || d.Buffer.Namespace != "form" {
		t.Fatalf("deps: %+v", d)
	}
	t.Setenv("BUFFER_NAMESPACE", "shared")
	if DepsFromEnv().Buffer.Namespace != "shared" {
		t.Fatal("generic namespace")
	}
	os.Unsetenv("FORM_MAX_INPUT_BYTES")
	t.Setenv("FORM_MAX_INPUT_BYTES", "bad")
	if DepsFromEnv().MaxInputBytes != defaultMaxInput {
		t.Fatal("default input limit")
	}
}

func TestFlattenWithNoValueChangeIsFilled(t *testing.T) {
	form := formPDF(t)
	r := mustOK(t, call(t, Deps{}, "form_fill_v1", map[string]any{"file": b64(form), "values": map[string]any{"company": "Initial Corp"}, "flatten": true}))
	in := r["integrity"].(map[string]any)
	if r["outcome"] != "filled" || in["unchanged"] != false || r["flattened"] != true || in["input_sha256"] == in["output_sha256"] {
		t.Fatalf("flatten outcome: %v", r)
	}
}

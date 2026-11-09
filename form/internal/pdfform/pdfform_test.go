package pdfform

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/form/internal/testpdf"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

var bg = context.Background()

func mustForm(t *testing.T) []byte {
	t.Helper()
	data, err := testpdf.Form()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustDoc(t *testing.T, data []byte) *Doc {
	t.Helper()
	d, err := Inspect(bg, data, 0)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func wantCode(t *testing.T, err error, code, field string) {
	t.Helper()
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("want error %s, got %v", code, err)
	}
	if pe.Code != code || pe.Field != field {
		t.Fatalf("want %s on %q, got %s on %q: %s", code, field, pe.Code, pe.Field, pe.Message)
	}
}

func TestInspectFields(t *testing.T) {
	data, err := testpdf.WithRequired(mustForm(t), "vat")
	if err != nil {
		t.Fatal(err)
	}
	d := mustDoc(t, data)
	if d.Pages != 2 || d.Encrypted || d.XFA || d.Signed || d.Refusal() != nil {
		t.Fatalf("doc: %+v", d)
	}
	get := func(n string) Field {
		f, ok := d.Find(n)
		if !ok {
			t.Fatalf("field %q missing in %+v", n, d.Fields)
		}
		return *f
	}
	if f := get("company"); f.Type != TypeText || f.Value != "Initial Corp" || f.Tooltip != "Company name" || f.ReadOnly || f.Required {
		t.Fatalf("company: %+v", f)
	}
	if f := get("vat"); f.MaxLength != 8 || !f.Required || f.Value != "" {
		t.Fatalf("vat: %+v", f)
	}
	if f := get("comments"); !f.Multiline {
		t.Fatalf("comments: %+v", f)
	}
	if f := get("locked_ref"); !f.ReadOnly || f.Value != "FIXED-1" {
		t.Fatalf("locked_ref: %+v", f)
	}
	if f := get("gdpr"); f.Type != TypeCheckbox || f.Value != false {
		t.Fatalf("gdpr: %+v", f)
	}
	if f := get("size"); f.Type != TypeRadio || f.Value != "Small" || !reflect.DeepEqual(f.Options, []string{"Small", "Medium", "Large"}) {
		t.Fatalf("size: %+v", f)
	}
	if f := get("country"); f.Type != TypeCombobox || f.Editable || !reflect.DeepEqual(f.Options, []string{"France", "Germany", "Spain"}) {
		t.Fatalf("country: %+v", f)
	}
	if f := get("city"); f.Type != TypeCombobox || !f.Editable {
		t.Fatalf("city: %+v", f)
	}
	if f := get("certs"); f.Type != TypeListbox || !f.Multiple || len(f.Options) != 3 {
		t.Fatalf("certs: %+v", f)
	}
	if f := get("second_page"); !reflect.DeepEqual(f.Pages, []int{2}) {
		t.Fatalf("second_page: %+v", f)
	}
	if len(d.Fields) != 11 {
		t.Fatalf("fields: %d", len(d.Fields))
	}
}

func TestFillEveryType(t *testing.T) {
	data := mustForm(t)
	before := mustDoc(t, data)
	vals := map[string]any{
		"company": "ACME é & Co", "vat": "FR123456", "comments": "line one\nline two", "gdpr": true,
		"size": "Large", "country": "Spain", "city": "Berlin", "certs": []any{"SOC 2", "HDS"}, "second_page": "p2",
	}
	out, in, err := Fill(bg, data, vals, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !in.ReadBackVerified || !in.UntouchedUnchanged || !in.PagesContentIntact || !in.Validated || in.Flattened || in.Unchanged {
		t.Fatalf("integrity: %+v", in)
	}
	if in.InputSHA256 != SHA(data) || in.OutputSHA256 != SHA(out) || in.InputSHA256 == in.OutputSHA256 {
		t.Fatalf("sha: %+v", in)
	}
	if in.FieldsFilled != 9 || in.FieldsUntouched != 2 || in.PagesBefore != 2 || in.PagesAfter != 2 {
		t.Fatalf("counts: %+v", in)
	}
	after := mustDoc(t, out)
	if after.Pages != before.Pages || len(after.Fields) != len(before.Fields) {
		t.Fatal("structure changed")
	}
	for name, want := range map[string]any{"company": "ACME é & Co", "vat": "FR123456", "gdpr": true, "size": "Large", "country": "Spain", "city": "Berlin", "second_page": "p2"} {
		if f, _ := after.Find(name); f.Value != want {
			t.Fatalf("%s: got %#v want %#v", name, f.Value, want)
		}
	}
	if f, _ := after.Find("comments"); f.Value.(string) != "line one\nline two" {
		t.Fatalf("comments: %#v", f.Value)
	}
	if f, _ := after.Find("certs"); !reflect.DeepEqual(f.Value, []string{"SOC 2", "HDS"}) {
		t.Fatalf("certs: %#v", f.Value)
	}
	// Fields that were not requested keep their value.
	for _, n := range []string{"untouched", "locked_ref"} {
		b, _ := before.Find(n)
		a, _ := after.Find(n)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s changed: %+v -> %+v", n, b, a)
		}
	}
	if bytes.Equal(out, data) {
		t.Fatal("output equals input")
	}
}

func TestFillIsAtomic(t *testing.T) {
	data := mustForm(t)
	good := map[string]any{"company": "Good"}
	cases := []struct {
		name  string
		bad   map[string]any
		code  string
		field string
	}{
		{"unknown field", map[string]any{"nope": "x"}, CodeUnknownField, "nope"},
		{"text expects string", map[string]any{"vat": true}, CodeTypeMismatch, "vat"},
		{"checkbox expects bool", map[string]any{"gdpr": "maybe"}, CodeTypeMismatch, "gdpr"},
		{"checkbox list", map[string]any{"gdpr": []any{"a"}}, CodeTypeMismatch, "gdpr"},
		{"unknown radio option", map[string]any{"size": "Huge"}, CodeUnknownOption, "size"},
		{"empty radio", map[string]any{"size": ""}, CodeUnknownOption, "size"},
		{"unknown combo option", map[string]any{"country": "Italy"}, CodeUnknownOption, "country"},
		{"free value in editable combo", map[string]any{"city": "Lyon"}, CodeUnknownOption, "city"},
		{"unknown list option", map[string]any{"certs": []any{"SOC 2", "PCI"}}, CodeUnknownOption, "certs"},
		{"list element type", map[string]any{"certs": []any{1}}, CodeTypeMismatch, "certs"},
		{"too long", map[string]any{"vat": "123456789"}, CodeValueTooLong, "vat"},
		{"line break in single line", map[string]any{"company": "a\nb"}, CodeInvalidValue, "company"},
		{"read-only", map[string]any{"locked_ref": "changed"}, CodeFieldReadOnly, "locked_ref"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			batch := map[string]any{"company": good["company"]}
			for k, v := range tc.bad {
				batch[k] = v
			}
			if tc.bad["company"] != nil {
				delete(batch, "company")
				batch["second_page"] = "ok"
				batch["company"] = tc.bad["company"]
			}
			out, in, err := Fill(bg, data, batch, false, 0)
			wantCode(t, err, tc.code, tc.field)
			if out != nil || in != nil {
				t.Fatal("a refused batch must produce nothing")
			}
		})
	}
	// Exact limit and a multiline value are accepted.
	if _, _, err := Fill(bg, data, map[string]any{"vat": "12345678", "comments": "a\nb"}, false, 0); err != nil {
		t.Fatalf("limit: %v", err)
	}
	for _, v := range []string{"a\r\nb", "a\rb"} {
		_, _, err := Fill(bg, data, map[string]any{"comments": v}, false, 0)
		wantCode(t, err, CodeInvalidValue, "comments")
	}
	if _, _, err := Fill(bg, data, map[string]any{"certs": []any{}, "city": ""}, false, 0); err != nil {
		t.Fatalf("empty list and empty combo: %v", err)
	}
	if _, _, err := Fill(bg, data, nil, false, 0); err == nil {
		t.Fatal("empty values must be refused")
	}
	// Single selection list: a list of two is refused (use a single-select via the same field type).
	wantCode(t, func() error {
		d := mustDoc(t, data)
		f, _ := d.Find("certs")
		_, err := validateOne(&Field{Name: "x", Type: TypeListbox, Options: f.Options, Multiple: false}, []any{"SOC 2", "HDS"})
		return err
	}(), CodeInvalidValue, "x")
	wantCode(t, func() error {
		_, err := validateOne(&Field{Name: "sig", Type: TypeSignature}, "x")
		return err
	}(), CodeTypeMismatch, "sig")
}

func TestFillNoOpReturnsInputAsIs(t *testing.T) {
	data := mustForm(t)
	out, in, err := Fill(bg, data, map[string]any{"company": "Initial Corp", "size": "Small"}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !in.Unchanged || !bytes.Equal(out, data) || in.InputSHA256 != in.OutputSHA256 {
		t.Fatalf("expected an unchanged file: %+v", in)
	}
	// Filling the produced file again with the same values is a no-op too.
	filled, _, err := Fill(bg, data, map[string]any{"company": "Second"}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	again, in2, err := Fill(bg, filled, map[string]any{"company": "Second"}, false, 0)
	if err != nil || !in2.Unchanged || !bytes.Equal(again, filled) {
		t.Fatalf("second fill: %v %+v", err, in2)
	}
}

func TestFillDoesNotMutateItsInput(t *testing.T) {
	data := mustForm(t)
	cp := append([]byte(nil), data...)
	if _, _, err := Fill(bg, data, map[string]any{"company": "X"}, true, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, cp) {
		t.Fatal("the input buffer was modified")
	}
}

func TestFlatten(t *testing.T) {
	data := mustForm(t)
	out, in, err := Fill(bg, data, map[string]any{"company": "Flat Corp", "gdpr": true, "size": "Medium", "second_page": "p2"}, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !in.Flattened || in.FlattenedWidgets != 13 || in.PagesAfter != 2 || !in.ReadBackVerified {
		t.Fatalf("integrity: %+v", in)
	}
	d := mustDoc(t, out)
	if d.Pages != 2 || len(d.Fields) != 0 || d.Signed || d.XFA {
		t.Fatalf("flattened doc: %+v", d)
	}
	if in.OutputSHA256 != SHA(out) {
		t.Fatal("sha")
	}
	// A flattened file has no field left to fill.
	if _, _, err := Fill(bg, out, map[string]any{"company": "again"}, false, 0); err == nil {
		t.Fatal("expected form_no_fields")
	} else {
		wantCode(t, err, CodeNoForm, "")
	}
}

func TestRefusals(t *testing.T) {
	form := mustForm(t)
	xfa, err := testpdf.XFA(form)
	if err != nil {
		t.Fatal(err)
	}
	xfaBad, err := testpdf.XFAMalformed(form)
	if err != nil {
		t.Fatal(err)
	}
	sigFlags, err := testpdf.SignedFlags(form)
	if err != nil {
		t.Fatal(err)
	}
	sigField, err := testpdf.SignedField(form)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := testpdf.Encrypted(form, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	encEmpty, err := testpdf.EncryptedEmptyUserPassword(form)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		code string
		doc  func(*Doc) bool
	}{
		{"xfa", xfa, CodeXFA, func(d *Doc) bool { return d.XFA && !d.Signed && !d.Encrypted }},
		{"xfa that does not validate", xfaBad, CodeXFA, func(d *Doc) bool { return d.XFA }},
		{"signed by flags", sigFlags, CodeSigned, func(d *Doc) bool { return d.Signed && !d.XFA }},
		{"signed by field", sigField, CodeSigned, func(d *Doc) bool { return d.Signed && d.Signatures == 1 }},
		{"encrypted with a password", enc, CodeEncrypted, func(d *Doc) bool { return d.Encrypted }},
		{"encrypted with an empty user password", encEmpty, CodeEncrypted, func(d *Doc) bool { return d.Encrypted }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := mustDoc(t, tc.data)
			if !tc.doc(d) {
				t.Fatalf("inspection: %+v", d)
			}
			if e := d.Refusal(); e == nil || e.Code != tc.code {
				t.Fatalf("refusal: %v", e)
			}
			for _, flatten := range []bool{false, true} {
				out, in, err := Fill(bg, tc.data, map[string]any{"company": "X"}, flatten, 0)
				wantCode(t, err, tc.code, "")
				if out != nil || in != nil {
					t.Fatal("a refused file must produce nothing")
				}
			}
		})
	}
	// The signature field is listed and never fillable.
	d := mustDoc(t, sigField)
	f, ok := d.Find("approval")
	if !ok || f.Type != TypeSignature || !f.Signed {
		t.Fatalf("signature field: %+v", f)
	}
	// A signed file is also refused by the flatten step on its own.
	if _, _, err := Flatten(bg, sigFlags, 0); err == nil {
		t.Fatal("a signed file must not be flattened")
	}
}

func TestInvalidInputs(t *testing.T) {
	for name, data := range map[string][]byte{
		"not a pdf": []byte("hello"),
		"truncated": mustForm(t)[:300],
		"garbage":   append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0xff, 0x00, '<', '<'}, 200)...),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Inspect(bg, data, 0)
			wantCode(t, err, CodeInvalidPDF, "")
			_, _, err = Fill(bg, data, map[string]any{"a": "b"}, false, 0)
			wantCode(t, err, CodeInvalidPDF, "")
		})
	}
	nf, err := testpdf.NoForm()
	if err != nil {
		t.Fatal(err)
	}
	d := mustDoc(t, nf)
	if len(d.Fields) != 0 || d.Pages != 1 {
		t.Fatalf("no-form doc: %+v", d)
	}
	_, _, err = Fill(bg, nf, map[string]any{"a": "b"}, false, 0)
	wantCode(t, err, CodeNoForm, "")
	// The input size limit is enforced by pdfcpu as well.
	if _, err := Inspect(bg, mustForm(t), 1000); err == nil {
		t.Fatal("expected the size limit to apply")
	}
}

func TestUnencodableTextIsACleanRefusal(t *testing.T) {
	_, _, err := Fill(bg, mustForm(t), map[string]any{"company": "日本語"}, false, 0)
	if err == nil || strings.Contains(err.Error(), "panic") {
		t.Fatalf("want a refusal, got %v", err)
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("not a typed error: %v", err)
	}
}

// The integrity check must catch what a faulty engine could do: these cases
// feed compare() with tampered "after" documents.
func TestCompareDetectsTampering(t *testing.T) {
	data := mustForm(t)
	before := mustInspectDigests(t, data)
	out, _, err := Fill(bg, data, map[string]any{"company": "X"}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	good := mustInspectDigests(t, out)
	exp := map[string]expectedValue{"company": {typ: TypeText, text: "X"}}
	if err := compare(before, good, exp, &Integrity{}); err != nil {
		t.Fatalf("untampered: %v", err)
	}
	clone := func() *Doc {
		d := *good
		d.Fields = append([]Field(nil), good.Fields...)
		d.pages = make([]pageDigest, len(good.pages))
		for i, p := range good.pages {
			d.pages[i] = pageDigest{content: p.content, resources: map[string]string{}}
			for k, v := range p.resources {
				d.pages[i].resources[k] = v
			}
		}
		return &d
	}
	set := func(d *Doc, name string, fn func(*Field)) {
		for i := range d.Fields {
			if d.Fields[i].Name == name {
				fn(&d.Fields[i])
			}
		}
	}
	for name, tc := range map[string]func(*Doc){
		"page lost":           func(d *Doc) { d.Pages-- },
		"field removed":       func(d *Doc) { d.Fields = d.Fields[1:] },
		"value not written":   func(d *Doc) { set(d, "company", func(f *Field) { f.Value = "other" }) },
		"other field changed": func(d *Doc) { set(d, "untouched", func(f *Field) { f.Value = "changed" }) },
		"flag changed":        func(d *Doc) { set(d, "company", func(f *Field) { f.ReadOnly = true }) },
		"options changed":     func(d *Doc) { set(d, "size", func(f *Field) { f.Options = []string{"Small"} }) },
		"page content":        func(d *Doc) { d.pages[0].content = []byte("changed") },
		"page resource": func(d *Doc) {
			for k := range d.pages[0].resources {
				d.pages[0].resources[k] = "other"
				break
			}
		},
		"page unreadable": func(d *Doc) { d.pages[1].err = "page 2: cannot be decoded" },
		"became signed":   func(d *Doc) { d.Signed = true },
		"became xfa":      func(d *Doc) { d.XFA = true },
	} {
		t.Run(name, func(t *testing.T) {
			d := clone()
			tc(d)
			wantCode(t, compare(before, d, exp, &Integrity{}), CodeIntegrityFailed, errFieldOf(name))
		})
	}
}

func errFieldOf(name string) string {
	switch name {
	case "value not written", "flag changed":
		return "company"
	case "other field changed":
		return "untouched"
	case "options changed":
		return "size"
	}
	return ""
}

func TestReadBackEqual(t *testing.T) {
	if readBackEqual(&Field{Value: "a\nb"}, expectedValue{typ: TypeText, text: "a\r\nb"}) || readBackEqual(&Field{Value: "a\nb"}, expectedValue{typ: TypeText, text: "a\rb"}) {
		t.Fatal("read back is strict: no line ending normalization")
	}
	if !readBackEqual(&Field{Value: "a\nb"}, expectedValue{typ: TypeText, text: "a\nb"}) {
		t.Fatal("identical")
	}
	if readBackEqual(&Field{Value: "a"}, expectedValue{typ: TypeText, text: "b"}) {
		t.Fatal("different text")
	}
	if !readBackEqual(&Field{Value: []string{"b", "a"}}, expectedValue{typ: TypeListbox, list: []string{"a", "b"}}) {
		t.Fatal("list order is irrelevant")
	}
	if !readBackEqual(&Field{Value: ""}, expectedValue{typ: TypeListbox}) || !readBackEqual(&Field{Value: []string{}}, expectedValue{typ: TypeListbox}) {
		t.Fatal("empty selection")
	}
	if readBackEqual(&Field{Value: true}, expectedValue{typ: TypeCheckbox, boolean: false}) {
		t.Fatal("checkbox")
	}
}

func TestCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(bg)
	cancel()
	data := mustForm(t)
	_, err := Inspect(ctx, data, 0)
	wantCode(t, err, CodeCancelled, "")
	_, _, err = Fill(ctx, data, map[string]any{"company": "x"}, false, 0)
	wantCode(t, err, CodeCancelled, "")
}

func TestMarkerTextDoesNotMeanEncryptedOrSigned(t *testing.T) {
	data, err := testpdf.WithMarkerText(mustForm(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("/Encrypt and /ByteRange")) {
		t.Fatal("the fixture must carry the raw markers")
	}
	d := mustDoc(t, data)
	if d.Encrypted || d.Signed || d.XFA || d.Refusal() != nil || len(d.Fields) != 11 {
		t.Fatalf("doc: %+v", d)
	}
	if _, in, err := Fill(bg, data, map[string]any{"company": "Marker"}, false, 0); err != nil || !in.ReadBackVerified {
		t.Fatalf("fill: %v", err)
	}
}

func pageContentAndXObjects(t *testing.T, data []byte) (string, map[string]bool) {
	t.Helper()
	ctx, err := api.ReadContext(bg, bytes.NewReader(data), newConf(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := api.ValidateContext(bg, ctx); err != nil {
		t.Fatal(err)
	}
	xt := ctx.XRefTable
	pd, _, _, err := xt.PageDict(bg, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := xt.PageContent(pd, 1)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	res, _ := xt.DereferenceDict(pd["Resources"])
	xo, _ := xt.DereferenceDict(res["XObject"])
	for k := range xo {
		names[k] = true
	}
	return string(c), names
}

// ctmBeforeFlatDraws runs a small content-stream interpreter (q, Q, cm) and
// returns the CTM in force just before each appearance's own cm.
func ctmBeforeFlatDraws(content string) [][6]float64 {
	mul := func(a, b [6]float64) [6]float64 { // a applied first, then b
		return [6]float64{a[0]*b[0] + a[1]*b[2], a[0]*b[1] + a[1]*b[3], a[2]*b[0] + a[3]*b[2], a[2]*b[1] + a[3]*b[3], a[4]*b[0] + a[5]*b[2] + b[4], a[4]*b[1] + a[5]*b[3] + b[5]}
	}
	ident := [6]float64{1, 0, 0, 1, 0, 0}
	cur, stack := ident, [][6]float64{}
	var out [][6]float64
	toks := strings.Fields(content)
	for i, tk := range toks {
		switch tk {
		case "q":
			stack = append(stack, cur)
		case "Q":
			if len(stack) > 0 {
				cur, stack = stack[len(stack)-1], stack[:len(stack)-1]
			}
		case "cm":
			if i >= 6 {
				var m [6]float64
				for j := 0; j < 6; j++ {
					m[j], _ = strconv.ParseFloat(toks[i-6+j], 64)
				}
				if i+2 < len(toks) && strings.HasPrefix(toks[i+1], "/FmFlat") && toks[i+2] == "Do" {
					out = append(out, cur)
				}
				cur = mul(m, cur)
			}
		}
	}
	return out
}

func TestFlattenKeepsExistingXObjectNames(t *testing.T) {
	data, err := testpdf.WithExistingXObject(mustForm(t))
	if err != nil {
		t.Fatal(err)
	}
	beforeContent, beforeNames := pageContentAndXObjects(t, data)
	if !beforeNames["FmFlat1"] || !strings.Contains(beforeContent, "/FmFlat1 Do") {
		t.Fatal("fixture lacks the pre-existing /FmFlat1")
	}
	ctx, _ := api.ReadContext(bg, bytes.NewReader(data), newConf(0))
	_ = api.ValidateContext(bg, ctx)
	pd, _, _, _ := ctx.XRefTable.PageDict(bg, 1, false)
	res, _ := ctx.XRefTable.DereferenceDict(pd["Resources"])
	xo, _ := ctx.XRefTable.DereferenceDict(res["XObject"])
	orig := xo["FmFlat1"]

	out, in, err := Fill(bg, data, map[string]any{"company": "Names"}, true, 0)
	if err != nil || in.FlattenedWidgets == 0 {
		t.Fatalf("fill: %v", err)
	}
	content, names := pageContentAndXObjects(t, out)
	ctx2, _ := api.ReadContext(bg, bytes.NewReader(out), newConf(0))
	_ = api.ValidateContext(bg, ctx2)
	pd2, _, _, _ := ctx2.XRefTable.PageDict(bg, 1, false)
	res2, _ := ctx2.XRefTable.DereferenceDict(pd2["Resources"])
	xo2, _ := ctx2.XRefTable.DereferenceDict(res2["XObject"])
	// The original resource still points at the original stream (same object content).
	o1, _ := ctx.XRefTable.Dereference(orig)
	o2, _ := ctx2.XRefTable.Dereference(xo2["FmFlat1"])
	s1, s2 := o1.(types.StreamDict), o2.(types.StreamDict)
	_ = ctx.XRefTable.DecodeStream(&s1)
	_ = ctx2.XRefTable.DecodeStream(&s2)
	if string(s1.Content) != "0 0 5 5 re f" || string(s2.Content) != "0 0 5 5 re f" {
		t.Fatalf("/FmFlat1 was overwritten: %q", s2.Content)
	}
	if !strings.Contains(content, "/FmFlat1 Do") || !names["FmFlat2"] || len(names) < 3 {
		t.Fatalf("names %v, content %q", names, content)
	}
}

func TestFlattenIsolatesTheGraphicState(t *testing.T) {
	data, err := testpdf.WithUnrestoredScale(mustForm(t))
	if err != nil {
		t.Fatal(err)
	}
	content, _ := pageContentAndXObjects(t, data)
	if got := ctmBeforeFlatDraws(content); len(got) != 0 {
		t.Fatal("fixture already draws flat appearances")
	}
	out, _, err := Fill(bg, data, map[string]any{"company": "CTM"}, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	flat, _ := pageContentAndXObjects(t, out)
	draws := ctmBeforeFlatDraws(flat)
	if len(draws) == 0 {
		t.Fatal("no appearance drawn")
	}
	for _, m := range draws {
		if m != [6]float64{1, 0, 0, 1, 0, 0} {
			t.Fatalf("an appearance inherits the CTM %v", m)
		}
	}
	// Control: without the isolation the interpreter would see the scale.
	if got := ctmBeforeFlatDraws("2 0 0 2 0 0 cm q 1 0 0 1 5 5 cm /FmFlat1 Do Q"); got[0] != [6]float64{2, 0, 0, 2, 0, 0} {
		t.Fatalf("interpreter: %v", got)
	}
}

func TestFlattenRefusesAWidgetWithoutAppearance(t *testing.T) {
	data, err := testpdf.WithoutAppearance(mustForm(t), "untouched")
	if err != nil {
		t.Fatal(err)
	}
	out, in, err := Fill(bg, data, map[string]any{"company": "NoAP"}, true, 0)
	wantCode(t, err, CodeFlattenFailed, "untouched")
	if out != nil || in != nil {
		t.Fatal("no output expected")
	}
	// Without flatten the same fill is fine, and an empty field without appearance may be flattened.
	if _, _, err := Fill(bg, data, map[string]any{"company": "NoAP"}, false, 0); err != nil {
		t.Fatal(err)
	}
	empty, err := testpdf.WithoutAppearance(mustForm(t), "vat")
	if err != nil {
		t.Fatal(err)
	}
	if _, in, err := Fill(bg, empty, map[string]any{"company": "Empty"}, true, 0); err != nil || !in.Flattened {
		t.Fatalf("empty field without appearance: %v", err)
	}
}

func TestFlattenWithNoValueChangeIsNotUnchanged(t *testing.T) {
	data := mustForm(t)
	out, in, err := Fill(bg, data, map[string]any{"company": "Initial Corp"}, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if in.Unchanged || !in.Flattened || in.InputSHA256 == in.OutputSHA256 || bytes.Equal(out, data) {
		t.Fatalf("integrity: %+v", in)
	}
}

func mustInspectDigests(t *testing.T, data []byte) *Doc {
	t.Helper()
	d, err := inspect(bg, data, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCompareDetectsAChangedImage(t *testing.T) {
	a, err := testpdf.WithImage(mustForm(t), 0x10)
	if err != nil {
		t.Fatal(err)
	}
	b, err := testpdf.WithImage(mustForm(t), 0xF0)
	if err != nil {
		t.Fatal(err)
	}
	da, db := mustInspectDigests(t, a), mustInspectDigests(t, b)
	if !bytes.Equal(da.pages[0].content, db.pages[0].content) {
		t.Fatal("the fixtures must share their /Contents")
	}
	wantCode(t, samePages(da.pages, db.pages), CodeIntegrityFailed, "")
	wantCode(t, flattenedPages(da.pages, db.pages), CodeIntegrityFailed, "")
	if err := samePages(da.pages, mustInspectDigests(t, a).pages); err != nil {
		t.Fatalf("same file: %v", err)
	}
	// Two unreadable pages are not "equal": they cannot be verified.
	bad := []pageDigest{{err: "page 1: cannot be decoded"}}
	wantCode(t, samePages(bad, bad), CodeIntegrityFailed, "")
	wantCode(t, flattenedPages(bad, bad), CodeIntegrityFailed, "")
	// A flatten that keeps the originals and wraps the content is accepted.
	wrapped := []pageDigest{{content: []byte("q\nBT ET\nQ\nq 1 0 0 1 0 0 cm /FmFlat1 Do Q"), resources: map[string]string{"X/a": "1", "X/FmFlat1": "2"}}}
	orig := []pageDigest{{content: []byte("BT ET"), resources: map[string]string{"X/a": "1"}}}
	if err := flattenedPages(orig, wrapped); err != nil {
		t.Fatal(err)
	}
	wrapped[0].resources["X/a"] = "9"
	wantCode(t, flattenedPages(orig, wrapped), CodeIntegrityFailed, "")
	wrapped[0].resources["X/a"] = "1"
	wrapped[0].content = []byte("Q BT ET")
	wantCode(t, flattenedPages(orig, wrapped), CodeIntegrityFailed, "")
}

func TestChangedAppearanceOfAnUntouchedFieldIsDetected(t *testing.T) {
	data := mustForm(t)
	before := mustInspectDigests(t, data)
	tampered, err := testpdf.WithReplacedAppearance(data, "untouched", "(changed) Tj")
	if err != nil {
		t.Fatal(err)
	}
	after := mustInspectDigests(t, tampered)
	// Same value, same properties, same page content: only the appearance differs.
	bf, _ := before.Find("untouched")
	af, _ := after.Find("untouched")
	if !reflect.DeepEqual(bf, af) || before.apSHA["untouched"] == after.apSHA["untouched"] || before.apDraw["untouched"] == after.apDraw["untouched"] {
		t.Fatal("the fixture must differ by the appearance only")
	}
	err = compare(before, after, map[string]expectedValue{"company": {typ: TypeText, text: "Initial Corp"}}, &Integrity{})
	wantCode(t, err, CodeIntegrityFailed, "untouched")
	// A requested field may change its appearance.
	if err := compare(before, after, map[string]expectedValue{"untouched": {typ: TypeText, text: "keep me"}}, &Integrity{}); err != nil {
		t.Fatalf("requested field: %v", err)
	}
	// The flatten draws the appearance that is in the file: for the tampered
	// file it differs from the original one.
	_, _, drawn, err := doFlatten(bg, tampered, 0)
	if err != nil {
		t.Fatal(err)
	}
	if drawn["untouched"] == before.apDraw["untouched"] || drawn["untouched"] != after.apDraw["untouched"] {
		t.Fatal("drawn digest must follow the appearance in the file")
	}
	_, _, drawnOK, err := doFlatten(bg, data, 0)
	if err != nil {
		t.Fatal(err)
	}
	for n, d := range before.apDraw {
		if drawnOK[n] != d {
			t.Fatalf("field %s: drawn %s, original %s", n, drawnOK[n], d)
		}
	}
}

func TestFlattenRefusesOptionalContentWidgets(t *testing.T) {
	data, err := testpdf.WithOptionalContent(mustForm(t), "untouched")
	if err != nil {
		t.Fatal(err)
	}
	out, in, err := Fill(bg, data, map[string]any{"company": "OC"}, true, 0)
	wantCode(t, err, CodeFlattenFailed, "untouched")
	if out != nil || in != nil {
		t.Fatal("no output expected")
	}
	if _, _, err := Fill(bg, data, map[string]any{"company": "OC"}, false, 0); err != nil {
		t.Fatalf("without flatten the widget is left as is: %v", err)
	}
}

func TestFlattenRegeneratesTheAppearanceOfAnUnchangedRequestedField(t *testing.T) {
	data := mustForm(t)
	tampered, err := testpdf.WithReplacedAppearance(data, "company", "(Autre societe) Tj")
	if err != nil {
		t.Fatal(err)
	}
	stale := mustInspectDigests(t, tampered).apDraw["company"]
	// Same /V as requested, stale /AP: the flatten must not freeze the stale one.
	out, in, err := Fill(bg, tampered, map[string]any{"company": "Initial Corp"}, true, 0)
	if err != nil || !in.Flattened || in.Unchanged || !in.ReadBackVerified {
		t.Fatalf("fill: %v %+v", err, in)
	}
	for k, v := range mustInspectDigests(t, out).pages[0].resources {
		if sum(v) == stale {
			t.Fatalf("the stale appearance (%s) was flattened", k)
		}
	}
	// Control: an untouched field keeps its original appearance, a genuine
	// requested one is flattened too.
	ref := mustInspectDigests(t, data)
	found := false
	for _, v := range mustInspectDigests(t, out).pages[0].resources {
		if sum(v) == ref.apDraw["untouched"] {
			found = true
		}
	}
	if !found {
		t.Fatal("the untouched field lost its original appearance")
	}
	// Every field type survives the regeneration with its value.
	for _, vals := range []map[string]any{
		{"company": "Initial Corp", "size": "Small", "gdpr": false, "vat": "", "country": "", "certs": []any{}},
	} {
		if _, in, err := Fill(bg, data, vals, true, 0); err != nil || !in.Flattened {
			t.Fatalf("%v: %v", vals, err)
		}
	}
	// A field with no other value cannot be regenerated: refused, with its name.
	doc := &Doc{Fields: []Field{{Name: "r", Type: TypeRadio, Options: []string{"A"}, Value: "A"}}}
	_, err = regenerateUnchanged(bg, data, doc, map[string]expectedValue{"r": {typ: TypeRadio, text: "A"}}, 0)
	wantCode(t, err, CodeFlattenFailed, "r")
}

func TestLiteralFeffTextIsAValueNotAnEmptyString(t *testing.T) {
	// The four characters "feff" are a value; a hex string FEFF is a lone byte order mark (empty).
	lit, err := testpdf.WithValueNoAppearance(mustForm(t), "untouched", types.StringLiteral("feff"))
	if err != nil {
		t.Fatal(err)
	}
	out, in, err := Fill(bg, lit, map[string]any{"company": "ACME"}, true, 0)
	wantCode(t, err, CodeFlattenFailed, "untouched")
	if out != nil || in != nil {
		t.Fatal("no output expected")
	}
	for name, v := range map[string]types.Object{"hex BOM": types.HexLiteral("FEFF"), "raw BOM": types.StringLiteral("\xfe\xff"), "empty": types.StringLiteral("")} {
		d, err := testpdf.WithValueNoAppearance(mustForm(t), "untouched", v)
		if err != nil {
			t.Fatal(err)
		}
		if _, in, err := Fill(bg, d, map[string]any{"company": "ACME"}, true, 0); err != nil || !in.Flattened {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if b, ok := stringBytes(types.StringLiteral("feff")); !ok || emptyText(b) {
		t.Fatal("literal text feff is not empty")
	}
	if b, ok := stringBytes(types.HexLiteral("feff")); !ok || !emptyText(b) {
		t.Fatal("hex feff is a lone BOM")
	}
}

func TestFlattenRefusesADegenerateMatrix(t *testing.T) {
	data, err := testpdf.WithHugeAppearanceBox(mustForm(t), "untouched")
	if err != nil {
		t.Fatal(err)
	}
	out, in, err := Fill(bg, data, map[string]any{"company": "Huge"}, true, 0)
	wantCode(t, err, CodeFlattenFailed, "untouched")
	if out != nil || in != nil {
		t.Fatal("no output expected")
	}
	if _, _, err := Fill(bg, data, map[string]any{"company": "Huge"}, false, 0); err != nil {
		t.Fatalf("without flatten: %v", err)
	}
}

func TestMatrixNumbersAreLossless(t *testing.T) {
	for in, want := range map[float64]string{0.5: "0.5", 2: "2", 1.5e-5: "0.000015", 123456789.125: "123456789.125", -3.25: "-3.25"} {
		if got := num(in); got != want {
			t.Fatalf("%v -> %s, want %s", in, got, want)
		}
		if strings.ContainsAny(num(in), "eE") {
			t.Fatal("exponent")
		}
	}
	if v, _ := strconv.ParseFloat(num(1.0/3), 64); v != 1.0/3 {
		t.Fatal("not lossless")
	}
}

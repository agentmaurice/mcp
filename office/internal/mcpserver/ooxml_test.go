package mcpserver

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

const (
	testCT   = `<Types xmlns="` + nsContentTypes + `"><Default Extension="rels" ContentType="x"/><Default Extension="xml" ContentType="y"/><Override PartName="/word/document.xml" ContentType="z"/></Types>`
	testDoc  = `<w:document xmlns:w="` + nsWordML + `"/>`
	relsHead = `<Relationships xmlns="` + nsRelationships + `">`
)

func fabricate(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func baseParts() map[string]string {
	return map[string]string{
		"[Content_Types].xml": testCT,
		"word/document.xml":   testDoc,
		"_rels/.rels":         relsHead + `<Relationship Id="r1" Type="t" Target="word/document.xml"/></Relationships>`,
	}
}

func TestCheckOutputRelationshipsAndContentTypes(t *testing.T) {
	with := func(extra map[string]string) []byte {
		p := baseParts()
		for k, v := range extra {
			p[k] = v
		}
		return fabricate(t, p)
	}
	rel := func(target, mode string) string {
		return relsHead + `<Relationship Id="r" Type="t" Target="` + target + `"` + mode + `/></Relationships>`
	}
	ok := map[string][]byte{
		"base":              fabricate(t, baseParts()),
		"relative target":   with(map[string]string{"word/_rels/document.xml.rels": rel("styles.xml", ""), "word/styles.xml": "<a/>"}),
		"parent target":     with(map[string]string{"word/_rels/document.xml.rels": rel("../docProps/app.xml", ""), "docProps/app.xml": "<a/>"}),
		"absolute target":   with(map[string]string{"word/_rels/document.xml.rels": rel("/docProps/app.xml", ""), "docProps/app.xml": "<a/>"}),
		"external ignored":  with(map[string]string{"word/_rels/document.xml.rels": rel("https://example.com/x", ` TargetMode="External"`)}),
		"fragment":          with(map[string]string{"word/_rels/document.xml.rels": rel("#bookmark", "")}),
		"percent-encoded":   with(map[string]string{"word/_rels/document.xml.rels": rel("my%20file.xml", ""), "word/my file.xml": "<a/>"}),
		"default extension": with(map[string]string{"customXml/item1.xml": "<a/>"}),
	}
	for name, data := range ok {
		if err := checkOutput("docx", data, 1<<20); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string][]byte{
		"missing target":     with(map[string]string{"word/_rels/document.xml.rels": rel("styles.xml", "")}),
		"root missing":       with(map[string]string{"_rels/.rels": rel("word/nope.xml", "")}),
		"escapes archive":    with(map[string]string{"_rels/.rels": rel("../word/document.xml", "")}),
		"escapes from part":  with(map[string]string{"word/_rels/document.xml.rels": rel("../../x.xml", "")}),
		"empty target":       with(map[string]string{"word/_rels/document.xml.rels": rel("", "")}),
		"malformed rels":     with(map[string]string{"word/_rels/document.xml.rels": "<Relationships"}),
		"no content type":    with(map[string]string{"word/data.bin": "x"}),
		"no extension":       with(map[string]string{"word/README": "x"}),
		"no override, other": with(map[string]string{"word/x.png": "x"}),
	}
	for name, data := range bad {
		err := checkOutput("docx", data, 1<<20)
		if te, isTE := err.(*toolError); !isTE || te.code != codeInvalidOutput {
			t.Errorf("%s: %v", name, err)
		}
	}
	// the message names the cause without echoing package content
	err := checkOutput("docx", bad["no content type"], 1<<20)
	if err == nil || !strings.Contains(err.Error(), "content_types") && !strings.Contains(err.Error(), "Content_Types") {
		t.Errorf("message: %v", err)
	}
}

func TestOOXMLReviewFindings(t *testing.T) {
	invalid := func(name string, data []byte) {
		t.Helper()
		err := checkOutput("docx", data, 1<<20)
		if te, ok := err.(*toolError); !ok || te.code != codeInvalidOutput {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a declaration without ContentType (absent or empty) types nothing
	p := baseParts()
	p["[Content_Types].xml"] = `<Types xmlns="` + nsContentTypes + `"><Default Extension="xml"/><Default Extension="rels" ContentType=""/></Types>`
	invalid("empty content types", fabricate(t, p))
	// the encoded target designates "missing file.xml", not the raw entry name
	p = baseParts()
	p["word/_rels/document.xml.rels"] = relsHead + `<Relationship Id="r" Type="t" Target="missing%20file.xml"/></Relationships>`
	p["word/missing%20file.xml"] = "<a/>"
	invalid("raw name shortcut", fabricate(t, p))
	// an encoded dot-dot cannot climb out either
	p = baseParts()
	p["_rels/.rels"] = relsHead + `<Relationship Id="r" Type="t" Target="%2e%2e/x.xml"/></Relationships>`
	invalid("encoded escape", fabricate(t, p))
}

func TestOOXMLSecondReviewFindings(t *testing.T) {
	invalid := func(name string, p map[string]string) {
		t.Helper()
		err := checkOutput("docx", fabricate(t, p), 1<<20)
		if te, ok := err.(*toolError); !ok || te.code != codeInvalidOutput {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a rels part whose source part is absent, even with a fragment-only target
	p := baseParts()
	p["word/_rels/absent.xml.rels"] = relsHead + `<Relationship Id="r" Type="t" Target="#bookmark"/></Relationships>`
	invalid("orphan rels", p)
	// ... and accepted once the source part exists
	p["word/absent.xml"] = "<a/>"
	if err := checkOutput("docx", fabricate(t, p), 1<<20); err != nil {
		t.Errorf("rels with source: %v", err)
	}
	// Default needs Extension, never PartName
	p = baseParts()
	p["word/data.bin"] = "x"
	p["[Content_Types].xml"] = strings.Replace(testCT, "</Types>", `<Default PartName="bin" ContentType="application/octet-stream"/></Types>`, 1)
	invalid("default with PartName", p)
	// Override needs PartName, never Extension
	p = baseParts()
	p["word/data.bin"] = "x"
	p["[Content_Types].xml"] = strings.Replace(testCT, "</Types>", `<Override Extension="bin" ContentType="application/octet-stream"/></Types>`, 1)
	invalid("override with Extension", p)
}

func TestOOXMLNamespaceAndTargetModeStrictness(t *testing.T) {
	const x = ` xmlns:x="urn:other"`
	invalid := func(name string, p map[string]string) {
		t.Helper()
		err := checkOutput("docx", fabricate(t, p), 1<<20)
		if te, ok := err.(*toolError); !ok || te.code != codeInvalidOutput {
			t.Errorf("%s: %v", name, err)
		}
	}
	rels := func(attrs string) map[string]string {
		p := baseParts()
		p["word/_rels/document.xml.rels"] = `<Relationships xmlns="` + nsRelationships + `"` + x + `><Relationship ` + attrs + `/></Relationships>`
		return p
	}
	ct := func(body string) map[string]string {
		p := baseParts()
		p["word/data.bin"] = "x"
		p["[Content_Types].xml"] = `<Types xmlns="` + nsContentTypes + `"` + x + `><Default Extension="rels" ContentType="a"/><Default Extension="xml" ContentType="b"/><Override PartName="/word/document.xml" ContentType="c"/>` + body + `</Types>`
		return p
	}
	// qualified TargetMode does not make the target external
	invalid("x:TargetMode", rels(`Id="r" Type="t" Target="gone.xml" x:TargetMode="External"`))
	// qualified Target / Id do not count
	invalid("x:Target", rels(`Id="r" Type="t" x:Target="document.xml"`))
	invalid("x:Id", rels(`x:Id="r" Type="t" Target="document.xml"`))
	// TargetMode is exactly External or Internal
	invalid("TargetMode external lowercase", rels(`Id="r" Type="t" Target="https://e.x/" TargetMode="external"`))
	invalid("TargetMode junk", rels(`Id="r" Type="t" Target="document.xml" TargetMode="Other"`))
	if err := checkOutput("docx", fabricate(t, rels(`Id="r" Type="t" Target="document.xml" TargetMode="Internal"`)), 1<<20); err != nil {
		t.Errorf("Internal: %v", err)
	}
	if err := checkOutput("docx", fabricate(t, rels(`Id="r" Type="t" Target="https://e.x/" TargetMode="External"`)), 1<<20); err != nil {
		t.Errorf("External: %v", err)
	}
	// qualified ContentType / Extension / PartName do not type a part
	invalid("x:ContentType", ct(`<Default Extension="bin" x:ContentType="d"/>`))
	invalid("x:Extension", ct(`<Default x:Extension="bin" ContentType="d"/>`))
	invalid("x:PartName", ct(`<Override x:PartName="/word/data.bin" ContentType="d"/>`))
	// elements in another namespace do not count
	invalid("foreign Default", ct(`<x:Default Extension="bin" ContentType="d"/>`))
	invalid("foreign Override", ct(`<x:Override PartName="/word/data.bin" ContentType="d"/>`))
	// a Relationship element of another namespace is not a relationship
	p := rels(`Id="r" Type="t" Target="gone.xml"`)
	p["word/_rels/document.xml.rels"] = strings.Replace(p["word/_rels/document.xml.rels"], "<Relationship ", "<x:Relationship ", 1)
	if err := checkOutput("docx", fabricate(t, p), 1<<20); err != nil {
		t.Errorf("foreign Relationship must be ignored: %v", err)
	}
	// control: the same data.bin is accepted with unqualified attributes
	if err := checkOutput("docx", fabricate(t, ct(`<Default Extension="bin" ContentType="d"/>`)), 1<<20); err != nil {
		t.Errorf("control: %v", err)
	}
}

func TestOOXMLTargetMustBeARelativeReference(t *testing.T) {
	for _, target := range []string{
		"https://example.com/a.xml", "mailto:a@b.c", "file:x.xml", "//example.com/a.xml", "a.xml?x=1", "a.xml?",
		`word\\a.xml`, "a%5Cb.xml", "sub:dir/a.xml", "%3Ax/a.xml", "ht%74ps://x/a.xml",
	} {
		p := baseParts()
		p["word/_rels/document.xml.rels"] = relsHead + `<Relationship Id="r" Type="t" Target="` + target + `"/></Relationships>`
		// even with an entry that would match once the text is concatenated
		p["word/https:/example.com/a.xml"] = "<a/>"
		p["word/a.xml"] = "<a/>"
		err := checkOutput("docx", fabricate(t, p), 1<<20)
		if te, ok := err.(*toolError); !ok || te.code != codeInvalidOutput {
			t.Errorf("%q: %v", target, err)
		}
	}
	// still valid: relative paths, a colon after the first segment, fragments
	p := baseParts()
	p["word/_rels/document.xml.rels"] = relsHead + `<Relationship Id="r" Type="t" Target="sub/a:b.xml#frag"/></Relationships>`
	p["word/sub/a:b.xml"] = "<a/>"
	if err := checkOutput("docx", fabricate(t, p), 1<<20); err != nil {
		t.Errorf("relative with colon after first segment: %v", err)
	}
}

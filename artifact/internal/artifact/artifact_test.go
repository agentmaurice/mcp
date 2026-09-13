package artifact

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCreateJSONAndCSVDeterministically(t *testing.T) {
	jsonResult, err := Create("json", map[string]interface{}{"answer": 42})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.StdEncoding.DecodeString(jsonResult.ContentBase64)
	if !strings.Contains(string(payload), `"answer": 42`) {
		t.Fatalf("unexpected JSON: %s", payload)
	}
	csvResult, err := Create("csv", []interface{}{map[string]interface{}{"b": 2, "a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	csvPayload, _ := base64.StdEncoding.DecodeString(csvResult.ContentBase64)
	if string(csvPayload) != "a,b\n1,2\n" {
		t.Fatalf("unexpected CSV: %q", csvPayload)
	}
}
func TestPatchAndRenderPDF(t *testing.T) {
	patched, err := ApplyPatch("markdown", "# Old", []Patch{{Op: "replace", Find: "Old", Value: "New"}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.StdEncoding.DecodeString(patched.ContentBase64)
	rendered, err := Render("markdown", string(payload), "pdf")
	if err != nil {
		t.Fatal(err)
	}
	pdf, _ := base64.StdEncoding.DecodeString(rendered.ContentBase64)
	if !strings.HasPrefix(string(pdf), "%PDF-1.4") {
		t.Fatalf("invalid PDF: %q", pdf[:8])
	}
	if _, err := Inspect("pdf", pdf); err != nil {
		t.Fatal(err)
	}
}

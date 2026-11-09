package mcpserver

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/form/internal/pdfform"
	"github.com/mark3labs/mcp-go/mcp"
)

const inspectDescription = "List the AcroForm fields of a PDF: full name, type (text, checkbox, radio, listbox, combobox, signature), current value, options, read-only, required and maximum length. Reports encryption, XFA and signatures, and whether the file can be filled."

const fillDescription = "Fill the AcroForm fields of a PDF by field name, as one atomic batch (all or nothing). Unknown fields, wrong types, unknown options and values that are too long are refused with the field name. Encrypted, signed and XFA PDFs are refused, never flattened or re-signed. The output is validated, every value is read back, and no other field or page may change; the SHA-256 of the input and of the output are returned. Optional flatten (default false)."

func fileSchema(desc string) map[string]any {
	return map[string]any{"type": "object", "description": desc,
		"properties": map[string]any{
			"content_base64": map[string]any{"type": "string", "description": "The PDF, base64 encoded (20 MiB decoded at most)."},
			"ref":            map[string]any{"type": "string", "description": "A buffer:// reference, read from the Buffer service when one is configured."},
		}}
}

func inspectSchema() mcp.ToolInputSchema {
	return schema(map[string]any{"file": fileSchema("The PDF to inspect.")}, []string{"file"})
}

func fillSchema() mcp.ToolInputSchema {
	return schema(map[string]any{
		"file": fileSchema("The PDF to fill."),
		"values": map[string]any{"type": "object", "description": "Values by fully qualified field name: a string (text, radio, combobox, listbox), a boolean (checkbox) or a list of strings (multiple listbox).",
			"additionalProperties": map[string]any{"oneOf": []any{
				map[string]any{"type": "string"}, map[string]any{"type": "boolean"},
				map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}},
		"flatten":  map[string]any{"type": "boolean", "description": "Draw the values into the page and remove the fields (default false)."},
		"filename": map[string]any{"type": "string", "description": "Name of the produced file (default filled.pdf)."},
	}, []string{"file", "values"})
}

func (h *handlers) inspect(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		File fileInput `json:"file"`
	}
	if err := decode(request, &in); err != nil {
		return failure(codeInvalidArguments, err.Error()), nil
	}
	data, err := h.load(ctx, "file", in.File)
	if err != nil {
		return fail(err), nil
	}
	doc, err := pdfform.Inspect(ctx, data, h.deps.MaxInputBytes)
	if err != nil {
		return fail(err), nil
	}
	body := map[string]any{
		"status": "ok", "bytes": len(data), "input_sha256": pdfform.SHA(data),
		"pages": doc.Pages, "encrypted": doc.Encrypted, "xfa": doc.XFA, "signed": doc.Signed, "signature_fields": doc.Signatures,
		"field_count": len(doc.Fields),
	}
	if e := doc.Refusal(); e != nil {
		body["fillable"], body["refusal_code"], body["refusal_message"] = false, e.Code, e.Message
	} else if len(doc.Fields) == 0 {
		body["fillable"], body["refusal_code"], body["refusal_message"] = false, pdfform.CodeNoForm, "the PDF has no fillable AcroForm field"
	} else {
		body["fillable"] = true
	}
	fields := doc.Fields
	if len(fields) > maxListedFields {
		fields, body["fields_truncated"] = fields[:maxListedFields], true
	}
	if fields == nil {
		fields = []pdfform.Field{}
	}
	body["fields"] = fields
	if doc.Unnamed > 0 {
		body["unnamed_fields_ignored"] = doc.Unnamed
	}
	return result(body), nil
}

func (h *handlers) fill(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		File     fileInput                  `json:"file"`
		Values   map[string]json.RawMessage `json:"values"`
		Flatten  bool                       `json:"flatten"`
		Filename string                     `json:"filename"`
	}
	if err := decode(request, &in); err != nil {
		return failure(codeInvalidArguments, err.Error()), nil
	}
	if len(in.Values) == 0 {
		return failure(codeInvalidArguments, "values must name at least one field"), nil
	}
	if len(in.Values) > h.deps.MaxValues {
		return failure(codeInvalidArguments, "values names too many fields"), nil
	}
	total := 0
	values := make(map[string]any, len(in.Values))
	for name, raw := range in.Values {
		total += len(name) + len(raw)
		if total > maxValuesBytes {
			return failure(codeInvalidArguments, "values exceeds the size limit"), nil
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return failure(codeInvalidArguments, "values."+name+" is not valid JSON"), nil
		}
		switch v.(type) {
		case string, bool, []any:
		default:
			return failureWith(pdfform.CodeTypeMismatch, "field "+quote(name)+" expects a string, a boolean or a list of strings", map[string]any{"field": name}), nil
		}
		values[name] = v
	}
	data, err := h.load(ctx, "file", in.File)
	if err != nil {
		return fail(err), nil
	}
	out, integrity, err := pdfform.Fill(ctx, data, values, in.Flatten, h.deps.MaxInputBytes)
	if err != nil {
		return fail(err), nil
	}
	if err := checkOutput(out, h.deps.MaxInputBytes*2); err != nil {
		return fail(err), nil
	}
	outcome := "filled"
	if integrity.Unchanged {
		outcome = "unchanged"
	}
	body := map[string]any{"status": "ok", "outcome": outcome, "integrity": integrity, "flattened": integrity.Flattened}
	if err := h.emit(ctx, "form_fill_v1", cleanFilename(in.Filename, "filled"), out, body); err != nil {
		return fail(err), nil
	}
	return result(body), nil
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `'`) + `"` }

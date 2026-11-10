package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"time"

	artifactengine "github.com/agentmaurice/mcpchatui/mcp/artifact/internal/artifact"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/interchange"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/mjml"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	renderDescription = "Render a template/v1 folder (inline manifest and files) with data and the platform context to a deterministic pdf, png, html, txt or eml (message profile, engine mjml: html, txt and eml, no sending), with PNG previews of every page and a provenance manifest. Without `template`, output ics, vcf or csv renders typed `data` (closed schema, see capabilities) to an iCalendar, vCard 4.0 or normalized CSV file. Without `template`, the v0.1 call (format, content, output_format) still renders Markdown or HTML to a simple PDF."
	// Largest request accepted by the template render (template + data + assets).
	maxRenderRequestBytes = 4 * 1024 * 1024
	// Budget for inline PNG previews when no Buffer is configured.
	maxInlinePreviewBytes = 4 * 1024 * 1024
)

// Deps are the collaborators of the server.
type Deps struct {
	Compiler template.Compiler     // nil when no Typst engine is configured
	MJML     template.MJMLCompiler // nil when no MJML engine is configured
	Buffer   *buffer.Client        // nil when the Buffer service is not configured
	Options  template.Options
}

type handlers struct{ deps Deps }

// DepsFromEnv reads the engine, limits and Buffer from the environment:
// TYPST_BIN, MJML_NODE_BIN, MJML_DIR, ARTIFACT_FONT_PATH, ARTIFACT_RENDER_TIMEOUT_SECONDS (60),
// ARTIFACT_MAX_PAGES (50), ARTIFACT_MAX_TEMPLATE_BYTES (1 MiB),
// ARTIFACT_RENDER_MAX_MEMORY_MB, and the ARTIFACT_BUFFER_* variables.
func DepsFromEnv() Deps {
	var d Deps
	if r := typst.FromEnv(); r.Available() {
		d.Compiler = r
	}
	if r := mjml.FromEnv(); r.Available() {
		d.MJML = r
	}
	d.Buffer = buffer.FromEnv()
	d.Options = template.Options{Timeout: 60 * time.Second}
	if v, err := strconv.Atoi(os.Getenv("ARTIFACT_RENDER_TIMEOUT_SECONDS")); err == nil && v > 0 {
		d.Options.Timeout = time.Duration(v) * time.Second
	}
	if v, err := strconv.Atoi(os.Getenv("ARTIFACT_MAX_PAGES")); err == nil && v > 0 {
		d.Options.MaxPages = v
	}
	if v, err := strconv.Atoi(os.Getenv("ARTIFACT_MAX_TEMPLATE_BYTES")); err == nil && v > 0 {
		d.Options.MaxTemplateBytes = v
	}
	return d
}

func (h *handlers) options() template.Options {
	o := h.deps.Options
	o.MJML = h.deps.MJML
	if o.MaxPages <= 0 {
		o.MaxPages = template.DefaultMaxPages
	}
	if o.MaxTemplateBytes <= 0 {
		o.MaxTemplateBytes = template.DefaultMaxTemplateBytes
	}
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	return o
}

func obj(props map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": props}
}

func renderSchema() mcp.ToolInputSchema {
	str := map[string]interface{}{"type": "string"}
	return schema(map[string]interface{}{
		"format":        map[string]interface{}{"type": "string", "enum": []string{"markdown", "html"}, "description": "v0.1: source format."},
		"content":       map[string]interface{}{"type": "string", "maxLength": maxArtifactBytes, "description": "v0.1: source text."},
		"output_format": map[string]interface{}{"type": "string", "enum": []string{"pdf"}, "description": "v0.1: output format."},
		"template": map[string]interface{}{"type": "object", "description": "template/v1 folder: `manifest` (object) and `files` (relative path to text content, including the entry and the input schema).",
			"properties": map[string]interface{}{"manifest": map[string]interface{}{"type": "object"}, "files": map[string]interface{}{"type": "object", "additionalProperties": str}},
			"required":   []string{"manifest", "files"}},
		"data": map[string]interface{}{"type": "object", "description": "Business data, validated against the template input_schema."},
		"context": obj(map[string]interface{}{
			"company": map[string]interface{}{"type": "object"}, "brand": map[string]interface{}{"type": "object"},
			"doc": map[string]interface{}{"type": "object"}, "i18n": map[string]interface{}{"type": "object"},
		}),
		"assets":             map[string]interface{}{"type": "object", "additionalProperties": str, "description": "Local files referenced by the brand (logo), base64, by file name."},
		"output":             map[string]interface{}{"type": "string", "enum": []string{"pdf", "png", "html", "txt", "eml", "ics", "vcf", "csv"}, "description": "pdf, png, html, txt, eml render a template (eml: message profile only); ics, vcf, csv render typed `data` with a Go engine, without template (see utility_outputs in artifact_capabilities_v1)."},
		"options":            map[string]interface{}{"type": "object", "description": "csv output only: separator, bom, line_ending, quote, expected_columns, expected_headers, neutralize_formulas."},
		"mode":               map[string]interface{}{"type": "string", "enum": []string{"preview", "final"}, "default": "preview"},
		"creation_timestamp": map[string]interface{}{"type": "integer", "description": "Fixed Unix timestamp; default is doc.date at 00:00 UTC."},
	}, nil)
}

type renderInput struct {
	Template  template.Template `json:"template"`
	Data      any               `json:"data"`
	Context   template.Context  `json:"context"`
	Assets    map[string]string `json:"assets"`
	Output    string            `json:"output"`
	Mode      string            `json:"mode"`
	Timestamp *int64            `json:"creation_timestamp"`
}

type fileOut struct {
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Bytes    int    `json:"bytes"`
	SHA256   string `json:"sha256"`
	// A pointer: an empty render is a valid result and carries content_base64 "" (never omitted).
	ContentBase64 *string `json:"content_base64,omitempty"`
	Ref           string  `json:"ref,omitempty"`
}

type previewOut struct {
	Page int `json:"page"`
	fileOut
}

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (h *handlers) renderTemplate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// One deadline for the whole call: validation, execution, compilation,
	// previews and every Buffer upload.
	ctx, cancel := context.WithTimeout(ctx, h.options().Timeout)
	defer cancel()
	raw, err := json.Marshal(request.Params.Arguments)
	if err != nil || len(raw) > maxRenderRequestBytes {
		return failure("invalid_arguments", "request exceeds the size limit"), nil
	}
	var in renderInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return failure("invalid_arguments", "arguments do not match schema: "+err.Error()), nil
	}
	if in.Output == "" {
		return failure("invalid_arguments", "output is required (pdf, png, html, txt or eml)"), nil
	}
	assets := map[string][]byte{}
	for name, enc := range in.Assets {
		body, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return failure(template.CodeAssetInvalid, "asset "+name+" is not valid base64"), nil
		}
		assets[name] = body
	}
	res, err := template.Render(ctx, template.Request{
		Template: in.Template, Data: in.Data, Context: in.Context, Assets: assets,
		Output: in.Output, Mode: in.Mode, Timestamp: in.Timestamp,
	}, h.deps.Compiler, h.options())
	if err != nil {
		var terr *template.Error
		if errors.As(err, &terr) {
			return failure(terr.Code, terr.Message), nil
		}
		return failure("artifact_render_failed", err.Error()), nil
	}

	main := fileOut{Filename: res.Filename, MimeType: res.MimeType, Bytes: len(res.Content), SHA256: hexSHA(res.Content)}
	var outputs []map[string]interface{}
	if h.deps.Buffer != nil {
		ref, err := h.deps.Buffer.Upload(ctx, main.Filename, main.MimeType, res.Content, main.SHA256)
		if err != nil {
			if ctx.Err() != nil {
				return failure(template.CodeRenderTimeout, "the render deadline passed during the Buffer upload"), nil
			}
			return failure("buffer_failed", err.Error()), nil
		}
		main.Ref = ref
		outputs = append(outputs, map[string]interface{}{"ref": ref, "mime_type": main.MimeType, "bytes": main.Bytes, "sha256": main.SHA256,
			"metadata": map[string]interface{}{"filename": main.Filename, "sha256": main.SHA256, "bytes": main.Bytes}})
	} else {
		if main.Bytes > maxArtifactBytes {
			return failure("storage_required", "rendered file exceeds the inline limit and no Buffer service is configured"), nil
		}
		enc := base64.StdEncoding.EncodeToString(res.Content)
		main.ContentBase64 = &enc
	}

	previews := make([]previewOut, 0, len(res.Previews))
	inlineBudget := maxInlinePreviewBytes
	for _, p := range res.Previews {
		po := previewOut{Page: p.Number, fileOut: fileOut{Filename: pageName(res.Filename, p.Number), MimeType: "image/png", Bytes: len(p.PNG), SHA256: hexSHA(p.PNG)}}
		switch {
		case h.deps.Buffer != nil:
			ref, err := h.deps.Buffer.Upload(ctx, po.Filename, po.MimeType, p.PNG, po.SHA256)
			if err != nil {
				if ctx.Err() != nil {
					return failure(template.CodeRenderTimeout, "the render deadline passed during the Buffer upload"), nil
				}
				return failure("buffer_failed", err.Error()), nil
			}
			po.Ref = ref
			// every preview is materialized like the main file: same shape (ref, mime_type,
			// size, sha256, metadata.filename read by the host's artifact materializer)
			outputs = append(outputs, map[string]interface{}{"ref": ref, "mime_type": po.MimeType, "bytes": po.Bytes, "sha256": po.SHA256,
				"metadata": map[string]interface{}{"filename": po.Filename, "sha256": po.SHA256, "bytes": po.Bytes, "page": po.Page}})
		case len(p.PNG) <= inlineBudget:
			inlineBudget -= len(p.PNG)
			enc := base64.StdEncoding.EncodeToString(p.PNG)
			po.ContentBase64 = &enc
		default:
			return failure("storage_required", "the page previews exceed the inline budget and no Buffer service is configured"), nil
		}
		previews = append(previews, po)
	}

	// `artifact` keeps the v0.1 shape (content and manifest) for existing callers.
	legacy := map[string]interface{}{"manifest": artifactengine.ManifestFor(formatOf(in.Output), res.Content)}
	if main.ContentBase64 != nil {
		legacy["content_base64"] = *main.ContentBase64
	}
	if main.Ref != "" {
		legacy["ref"] = main.Ref
	}
	out := map[string]interface{}{
		"status": "ok", "mode": in.Mode, "output": in.Output,
		"file": main, "pages": res.Pages, "previews": previews,
		"provenance": res.Provenance, "artifact": legacy,
	}
	if in.Mode == "" {
		out["mode"] = template.ModePreview
	}
	if len(outputs) > 0 {
		out["outputs"] = outputs
	}
	return result(out), nil
}

func formatOf(output string) string { return output }

func pageName(filename string, page int) string {
	return filename + ".page-" + strconv.Itoa(page) + ".png"
}

func (h *handlers) capabilities(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	opts := h.options()
	typstOK := false
	if h.deps.Compiler != nil {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		v, err := h.deps.Compiler.Version(cctx)
		cancel()
		typstOK = err == nil && v == template.TypstVersion
	}
	mjmlOK := false
	if h.deps.MJML != nil {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		v, err := h.deps.MJML.Version(cctx)
		cancel()
		mjmlOK = err == nil && v == template.MJMLVersion
	}
	return result(map[string]interface{}{
		"version":         "v1",
		"service_version": serviceVersion,
		"create_formats":  []string{"markdown", "html", "csv", "json", "pdf"},
		"patch_formats":   []string{"markdown", "html", "json"},
		"preview_formats": []string{"pdf"},
		"engines": []map[string]interface{}{
			{"name": "typst", "version": template.TypstVersion, "available": typstOK, "outputs": []string{"pdf", "png"}, "previews": "png_per_page"},
			{"name": "mjml", "version": template.MJMLVersion, "available": mjmlOK, "outputs": []string{"html", "txt", "eml"}, "previews": "none"},
			{"name": "html", "version": "go-template", "available": true, "outputs": []string{"html"}, "previews": "none"},
			{"name": "text", "version": "go-template", "available": true, "outputs": []string{"txt"}, "previews": "none"},
			{"name": "interchange", "version": "go-stdlib", "available": true, "outputs": []string{"ics", "vcf", "csv"}, "previews": "none"},
		},
		// Outputs rendered from typed data, without template: the closed JSON Schema of `data` (and of `options` for csv).
		"utility_outputs":   utilityCapabilities(),
		"template_contract": "agentmaurice.template/v1",
		"render_modes":      []string{template.ModePreview, template.ModeFinal},
		"limits": map[string]interface{}{
			"max_inline_bytes": maxArtifactBytes, "max_request_bytes": maxRenderRequestBytes,
			"max_template_bytes": opts.MaxTemplateBytes, "max_files": template.DefaultMaxFiles, "max_defines": template.DefaultMaxDefines, "max_schema_cost": template.MaxSchemaCost, "max_validation_work": template.MaxValidationWork, "max_output_bytes": template.DefaultMaxOutputBytes, "max_values_bytes": template.DefaultMaxValuesBytes, "max_pages": opts.MaxPages,
			"render_timeout_seconds": int(opts.Timeout / time.Second),
		},
		"error_codes": []string{
			template.CodeManifestSchema, template.CodeEntryMissing, template.CodeInputSchemaMissing, template.CodeInputSchemaInvalid,
			template.CodeReservedNamespace, template.CodeForbiddenFunction, template.CodeUnknownFunction, template.CodeTemplateSyntax,
			template.CodeTemplateExec, template.CodeFragmentUndeclared, template.CodeFragmentMissing, template.CodeLimitsExceeded,
			template.CodeTypstCodeContext, template.CodeTypstEscapedAction, template.CodeTypstRawContext, template.CodeTypstMathContext, template.CodeTypstCommentContext, template.CodeContextInvalid, template.CodeDataInvalid, template.CodeFilePathInvalid,
			template.CodeEngineUnsupported, template.CodeOutputUnsupported, template.CodePackageImport, template.CodeCompileFailed,
			template.CodeRenderTimeout, template.CodeEngineUnavailable, template.CodeEngineVersion, template.CodeAssetInvalid, template.CodeReservedFileName, template.CodeFileExtension,
			template.CodeMJMLFailed, template.CodeMJMLIncludeOutside, template.CodeMJMLIncludeDenied, template.CodeMessageOutputInvalid, template.CodeMessageTemplateResidue,
			template.CodeMessageTooHeavy, template.CodeMessageURLNotHTTPS, template.CodeMessageUnsubscribeMiss, template.CodeMessageHiddenRule, template.CodeMessageReservedChar,
			template.CodeQRInputInvalid, template.CodeQRInputTooLong, template.CodeQRLevelInvalid,
			"invalid_arguments", "storage_required", "buffer_failed",
			"invalid_artifact", "artifact_create_failed", "artifact_patch_failed", "artifact_render_failed", // v0.1 contract
		},
		"max_artifact_bytes":  maxArtifactBytes,
		"storage_integration": false,
		"buffer_configured":   h.deps.Buffer != nil,
	}), nil
}

func utilityCapabilities() []map[string]interface{} {
	var list []map[string]interface{}
	for _, u := range utilityOutputs {
		entry := map[string]interface{}{"output": u.output, "mime_type": u.mime, "filename": u.filename, "data_schema": interchange.Schema(u.output)}
		if u.output == interchange.OutputCSV {
			entry["options_schema"] = interchange.Schema("csv.options")
		}
		list = append(list, entry)
	}
	return list
}

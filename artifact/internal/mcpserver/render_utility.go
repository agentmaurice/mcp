package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	artifactengine "github.com/agentmaurice/mcpchatui/mcp/artifact/internal/artifact"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/interchange"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/mark3labs/mcp-go/mcp"
)

// utilityOutputs are the outputs of artifact_render_v1 produced by the Go
// engines of internal/interchange, without a template: the data is typed JSON
// validated against a closed schema (published by artifact_capabilities_v1).
var utilityOutputs = []struct{ output, mime, filename string }{
	{interchange.OutputICS, "text/calendar", "calendar.ics"},
	{interchange.OutputVCF, "text/vcard", "contacts.vcf"},
	{interchange.OutputCSV, "text/csv", "export.csv"},
}

func utilityOutput(output string) (mime, filename string, ok bool) {
	for _, u := range utilityOutputs {
		if u.output == output {
			return u.mime, u.filename, true
		}
	}
	return "", "", false
}

// renderUtility renders ics, vcf or csv. The arguments are `output`, `data` and,
// for csv, `options`; nothing else is accepted.
func (h *handlers) renderUtility(ctx context.Context, request mcp.CallToolRequest, output string) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	raw, err := json.Marshal(args)
	if err != nil || len(raw) > maxRenderRequestBytes {
		return failure("invalid_arguments", "request exceeds the size limit"), nil
	}
	for k := range args {
		switch k {
		case "output", "data":
		case "options":
			if output != interchange.OutputCSV {
				return failure("invalid_arguments", "options applies to the csv output only"), nil
			}
		default:
			return failure("invalid_arguments", "argument "+k+" is not accepted by the "+output+" output (arguments: output, data"+map[bool]string{true: ", options", false: ""}[output == interchange.OutputCSV]+")"), nil
		}
	}
	if _, ok := args["data"]; !ok {
		return failure("invalid_arguments", "data is required for the "+output+" output"), nil
	}
	var content []byte
	switch output {
	case interchange.OutputICS:
		content, err = interchange.RenderICS(args["data"])
	case interchange.OutputVCF:
		content, err = interchange.RenderVCF(args["data"])
	default:
		content, err = interchange.RenderCSV(args["data"], args["options"])
	}
	if err != nil {
		if errors.Is(err, interchange.ErrLimits) {
			return failure(template.CodeLimitsExceeded, err.Error()), nil
		}
		if errors.Is(err, interchange.ErrData) {
			return failure(template.CodeDataInvalid, err.Error()), nil
		}
		return failure("artifact_render_failed", err.Error()), nil
	}
	mime, filename, _ := utilityOutput(output)
	main := fileOut{Filename: filename, MimeType: mime, Bytes: len(content), SHA256: hexSHA(content)}
	out := map[string]interface{}{"status": "ok", "output": output}
	if h.deps.Buffer != nil {
		ref, err := h.deps.Buffer.Upload(ctx, main.Filename, main.MimeType, content, main.SHA256)
		if err != nil {
			return failure("buffer_failed", err.Error()), nil
		}
		main.Ref = ref
		out["outputs"] = []map[string]interface{}{{"ref": ref, "mime_type": main.MimeType, "bytes": main.Bytes, "sha256": main.SHA256,
			"metadata": map[string]interface{}{"filename": main.Filename, "sha256": main.SHA256, "bytes": main.Bytes}}}
	} else {
		if main.Bytes > maxArtifactBytes {
			return failure("storage_required", "rendered file exceeds the inline limit and no Buffer service is configured"), nil
		}
		enc := base64.StdEncoding.EncodeToString(content)
		main.ContentBase64 = &enc
	}
	legacy := map[string]interface{}{"manifest": artifactengine.ManifestFor(output, content)}
	if main.ContentBase64 != nil {
		legacy["content_base64"] = *main.ContentBase64
	}
	if main.Ref != "" {
		legacy["ref"] = main.Ref
	}
	out["file"] = main
	out["artifact"] = legacy
	out["provenance"] = map[string]interface{}{"engine": "go-" + output, "renderer": "agentmaurice-artifact", "renderer_version": serviceVersion, "input_sha256": hexSHA(raw), "output_sha256": main.SHA256}
	return result(out), nil
}

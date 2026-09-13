# AgentMaurice Artifact MCP

Go MCP server for creating, patching, rendering and inspecting deterministic
deliverables. Every result carries a provenance manifest with format, MIME
type, size, renderer and SHA-256.

Tools:

- `artifact_health_v1`
- `artifact_capabilities_v1`
- `artifact_create_v1`
- `artifact_patch_v1`
- `artifact_render_v1`
- `artifact_inspect_v1`

v0.1 creates Markdown, HTML, CSV, JSON and PDF, and renders a simple PDF
preview. Inline artifacts are capped at 2 MiB. Storage upload and the DOCX,
XLSX and PPTX formats are out of scope for this first slice.

## License

Apache-2.0.

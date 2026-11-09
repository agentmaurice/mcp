# AgentMaurice Form MCP

Go MCP server that **inspects and fills the AcroForm fields of an existing PDF**
without breaking the rest of the file. It is built on
[pdfcpu](https://github.com/pdfcpu/pdfcpu) (Apache-2.0, `v0.16.4`), used as a Go
module through its Go API (no CLI, no external process).

It does not decide what to answer: the source of each answer (Company Memory,
company profile) belongs to the module that calls it. The MCP only reads fields,
validates a batch of values and writes them back, with proof.

Module: `github.com/agentmaurice/mcpchatui/mcp/form`. Stdio and HTTP transports
come from `mcp/shared/modernmcp`; the image is a sidecar image
(`Dockerfile.sidecar`).

## Tools

| Tool | Purpose |
| --- | --- |
| `form_health_v1` | `{status:"ok", version}` |
| `form_capabilities_v1` | version, engine (pdfcpu, version, license), field types, refusals, limits, error codes |
| `form_inspect_v1` | `{file}`; pages, `encrypted`, `xfa`, `signed`, `signature_fields`, `fillable` (with `refusal_code` and `refusal_message` when false) and `fields[]` |
| `form_fill_v1` | `{file, values, flatten?, filename?}`; atomic fill of the fields named in `values`, then an integrity check; returns the filled PDF |

`file` is `{content_base64}` (20 MiB decoded at most) or `{ref}` (a `buffer://`
reference read from the Buffer service; without a Buffer the error is
`buffer_unavailable`).

### `form_inspect_v1`

Each field: `name` (fully qualified, the key to use in `form_fill_v1`), `type`
(`text`, `checkbox`, `radio`, `listbox`, `combobox`, `signature`, `button`),
`subtype` (`date` for a formatted date field), `value` (string, boolean for a
checkbox, list for a multiple list box), `options`, `read_only`, `required`,
`max_length`, `multiline`, `editable`, `multiple`, `signed`, `tooltip`, `pages`.
A file that cannot be filled is still described, with the reason: an
encrypted, signed or XFA PDF is reported, not an error.

### `form_fill_v1`

* **Atomic**: the whole batch is validated first; one refusal means no output.
* **Validation, with the field name** (`details.field`): `form_unknown_field`,
  `form_type_mismatch`, `form_unknown_option`, `form_value_too_long`
  (maximum length counted in characters), `form_field_readonly`,
  `form_invalid_value` (a line break in a single-line field, or any carriage return: write line breaks as `\n`).
  A combo box only takes one of its options, even when editable: pdfcpu drops
  any other value.
* **Refused files**: `form_encrypted`, `form_signed`, `form_xfa_unsupported`,
  `form_no_fields`. A signed file is never rewritten, so its signature is never
  invalidated; an XFA form is not converted; an encrypted file is never
  decrypted or re-encrypted.
* **`flatten`** (default `false`): draws the appearances into the page content
  and removes the widgets and the AcroForm dictionary. pdfcpu v0.16.4 has no
  flatten API, so this is done on its public object model (appearance streams
  placed with the matrix of the PDF reference, 12.5.5, algorithm 8.1).
  Hidden widgets are dropped; a widget with optional content (`/OC`) is refused
  (`form_flatten_failed`). A requested field whose value is already in place gets
  its appearance regenerated (pdfcpu is made to rewrite it), so a stale `/AP` is
  never frozen; if that is impossible the flatten is refused. An output that still has fields, XFA or a
  signature is refused.
* **Result**: `{status, outcome, flattened, file, outputs?, integrity}`.
  `outcome` is `filled` (also for a flatten, which always changes the file, with `integrity.unchanged` false), or `unchanged` when every requested value was already
  in place and `flatten` is off (the input is then returned as is, same SHA-256). `file` carries
  `filename`, `mime_type`, `bytes`, `sha256` and either `content_base64`
  (2 MiB at most inline, otherwise `storage_required`) or `ref` with an
  `outputs[]` entry (`ref`, `mime_type`, `bytes`, `sha256`, `metadata`) when a
  Buffer is configured.
* **Integrity** (`integrity`): the produced PDF is read back with pdfcpu and
  validated; the page count is the same; every written value is read back
  identically; no field that was not requested changed (value, options, flags,
  and a deep digest of its appearance streams /AP N, D and R, which must also be
  the ones drawn by a flatten); every page keeps its decoded content and every
  resource it can use (fonts, XObjects including images and Form XObjects,
  graphic states, color spaces, patterns, shadings, digested in depth with
  bounded recursion), strictly equal without `flatten`; with `flatten`, the
  original content is kept wrapped in `q ... Q` and every original resource
  stays present and identical; a page that cannot be digested is never
  "unchanged"; no signature appeared; `input_sha256` and
  `output_sha256`. Any mismatch is `form_integrity_failed` and no file is
  returned.

pdfcpu rewrites the whole file (it does not append an incremental update); the
page content, the other fields and the page count are preserved and checked, the
byte layout is not.

### Errors

An `isError` result with `{error, message}` (and `details.field` when a field is
at fault). Stable codes: `invalid_arguments`, `file_too_large`, `invalid_pdf`,
`form_no_fields`, `form_encrypted`, `form_signed`, `form_xfa_unsupported`,
`form_unknown_field`, `form_type_mismatch`, `form_unknown_option`,
`form_value_too_long`, `form_field_readonly`, `form_invalid_value`,
`form_flatten_failed`, `form_integrity_failed`, `form_fill_failed`,
`form_cancelled`, `invalid_output`, `storage_required`, `buffer_unavailable`, `buffer_not_found`,
`buffer_failed`.

The One adds `deployment_id`, `organization_id`, `user_id`, `space_id` and
`session_id` to every tool call: they are removed before the strict validation
(any other unknown argument is refused with `invalid_arguments`).

## Configuration

| Variable | Default | Role |
| --- | --- | --- |
| `FORM_MAX_INPUT_BYTES` | 20 MiB | input size limit (also applied by pdfcpu) |
| `FORM_MAX_VALUES` | 2000 | fields per fill |
| `BUFFER_SERVICE_URL`, `BUFFER_TOKEN`, `BUFFER_NAMESPACE` | none, namespace `form` | Buffer for `buffer://` input and output; `FORM_BUFFER_*` aliases accepted |
| `MCP_*` | | sidecar and transport settings from `mcp/shared` |

pdfcpu runs with a stateless configuration (no configuration directory written)
and offline.

## Development

```
task check         # gofmt, go vet, go test -race, build
```

The module depends on `mcp/shared` through `replace ... => ../shared` (the CI
checks it out next to this repository at `MCP_SHARED_REF`). Tests generate their
PDFs with pdfcpu (`internal/testpdf`): a two-page form with text, checkbox,
radio, combo and list fields, plus XFA, signed and encrypted variants derived
from it. No binary fixture is checked in.

## Third-party software

pdfcpu is Apache-2.0: see `NOTICE` and `LICENSE.pdfcpu`, both shipped in the
image under `/usr/share/doc/form/`.

# AgentMaurice Artifact MCP

Go MCP server for creating, patching, rendering and inspecting deterministic
deliverables. Every result carries a provenance manifest with format, MIME
type, size, renderer and SHA-256. Version 0.2.0 adds the rendering engine of
Document studio: it renders `template/v1` folders to PDF, PNG, HTML or text.

Tools:

- `artifact_health_v1`
- `artifact_capabilities_v1` (engines, template contract, limits, error codes)
- `artifact_create_v1`
- `artifact_patch_v1`
- `artifact_render_v1`
- `artifact_inspect_v1`

v0.1 creates Markdown, HTML, CSV, JSON and PDF, and renders a simple PDF
preview. Inline artifacts are capped at 2 MiB. The DOCX, XLSX and PPTX
formats, MJML, Kroki and pdfcpu are out of scope for 0.2.

## Rendering templates (`artifact_render_v1`)

A template is a `template/v1` folder of text files (manifest, entry, JSON
schema of the business data), provided inline. The call:

| Argument | Meaning |
| --- | --- |
| `template` | `{manifest, files}`: the manifest object and the folder files by relative path |
| `data` | business data, validated against the template `input_schema` |
| `context` | `{company, brand, doc, i18n}`: the reserved namespaces injected by the platform |
| `assets` | local files (logo, images; svg, png, jpg; up to 4 MiB in total), base64, by relative path; all are written into the Typst project at their normalized path |
| `output` | `pdf`, `png`, `html` or `txt` (must be declared by the manifest and produced by its engine) |
| `mode` | `preview` (default, previews at 72 ppi) or `final` (144 ppi) |
| `creation_timestamp` | optional fixed Unix time; default is `doc.date` at 00:00 UTC |

A call without `template` keeps the v0.1 contract (`format`, `content`,
`output_format`): its response is identical to the one of origin/main (same
fields, bytes, `renderer_version` `0.1.0` and error codes), checked against
recorded responses in `testdata/v01_origin_main.json`. Differences with 0.1, all
additive: `artifact_health_v1` reports `version` `0.2.0`, `artifact_capabilities_v1`
has more fields (existing keys unchanged), and the input schema of
`artifact_render_v1` lists the optional template arguments (no field is required
by the schema any more; v0.1 arguments are still validated by the tool).

## Chart fragments

The template library embedded in the engine includes `fragments/charts.typ`: six
pure-Typst chart fragments (`bar-chart`, `line-chart`, `pie-chart`, `gantt-chart`,
`org-chart`, `process-flow`), with the data contract `chart.schema.json` (`chart/v1`).
They draw vector graphics only, with the colours of the brand charter, French number
formatting and bounded inputs: out-of-bounds data fails the render with a compile
error whose message starts with `chart/v1:`. A template declares the fragment in
`fragments` and passes the chart data as JSON inside a content block. The files are
vendored from pilote-maurice `contracts/template/v1` (see `internal/template/contract/PIN`).

## Utility outputs: `ics`, `vcf`, `csv` (`artifact_render_v1`, no template)

`artifact_render_v1` also renders typed business data to an iCalendar (RFC 5545),
a vCard 4.0 (RFC 6350) or a normalized CSV file, with a Go engine
(`internal/interchange`, standard library only). These outputs have no template:
the call carries `output` and `data` (and `options` for `csv`), nothing else
(`invalid_arguments` otherwise). `data` is validated against a closed JSON Schema
(`additionalProperties: false` at every level, published as `utility_outputs` by
`artifact_capabilities_v1`), then decoded strictly; no free text is ever
interpreted. Errors reuse the existing codes: `data_invalid` (schema or semantic
rule, with the path), `limits_exceeded` (bounds), `invalid_arguments`,
`storage_required`, `buffer_failed`.

- `ics`: `{name?, method?, events: [{uid?, dtstamp?, start, end | duration_minutes, all_day?, timezone?, summary, description?, location?, url?, status?, organizer?, attendees?, rrule?}]}`.
  A `start` with a zone designator (RFC 3339) is an instant: UTC (`...Z`) or, with
  `timezone`, `TZID` plus an embedded `VTIMEZONE` computed from the IANA data
  compiled into the binary; a local `start` (`2026-11-10T09:30`) requires
  `timezone`. All-day events use dates (`end` is exclusive) or `duration_days` (`P<n>D`); `duration_minutes` is for timed events only. `rrule`: `freq`,
  `interval`, `count` or `until`, `by_day`; with a timezone other than UTC the rule must be bounded (`count` or `until`, at most 100 years) and the VTIMEZONE covers all its occurrences. `uid` defaults to a digest of the
  event, `dtstamp` to the start in UTC, so the output is identical at every render.
- `vcf`: `{cards: [{uid?, fn, n?, org?, title?, email?, tel?, adr?, url?, note?}]}`;
  `email`, `tel`, `adr`, `url` are arrays, `org` lists the organization then its units.
  `tel` is a `tel:` URI for a global number (`+` country code), and text (`VALUE=text`, form kept) for a local one. `uid` defaults to a UUID derived from the card.
- `csv`: `{headers?, rows: [[cell...] | {header: cell}]}`, a cell being a string,
  number, boolean or null. `options`: `separator` (`,` `;` tab `|`), `bom`,
  `line_ending` (`crlf` default, `lf`), `quote` (`minimal`, `all`),
  `expected_columns`, `expected_headers`, `neutralize_formulas`. Every row must
  have as many columns as the headers (or the first row). `artifact_create_v1`
  with `csv` uses the same writer with the zero options (comma, LF, minimal
  quoting) and none of the bounds below: its output and limits are those of v0.1.

A CSV cell is never rewritten: a cell with a line break (CR, LF or CRLF) is quoted and written as given, whatever `line_ending` and `quote` are; `line_ending` applies to record ends only.

Text values are escaped (backslash, semicolon, comma, line breaks) and every line
is folded at 75 octets with CRLF, without splitting a UTF-8 sequence. Bounds:
500 events or cards, 100 attendees per event, 32 KiB per text field or CSV cell,
50 000 CSV rows, 256 columns, 8 MiB of output (these bounds apply to `artifact_render_v1` only) (2 MiB inline without Buffer).

Engines: **Typst 0.15.1** (`pdf`, `png`; the version is enforced), `html`
(`html/template`, contextual escaping) and `text` (`txt`). Typst packages
(`@preview/...`) are not supported in v0.2: any string literal that looks like a
package specification (`"@name/..."`) is refused (`package_import_forbidden`), and
at runtime Typst gets empty package directories, a minimal environment (HOME and
XDG_* inside the per-call project), dead proxies (`127.0.0.1:9`) and, with
Docker, `--network none`, so a package import fails by compilation
(`compile_failed`). All files of the
folder are written into the Typst project under their relative path (extensions
`typ`, `svg`, `png`, `jpg`, `json`, `csv`); `.typ` files other than the entry are
executed like the entry, with the same by-reference values. `brand.typ`,
`values.json`, `out.pdf`, `page-*.png` and `fragments/` are reserved
(`reserved_file_name`). Template execution runs under the render timeout
(`render_timeout`) and the Typst values table (`values.json`, every value passed by reference) is capped at 8 MiB in total (the JSON-encoded size written in the file, separators included), counted before each value is stored (`limits_exceeded`); each output, including each compiled PDF or PNG file, is capped at 20 MiB
(`limits_exceeded`, checked before any upload to the Buffer). Template paths are
normalized with `path.Clean`; two paths that collide after normalization are
refused (`file_path_invalid`).

The render validates first: manifest against the contract schema, paths,
declared output, `input_schema` (closed keywords, reserved namespaces), data
against `input_schema`, the context, limits, fragments, forbidden and unknown
template functions. It then executes the template with the contract semantics
(Typst values are passed by reference through `values.json`, never written
into the source) and compiles. Static analysis of optional-property guards
belongs to `maurice template check`; an unguarded absent value still fails at
execution (`template_exec`).

Result (structured content):

- `file`: `filename`, `mime_type`, `bytes`, `sha256` and either `content_base64`
  or a `buffer://` `ref` (see Storage below). For `png` the file is the first page.
- `pages` and `previews`: Typst outputs (`pdf`, `png`) carry one PNG per page (`page`, `sha256`, `content_base64`
  or `ref`). Without the Buffer, previews are inline up to 4 MiB in total;
  beyond that the call fails with `storage_required`, never a partial result.
  `html` and `txt` outputs have no preview by contract: `pages` is 0 and
  `previews` is empty (the file is itself the viewable content); the engines
  list in `artifact_capabilities_v1` says `previews: none` for them.
- `provenance`: template key, version and digest, SHA-256 of the data, of the
  context and of the resolved source (`source_sha256`: every file of the resolved
  Typst project, sorted by normalized path, as path length, path, content length
  and content, including `values.json`, `brand.typ`, other template files and
  fragments, plus all provided assets), engine and renderer versions, mode,
  output, fixed timestamp, page count.
- `artifact`: the v0.1 shape (`manifest`, `content_base64`) for existing callers.
- `outputs`: set when the Buffer is used, for the host platform to materialize: the main
  file and every page preview, each with `ref` (`buffer://`, distinct), `mime_type`,
  `bytes`, `sha256` and `metadata.filename`.

The same request gives the same SHA-256: the creation timestamp is fixed, the
project root is confined, system fonts are ignored and only Typst's embedded
fonts plus `ARTIFACT_FONT_PATH` are used.

The render timeout is one deadline for the whole call, Buffer uploads included.
When it passes during an upload the call fails with `render_timeout`; files
already uploaded stay in the Buffer until their TTL (the Buffer has no delete
call), and no reference is returned for them.

`printf` is bounded before it runs: the format is walked without executing it
and an upper bound is computed (literal format, plus per verb the largest of the
width, the precision and the argument size, following `*` and `[n]`; strings
count their length, `%q` six times and `%x` twice, maps and slices their JSON
size plus the width or precision times the number of leaves and containers, other values 512 bytes, unused arguments included). A bound above the
20 MiB output budget is refused with `limits_exceeded` before `fmt` allocates.
A `printf` width or precision above 1000, literal or taken from a `*`, is refused
with `limits_exceeded` before any computation (a document layout never needs
more). `%q`, `%x`, `%X`, `%U` and the flags `#` and `+` multiply the size of any
argument, collections included, by 10 (worst case of per byte escapes), and map
keys count as leaves like values. The bound is deliberately cautious and may
refuse a legitimate but improbable call.

A template folder and its assets together may hold at most 256 files
(`limits_exceeded`, checked before anything else; `max_files` in
`artifact_capabilities_v1`). Validation runs under the render deadline too and
fails with `render_timeout` when it passes. A template may hold at most 256
`define` blocks (`limits_exceeded`, `max_defines` in capabilities); every `define`
body and every `range` body checks the deadline, so sub-templates that write
nothing still stop at the timeout.

The `input_schema` dialect is JSON Schema draft 2020-12: `$schema` may be absent or
be 2020-12, any other value (draft-07 included) is refused with
`input_schema_invalid`. The `input_schema` is bounded before it is compiled (the JSON Schema validator
cannot be interrupted): its cost is independent of keywords: 1 plus the cost of every object or
array child at any depth (unknown keywords included), and a local `$ref` or
`$dynamicRef` (a JSON pointer) adds the cost of its target each time it is used, so chains of `$defs` referencing each other twice explode. Above
10 000 (`max_schema_cost` in capabilities) the call fails with `limits_exceeded`;
a cyclic `$ref` is refused with `input_schema_invalid`. The validation work is also bounded: the schema cost times the
data work (nodes plus the byte length of every string and object key) must not exceed 20 000 000
(`max_validation_work` in capabilities), else `limits_exceeded` before the data
is validated. `print`, `println`, `html`, `js` and `urlquery` are bounded before
they run like `printf` (arguments counted by size, collections by their JSON
length).

Error priority for an `input_schema` is fixed and deterministic (keys are
visited in sorted order, validator messages are sorted): dialect, closed
keywords, then references (non local, unresolvable or cyclic: always
`input_schema_invalid`, before any cost check), then reserved namespaces, then
the cost bound (`limits_exceeded`), then compilation.

The values of `const`, `enum`, `default`, `examples`, `title`, `description` and
`$comment` are literals: the schema walks do not look into them, so a
`{"$ref": ...}` object there is data, not a reference.

A `define` or `block` cannot be named `t`, start with `__entry_` (the entry template is named `__entry_<random nonce>` per render) or be named like an internal
function (`__present`, `__tick`, `escTypst`, `escHTML`, `escMarkdown`, `escMJML`,
`escText`, `escJSON`, `raw`, `now`, `call`): it is refused with
`forbidden_function`.

The data and the context may be nested at most 256 levels deep (`limits_exceeded`
at validation), so every walk over them has a bounded recursion. `json` is
bounded before it allocates (compact size, escaping, and indentation per node)
like `print`.

`printf` refuses `%p` (a memory address) and any pointer, function, channel or
unsafe value argument, as `print`, `println`, `html`, `js` and `urlquery` do
(`forbidden_function`). `%v` of a map is deterministic (keys sorted) and `%T` only
reveals the JSON types.

The literal `nil` is refused (`forbidden_function`) except as an argument of
`default`.

All fingerprints (template, source) frame every path and content with an 8-byte
big-endian length, so a file boundary cannot be imitated.

`$` cannot be declared or reassigned (`{{ $ = ... }}`, `{{ $ := ... }}`:
`forbidden_function`); other variables (`$x := ...`, `$x = ...`) are allowed and
checked where they are used. `$` alone in the entry template is the only exempt
value of the absent value check (it is the root, never nil).

Typst outputs (PDF, PNG pages) and its scratch directories are written to a
per-call output directory outside the project root, which is mounted read only
in Docker and identical for both passes (PNG previews, then PDF): a template
cannot read an earlier output, and previews and PDF come from the same project.

The Typst context rules (code context, backslash before an action, `(`, `[` or
`.name` right after an action, glued `#`) run on the text as it will be after
`{{-` and `-}}` trim whitespace, ignoring the actions that print nothing. A
backslash before an action is `typst_escaped_action`.

### Absent values in conditions

`{{ if .data.flag }}`, `{{ with .data.x }}` and `{{ range .data.items }}` accept
an absent value (it is false or empty). Arguments of functions inside a
condition are checked like everywhere else (`not`, `eq`, `and`, ... included): `{{ if eq .data.flag true }}` and `{{ if not .data.flag }}` fail
with `template_exec` when `flag` is absent, because `eq` would receive nil. Use
`{{ if .data.flag }}` (or guard with `default`) instead.

### Errors

Failures return `isError` with a stable `error` code: `invalid_arguments`,
`manifest_schema`, `file_path_invalid`, `entry_missing`, `input_schema_missing`,
`input_schema_invalid`, `reserved_namespace`, `data_invalid`, `context_invalid`,
`forbidden_function`, `unknown_function`, `template_syntax`, `template_exec`,
`typst_code_context`, `typst_raw_context`, `typst_math_context`, `typst_comment_context`, `package_import_forbidden`, `fragment_undeclared`,
`fragment_missing`, `engine_unsupported`, `output_unsupported`, `limits_exceeded`,
`engine_unavailable`, `engine_version_mismatch`, `compile_failed`,
`render_timeout`, `asset_invalid`, `reserved_file_name`, `file_extension_forbidden`, `storage_required`, `buffer_failed`.

### Limits and configuration

| Variable | Default | Role |
| --- | ---: | --- |
| `TYPST_BIN` | `typst` in `PATH` | Typst 0.15.1 binary (the image sets it) |
| `ARTIFACT_FONT_PATH` | empty | extra free fonts directory (the image sets Liberation) |
| `ARTIFACT_RENDER_TIMEOUT_SECONDS` | `60` | total time of one render |
| `ARTIFACT_RENDER_MAX_MEMORY_MB` | none | address space limit per compilation (Linux) |
| `ARTIFACT_MAX_PAGES` | `50` | pages (the manifest `limits.max_pages` can lower it) |
| `ARTIFACT_MAX_TEMPLATE_BYTES` | `1048576` | size of manifest and files; the manifest `limits.max_source_bytes` bounds the entry |
| `ARTIFACT_BUFFER_SERVICE_URL`, `ARTIFACT_BUFFER_TOKEN` | none | Buffer service endpoint and bearer token |
| `ARTIFACT_BUFFER_NAMESPACE`, `ARTIFACT_BUFFER_TTL_SECONDS` | `artifact`, `3600` | Buffer namespace and TTL |

The request is capped at 4 MiB. Without the Buffer, the rendered file is
returned inline up to 2 MiB (`storage_required` above).

### Storage

The server does not write to Storage itself. It follows the mechanism of the
Document and Browser MCPs: with the Buffer configured, files are uploaded to
the Buffer and returned as `buffer://` references in `file.ref` and `outputs`,
and the host platform materializes `outputs` in Storage. Without it, files are inline
base64, bounded.

## Vendored contract

`internal/template/contract/` holds the `template/v1` schemas and the shared
Typst fragments, with a `PIN` of their origin. `testdata/contract/` holds the
golden fixtures. Update them by copying again and bumping the PIN.

## Tests

`go test ./...` renders the contract goldens with a real Typst 0.15.1 found in
`TYPST_BIN`, then in `PATH`, then through Docker with the public image
`ghcr.io/typst/typst:0.15.1`. When none is usable the render tests are skipped.
The container image embeds the official Typst release (SHA-256 verified) and
ships its Apache-2.0 license under `/usr/share/doc/typst`.

## Release

Version 0.2.0 is published by the tag `v0.2.0` (AGENT-1786): the template
rendering engine of AGENT-1574 is a minor version, not a patch.

## License

Apache-2.0.

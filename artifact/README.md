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
formats, Kroki and pdfcpu are out of scope for 0.2.

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
| `print` | optional print layout for the `pdf` output (bleed, mirrored margins, crop marks, PDF page boxes), see below |

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

## QR codes: `qrModules` and `fragments/qr.typ`

Templates can print a deterministic QR code (contract `qr/v1`) with no package, plugin
or image. The engine encodes in Go, the fragment draws in pure Typst:

```
#import "/fragments/qr.typ": qr-code
#qr-code([{{ qrModules .data.payment_url "M" }}], size: 25mm)
```

- `qrModules text [level]` is a template function. It encodes the UTF-8 bytes of `text`
  (byte mode) in the smallest QR version that holds them, with the error correction
  level `L`, `M`, `Q` or `H` (upper case, default `M`) and the mask of lowest penalty (the
  first one on ties). If the text holds a byte outside ASCII, an ECI segment (designator 26, UTF-8; 12 bits) precedes the byte segment, so a reader that defaults to ISO-8859-1 still reads the UTF-8; pure ASCII has no ECI. The same text and level always give the same matrix. It returns the
  module matrix as plain text: one row per line of `1` (dark) and `0` (light) characters,
  rows separated by `/`, no trailing separator. That text goes through the normal Typst
  escaping of the engine (passed by reference, never raw): put it inside a content block
  `[...]`, not inside a string (`typst_code_context`).
- `qr-code(matrix, size: 30mm)` (`fragments/qr.typ`, declared in `fragments`) validates the
  matrix (square, 21 + 4k modules up to 117, characters `0` and `1` only; otherwise the
  compile error message starts with `qr/v1:`), adds a 4-module quiet zone and draws the dark
  modules in the darkest colour of `brand.colors` on white. `size` is the side of the whole
  square, quiet zone included.
- Bounds: 1 to 512 bytes of UTF-8 text (an accented letter counts 2 or 3 bytes), QR version
  25 at most (117 x 117 modules, what 512 bytes need at level H, with or without the ECI: its 12 bits fit in the margin of version 25, so the 512-byte bound is unchanged). Render error codes:
  `qr_input_invalid` (empty text, or a text argument that is not a string), `qr_input_too_long` (more than 512 bytes),
  `qr_level_invalid` (a level string other than `L`, `M`, `Q`, `H`, or more than one level),
  all listed in `artifact_capabilities_v1`. A level argument that is not a string (`{{ qrModules "a" 12 }}`) and an absent or `null`
  value fail with `template_exec` (the `text/template` call error or the absent-value guard),
  before `qrModules` runs.

The encoder is the standard library only (`internal/template/qr.go`, no QR dependency). Its
matrices are decoded back by an independent ZXing port (`github.com/makiuchi-d/gozxing`, MIT,
test dependency only) for every length from 1 to 512 bytes at the four levels.

## Professional print: the `print` option and `fragments/print.typ`

`artifact_render_v1` takes an optional `print` argument for the `pdf` output of a
template that declares `fragments/print.typ` (contract `template/v1`, AGENT-1921). It is a render option, not business
data. A closed JSON Schema is published by `artifact_capabilities_v1` (`print.schema`):

| Field | Type | Default | Bounds |
| --- | --- | --- | --- |
| `bleed_mm` | number | `3` | 0 to 5 mm, both included |
| `mirror_margins` | boolean | `false` | inside and outside margins alternate with the page parity (recto verso) |
| `crop_marks` | boolean | `false` | crop marks in a 7 mm mark zone around the bleed |

`print: {}` means a 3 mm bleed and nothing else; `print: null` is the same as no `print` (the published schema is `["object", "null"]`). Unknown fields, wrong types and an
out-of-range `bleed_mm` fail with `print_invalid`; the option on another output than `pdf`, on a
template that does not declare the fragment, or on a non-Typst engine fails with `print_unsupported`.

How it works. The platform writes `print-options` into `/brand.typ` (only when the option is
given: without it every byte, the PDF, `/brand.typ` and the provenance, is the same as before the
option). The fragment `print-layout` lays the pages out: the page is the finished size grown by
`bleed + mark zone` on each side, the template margins are added inside, `background` is drawn on the finished size plus the bleed,
and the crop marks are drawn outside the finished area, outside the bleed. Typst 0.15.1 cannot write the PDF
page boxes, so after the compilation the engine writes `/BleedBox` (the finished size plus the bleed) and
`/TrimBox` (the finished size) into every page dictionary, as an incremental update of the file Typst produced
(standard library only: the original bytes are kept, the page objects are appended, then a new xref section and
trailer with `/Prev`). The `/MediaBox` is the whole page, mark zone included. The result is reproducible: two
renders are identical to the byte, and the provenance echoes `print`. When `print` is given the engine compiles the project a second time, PDF only and without `print`, and
compares the MediaBox of every page: each page with `print` must be the page without it grown by 2 x (bleed + mark zone) in
width and height (0.01 pt), with the same page count. Otherwise the template declares `fragments/print.typ` without applying
`print-layout` and the render fails with `print_unsupported` ("the template declares fragments/print.typ but does not apply
print-layout"), so no PDF carries boxes that do not match its layout. The `TrimBox` is computed from the page without `print`.
Cost: one more Typst PDF compilation per print render (about 2 s on the goldens, no previews). A static check comes first: the Typst entry must apply the fragment with a `#show: print-layout` (or `#show: print-layout.with(...)`) rule, outside comments, strings and raw text, else the same `print_unsupported`. With `bleed_mm` 0 and no crop marks nothing grows, and a result identical to the render without `print` is correct (`TrimBox` = `BleedBox` = `MediaBox`). The layout is guaranteed by the contract fragment: the engine checks that it is applied and computes the boxes from the render without `print`; a template that imitates the geometry on purpose without applying the fragment is outside the threat model. A PDF the update cannot read fails
with `print_box_failed`. The PNG previews show the same layout. The Typst fragment is vendored from
pilote-maurice `contracts/template/v1` (see `internal/template/contract/PIN`).

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

Engines: **Typst 0.15.1** (`pdf`, `png`; the version is enforced), **MJML 5.4.1**
(`html`, `txt`, `eml`; message profile, see below), `html`
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

### Message profile: the `mjml` engine (e-mail)

A template with `source.engine` `mjml` (kind and profile `message`, see the
contract) renders to `html`, `txt` or `eml`. Nothing is ever sent: the outputs
are files, with no recipient, date or message identifier.

1. The entry and the declared fragments (`fragments/message/*.mjml` of the
   vendored library) are executed with the Go template syntax, values escaped
   like `html/template` (a value is text, never markup: `<b>` and `{{ 1+1 }}`
   in data are shown as typed).
2. The resolved files are written to a temporary working directory and compiled
   by MJML 5.4.1 (`node render.mjs`): strict validation, `mj-include` enabled
   and bounded to that directory, no minification. An `mj-include` that does not
   target a declared `fragments/message/*.mjml` is refused before MJML runs
   (`mjml_include_outside_fragments`; `fragment_undeclared` when it is a library
   fragment the manifest does not declare). The sub-process gets an empty
   environment with dead proxies, a time limit, a heap limit and a bounded output,
   and Node's permission model limits it to reading the engine folder and the
   working directory (no child process). MJML makes no network call; the process
   is not isolated from the network at kernel level.
3. Before MJML, the entry and the fragments are executed a second time against
   the very same data (same branches), with every value marked between two
   private-use characters (U+E000, U+E001, raw or percent-encoded in a URL). The
   marked zones are removed and any template brace left (`{{`, `}}`) was written
   by the template itself, not by a value: `message_template_residue`. Data that
   hold U+E000 or U+E001 are refused (`message_reserved_character`). An emission
   through `html` with arguments (`{{ html .data.x }}`) is not marked: braces in
   such a value count as the template's.
4. The HTML is checked, whatever the output asked: a document is produced
   (`message_output_invalid`); no `mj-include denied` comment
   (`mjml_include_denied`); under 102 KiB (`message_html_too_heavy`);
   every `href`, `src` and `background` (attribute, conditional comment) and every
   URL of the CSS (`url()`, `image-set()`, `@import`; comments removed and escapes
   such as `u\72l(` decoded first, in `<style>` blocks, `style` attributes and
   conditional comments) is an absolute `https://` URL, `srcset` is refused
   (`message_url_not_https`); and when the manifest says
   `message.category` `marketing`, the HTML holds an `<a href>` equal to
   `data.unsubscribe_url` (scheme and host case-insensitive, default port ignored,
   the rest compared byte for byte, so `a%2Fb` is not `a/b`;
   `message_unsubscribe_missing`). MJML refusing the document returns
   `mjml_failed` with its validation errors; no engine returns `engine_unavailable`.
5. `txt` is derived from the final HTML: `head`, `style`, comments and hidden
   elements are dropped. Hidden means the `hidden` attribute, or a declaration
   `display:none`, `visibility:hidden`, `mso-hide:all`, `opacity:0`, or
   `max-height`, `font-size` or `line-height` at 0 with `overflow:hidden` (the
   preheader), in the inline style or in a `<style>` rule outside `@media` whose
   selector is simple (`.class`, `element.class`, `element`, `#id`, lists of them).
   A hiding rule with another selector (descendant, attribute, pseudo-class,
   nesting) makes `txt` and `eml` fail with `message_hidden_rule_unsupported`
   rather than risk leaking hidden text (`html` still renders). A link is written
   `label (url)`, an image `[alt]`, lists are numbered, entities are decoded,
   invisible padding is removed.
6. `eml` is a `multipart/alternative` message: the text part, then the HTML part,
   both UTF-8 and quoted-printable (readable and diffable, a few percent larger
   where base64 adds a third), CRLF line ends, and a boundary derived from the
   content. `Subject` is written (RFC 2047 when needed) only if `data.subject` is
   a string. Same inputs, same bytes.

The image ships the Node binary of a pinned official image and MJML installed
from the committed lockfile (`docker/mjml`, `npm ci --omit=dev --ignore-scripts`),
with the Node and MJML licenses under `/usr/share/doc/node` and `/usr/share/doc/mjml`.

### Errors

Failures return `isError` with a stable `error` code: `invalid_arguments`,
`manifest_schema`, `file_path_invalid`, `entry_missing`, `input_schema_missing`,
`input_schema_invalid`, `reserved_namespace`, `data_invalid`, `context_invalid`,
`forbidden_function`, `unknown_function`, `template_syntax`, `template_exec`,
`typst_code_context`, `typst_raw_context`, `typst_math_context`, `typst_comment_context`, `qr_input_invalid`, `qr_input_too_long`, `qr_level_invalid`, `print_invalid`, `print_unsupported`, `print_box_failed`, `package_import_forbidden`, `fragment_undeclared`,
`fragment_missing`, `engine_unsupported`, `output_unsupported`, `limits_exceeded`,
`engine_unavailable`, `engine_version_mismatch`, `compile_failed`, `mjml_failed`,
`mjml_include_outside_fragments`, `mjml_include_denied`, `message_output_invalid`,
`message_template_residue`, `message_reserved_character`, `message_hidden_rule_unsupported`, `message_html_too_heavy`, `message_url_not_https`,
`message_unsubscribe_missing`, `render_timeout`, `asset_invalid`, `reserved_file_name`, `file_extension_forbidden`, `storage_required`, `buffer_failed`.

### Limits and configuration

| Variable | Default | Role |
| --- | ---: | --- |
| `TYPST_BIN` | `typst` in `PATH` | Typst 0.15.1 binary (the image sets it) |
| `MJML_NODE_BIN` | `node` in `PATH` | Node binary that runs MJML (the image sets it) |
| `MJML_DIR` | none | folder holding `render.mjs` and `node_modules` with MJML 5.4.1 (the image sets it) |
| `ARTIFACT_FONT_PATH` | empty | extra free fonts directory (the image sets Liberation) |
| `ARTIFACT_RENDER_TIMEOUT_SECONDS` | `60` | total time of one render |
| `ARTIFACT_RENDER_MAX_MEMORY_MB` | none (MJML: 512) | address space limit per Typst compilation (Linux); V8 heap limit of the MJML process |
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

The message tests need MJML 5.4.1: `MJML_NODE_BIN` with `MJML_DIR`, else a Node
in `PATH`, with MJML installed by `npm ci` from `docker/mjml` into a temporary folder
(network needed once). Without an engine they are skipped locally and fail when
`CI` or `ARTIFACT_REQUIRE_MJML=1` is set.

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

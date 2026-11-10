package template

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/version"
)

// TypstVersion is the only Typst version this renderer accepts (bench AGENT-1573).
const TypstVersion = "0.15.1"

// Modes of a render.
const (
	ModePreview = "preview"
	ModeFinal   = "final"
)

// Default limits; every one can be lowered by the caller's Options.
const (
	DefaultMaxTemplateBytes = 1 << 20
	DefaultMaxPages         = 50
	DefaultMaxOutputBytes   = 20 << 20
	DefaultMaxFiles         = 256
	DefaultMaxDefines       = 256
	DefaultMaxValuesBytes   = 8 << 20
	previewPPI              = 72
	finalPPI                = 144
)

// CompileRequest asks the Typst engine for one output of the resolved project.
type CompileRequest struct {
	Dir       string // project root (contains the resolved .typ, brand.typ, values.json); read only for Typst
	OutDir    string // outputs and scratch directories (packages, cache, home), outside the root
	Entry     string // entry file, relative to Dir
	Out       string // output pattern, relative to OutDir ("{0p}" for pages in png)
	Format    string // pdf or png
	PPI       int    // png only
	Timestamp int64  // fixed creation timestamp (Unix seconds), for reproducible output
}

// Compiler is the Typst engine.
type Compiler interface {
	Version(ctx context.Context) (string, error)
	Compile(ctx context.Context, req CompileRequest) error
}

// Context is the platform part of the execution context (reserved namespaces).
type Context struct {
	Company map[string]any `json:"company"`
	Brand   map[string]any `json:"brand"`
	Doc     map[string]any `json:"doc"`
	I18n    map[string]any `json:"i18n"`
}

// Request is one render.
type Request struct {
	Template  Template
	Data      any
	Context   Context
	Assets    map[string][]byte // local files (logo, images) written into the Typst project, by normalized path
	Output    string            // pdf, png, html, txt
	Mode      string            // preview or final
	Timestamp *int64            // optional fixed timestamp; default derived from doc.date
}

// Options bounds a render.
type Options struct {
	MaxTemplateBytes int
	MaxPages         int
	MaxOutputBytes   int // size of each executed template output (default 20 MiB)
	MaxValuesBytes   int // total size of the Typst values table, values.json (default 8 MiB)
	Timeout          time.Duration
	MJML             MJMLCompiler // nil when no MJML engine is configured (mjml engine only)
}

// Provenance records where a rendered file comes from. Same request, same
// provenance and same output bytes.
type Provenance struct {
	Schema   string `json:"schema"`
	Template struct {
		Key     string `json:"key"`
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	} `json:"template"`
	DataSHA256    string `json:"data_sha256"`
	ContextSHA256 string `json:"context_sha256"`
	SourceSHA256  string `json:"source_sha256"`
	Engine        struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"engine"`
	Renderer struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"renderer"`
	Mode      string `json:"mode"`
	Output    string `json:"output"`
	Timestamp string `json:"timestamp"`
	Pages     int    `json:"pages,omitempty"`
}

// Page is one PNG preview.
type Page struct {
	Number int
	PNG    []byte
}

// Result is a rendered file with its previews.
type Result struct {
	Filename   string
	MimeType   string
	Content    []byte
	Pages      int
	Previews   []Page
	Provenance Provenance
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func canonicalSHA(v any) string {
	b, _ := json.Marshal(v) // maps are marshalled with sorted keys
	return sha(b)
}

// frame writes a block with an unambiguous framing: its length on 8 bytes
// (big-endian), then its bytes. Every fingerprint of the package uses it, so no
// content can imitate a separator.
func frame(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

// templateDigest fingerprints the template folder: the canonical manifest, the
// number of files, then for every file sorted by path the framed path and the
// framed content.
func templateDigest(manifest json.RawMessage, files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	var canon any
	_ = json.Unmarshal(manifest, &canon)
	cb, _ := json.Marshal(canon)
	frame(h, cb)
	frame(h, []byte(strconv.Itoa(len(names))))
	for _, n := range names {
		frame(h, []byte(n))
		frame(h, []byte(files[n]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fixedTimestamp returns the creation timestamp: explicit, else midnight UTC
// of doc.date, else 0. It never reads the clock.
func fixedTimestamp(explicit *int64, doc map[string]any) int64 {
	if explicit != nil {
		return *explicit
	}
	if d, _ := doc["date"].(string); d != "" {
		if t, err := time.Parse("2006-01-02", d); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// Render validates the template and the data, executes the template and
// compiles it. compiler may be nil for engines that do not need one.
func Render(ctx context.Context, req Request, compiler Compiler, opts Options) (*Result, error) {
	if opts.MaxTemplateBytes <= 0 {
		opts.MaxTemplateBytes = DefaultMaxTemplateBytes
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = DefaultMaxPages
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = DefaultMaxOutputBytes
	}
	// First of all, before any other validation: bound the number of files.
	if n := len(req.Template.Files) + len(req.Assets); n > DefaultMaxFiles {
		return nil, errf(CodeLimitsExceeded, "%d files (template files and assets), above the limit of %d", n, DefaultMaxFiles)
	}
	if req.Mode == "" {
		req.Mode = ModePreview
	}
	if req.Mode != ModePreview && req.Mode != ModeFinal {
		return nil, errf(CodeInvalidModeOrOutput, "mode must be %q or %q", ModePreview, ModeFinal)
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	full := map[string]any{
		"data": req.Data, "company": req.Context.Company, "brand": req.Context.Brand,
		"doc": req.Context.Doc, "i18n": req.Context.I18n,
	}
	for _, name := range []string{"company", "brand", "doc", "i18n"} {
		if v, ok := full[name].(map[string]any); !ok || v == nil {
			return nil, errf(CodeContextInvalid, "context.%s is required", name)
		}
	}
	if err := checkReservedDataKeys(req.Data); err != nil {
		return nil, err
	}
	if dataDepth(full, MaxDataDepth) > MaxDataDepth {
		return nil, errf(CodeLimitsExceeded, "the data or the context is nested deeper than %d levels", MaxDataDepth)
	}
	if err := ValidateContext(full); err != nil {
		return nil, err
	}
	p, err := ValidateCtx(ctx, req.Template, req.Data, req.Output, opts.MaxTemplateBytes)
	if err != nil {
		return nil, err
	}
	ex, err := newExecutor(ctx, p.Manifest.Source.Engine, full, opts.MaxOutputBytes)
	if err != nil {
		return nil, err
	}
	if opts.MaxValuesBytes > 0 {
		ex.maxValues = opts.MaxValuesBytes
	}
	entryName := filepath.Base(p.Entry)
	if req.Assets, err = normalizeAssets(req.Assets); err != nil {
		return nil, err
	}
	if err := checkAssets(req.Assets, p.Files); err != nil {
		return nil, err
	}
	if err := checkProjectPaths(p.Files, req.Assets, p.Fragments); err != nil {
		return nil, err
	}
	rendered, err := ex.render(p.Files[p.Entry])
	if err != nil {
		return nil, execErr("rendering "+p.Entry, err)
	}
	// Local files of the folder are written into the Typst project under their
	// relative path; the other .typ files are executed like the entry (same
	// values table), the rest is copied as is.
	// Sorted paths: the order of execution decides the index of every value in
	// values.json, so it must not depend on map iteration order.
	locals := map[string]string{}
	for _, name := range sortedKeys(p.Files) {
		body := p.Files[name]
		if name == p.Entry {
			continue
		}
		if p.Manifest.Source.Engine == EngineTypst && strings.EqualFold(filepath.Ext(name), ".typ") {
			text, err := ex.render(body)
			if err != nil {
				return nil, execErr("rendering "+name, err)
			}
			body = text
		}
		locals[name] = body
	}
	fragments := map[string]string{}
	for _, name := range sortedKeys(p.Fragments) {
		text := p.Fragments[name]
		if strings.Contains(text, "{{") {
			if text, err = ex.render(text); err != nil {
				return nil, execErr("rendering fragment "+name, err)
			}
		}
		fragments[name] = text
	}

	ts := fixedTimestamp(req.Timestamp, req.Context.Doc)
	res := &Result{}
	prov := &res.Provenance
	prov.Schema = "agentmaurice.artifact-provenance/v1"
	prov.Template.Key = p.Manifest.Key
	prov.Template.Version = p.Manifest.Version
	prov.Template.SHA256 = templateDigest(req.Template.Manifest, p.Files)
	prov.DataSHA256 = canonicalSHA(req.Data)
	prov.ContextSHA256 = canonicalSHA(map[string]any{"company": full["company"], "brand": full["brand"], "doc": full["doc"], "i18n": full["i18n"]})
	prov.Renderer.Name = "agentmaurice-artifact"
	prov.Renderer.Version = version.Version
	prov.Mode, prov.Output = req.Mode, req.Output
	prov.Timestamp = time.Unix(ts, 0).UTC().Format(time.RFC3339)

	switch p.Manifest.Source.Engine {
	case EngineHTML, EngineText:
		res.Content = []byte(rendered)
		prov.SourceSHA256 = sha(res.Content)
		prov.Engine.Name = p.Manifest.Source.Engine
		prov.Engine.Version = "go-template"
		res.Filename = strings.TrimSuffix(entryName, filepath.Ext(entryName)) + "." + req.Output
		if req.Output == "html" {
			res.MimeType = "text/html; charset=utf-8"
		} else {
			res.MimeType = "text/plain; charset=utf-8"
		}
		return res, nil
	}
	if p.Manifest.Source.Engine == EngineMJML {
		return renderMessage(ctx, req, p, entryName, rendered, fragments, full, opts, res)
	}
	return renderTypst(ctx, req, p, ex, entryName, rendered, locals, fragments, ts, compiler, opts, res)
}

func renderTypst(ctx context.Context, req Request, p *Prepared, ex *executor, entryName, rendered string, locals,
	fragments map[string]string, ts int64, compiler Compiler, opts Options, res *Result) (*Result, error) {
	if compiler == nil {
		return nil, errf(CodeEngineUnavailable, "no Typst engine is configured")
	}
	logo := logoFile(req.Context.Brand, req.Assets)
	brandTyp, err := brandTypst(req.Context.Brand, req.Context.Doc, logo)
	if err != nil {
		return nil, errf(CodeContextInvalid, "brand.typ: %v", err)
	}
	dir, err := os.MkdirTemp("", "artifact-render-")
	if err != nil {
		return nil, errf(CodeCompileFailed, "temporary directory: %v", err)
	}
	defer os.RemoveAll(dir)
	// Typst outputs never go under the project root: the root stays identical
	// between the passes, so a template cannot read what an earlier pass produced.
	outDir, err := os.MkdirTemp("", "artifact-out-")
	if err != nil {
		return nil, errf(CodeCompileFailed, "temporary directory: %v", err)
	}
	defer os.RemoveAll(outDir)
	write := func(rel string, body []byte) error {
		target := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	}
	files := map[string][]byte{
		p.Entry:       []byte(rendered),
		"values.json": ex.valuesJSON(),
		"brand.typ":   []byte(brandTyp),
	}
	for name, body := range locals {
		files[name] = []byte(body)
	}
	for name, body := range fragments {
		files[name] = []byte(body)
	}
	// every provided asset is written under its normalized path (the logo is one of them)
	for name, body := range req.Assets {
		files[name] = body
	}
	for name, body := range files {
		if err := write(name, body); err != nil {
			return nil, errf(CodeCompileFailed, "writing %s: %v", name, err)
		}
	}
	res.Provenance.SourceSHA256 = sourceDigest(files, req.Assets)

	got, err := compiler.Version(ctx)
	if err != nil {
		return nil, asEngineError(ctx, err)
	}
	if got != TypstVersion {
		return nil, errf(CodeEngineVersion, "Typst %s is required, found %s", TypstVersion, got)
	}
	res.Provenance.Engine.Name, res.Provenance.Engine.Version = "typst", got

	ppi := previewPPI
	if req.Mode == ModeFinal {
		ppi = finalPPI
	}
	// PNG first: it gives the page count before the PDF is produced.
	if err := compiler.Compile(ctx, CompileRequest{Dir: dir, OutDir: outDir, Entry: p.Entry, Out: "page-{0p}.png", Format: "png", PPI: ppi, Timestamp: ts}); err != nil {
		return nil, asEngineError(ctx, err)
	}
	pageFiles, _ := filepath.Glob(filepath.Join(outDir, "page-*.png"))
	sort.Strings(pageFiles)
	limit := opts.MaxPages
	if m := p.Manifest.Limits.MaxPages; m > 0 && m < limit {
		limit = m
	}
	if len(pageFiles) > limit {
		return nil, errf(CodeLimitsExceeded, "document has %d pages, above the limit of %d", len(pageFiles), limit)
	}
	for i, f := range pageFiles {
		if err := checkProducedSize(f, opts.MaxOutputBytes); err != nil {
			return nil, err
		}
		body, err := os.ReadFile(f)
		if err != nil {
			return nil, errf(CodeCompileFailed, "reading preview: %v", err)
		}
		res.Previews = append(res.Previews, Page{Number: i + 1, PNG: body})
	}
	res.Pages = len(pageFiles)
	res.Provenance.Pages = res.Pages
	base := strings.TrimSuffix(entryName, filepath.Ext(entryName))
	switch req.Output {
	case "pdf":
		if err := compiler.Compile(ctx, CompileRequest{Dir: dir, OutDir: outDir, Entry: p.Entry, Out: "out.pdf", Format: "pdf", Timestamp: ts}); err != nil {
			return nil, asEngineError(ctx, err)
		}
		if err := checkProducedSize(filepath.Join(outDir, "out.pdf"), opts.MaxOutputBytes); err != nil {
			return nil, err
		}
		res.Content, err = os.ReadFile(filepath.Join(outDir, "out.pdf"))
		if err != nil {
			return nil, errf(CodeCompileFailed, "reading PDF: %v", err)
		}
		res.Filename, res.MimeType = base+".pdf", "application/pdf"
	case "png":
		if len(res.Previews) == 0 {
			return nil, errf(CodeCompileFailed, "the document has no page")
		}
		res.Content = res.Previews[0].PNG
		res.Filename, res.MimeType = base+".png", "image/png"
	}
	return res, nil
}

// asEngineError maps engine failures to stable codes.
func asEngineError(ctx context.Context, err error) error {
	if e, ok := err.(*Error); ok {
		return e
	}
	if ctx.Err() == context.DeadlineExceeded {
		return errf(CodeRenderTimeout, "compilation exceeded the time limit")
	}
	return errf(CodeCompileFailed, "%v", err)
}

// execErr keeps stable codes of the execution (timeout, size) and wraps the rest as template_exec.
func execErr(what string, err error) error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return errf(CodeTemplateExec, "%s: %v", what, err)
}

// maxAssetBytes bounds the provided assets together.
const maxAssetBytes = 4 << 20

// normalizeAssets cleans asset paths (path.Clean) and refuses unsafe paths,
// duplicates after normalization, other extensions than svg, png and jpg, and
// oversized sets.
func normalizeAssets(assets map[string][]byte) (map[string][]byte, error) {
	out := make(map[string][]byte, len(assets))
	total := 0
	for _, name := range sortedKeys(assets) {
		for _, d := range projectDirs {
			if name == d || strings.HasPrefix(name, d+"/") {
				return nil, errf(CodeFilePathInvalid, "path %q starts with the runner directory %q", name, d)
			}
		}
		ext := strings.ToLower(path.Ext(name))
		if !relativePath.MatchString(name) || strings.Contains(name, "..") || (ext != ".svg" && ext != ".png" && ext != ".jpg") {
			return nil, errf(CodeAssetInvalid, "asset %q: use a safe relative path ending in svg, png or jpg", name)
		}
		clean := path.Clean(name)
		if _, dup := out[clean]; dup {
			return nil, errf(CodeAssetInvalid, "asset %q is a duplicate after normalization (%q)", name, clean)
		}
		total += len(assets[name])
		out[clean] = assets[name]
	}
	if total > maxAssetBytes {
		return nil, errf(CodeLimitsExceeded, "assets are %d bytes, above the %d byte limit", total, maxAssetBytes)
	}
	return out, nil
}

// checkAssets refuses assets that would shadow a generated or template file.
func checkAssets(assets map[string][]byte, files map[string]string) error {
	for _, name := range sortedKeys(assets) {
		if reservedName(name) {
			return errf(CodeReservedFileName, "asset %q uses a name reserved to the platform", name)
		}
		if _, ok := files[name]; ok {
			return errf(CodeReservedFileName, "asset %q collides with a file of the template", name)
		}
	}
	return nil
}

// checkProducedSize applies the output size limit to a compiled file (PDF or
// PNG page) before it is read, so it can never reach the Buffer or the result.
func checkProducedSize(file string, max int) error {
	st, err := os.Stat(file)
	if err != nil {
		return errf(CodeCompileFailed, "compiled file missing: %v", err)
	}
	if st.Size() > int64(max) {
		return errf(CodeLimitsExceeded, "compiled file %s is %d bytes, above the %d byte output limit", filepath.Base(file), st.Size(), max)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sourceDigest fingerprints the whole resolved Typst project: every file written
// for the compilation (entry, other template files, fragments, values.json,
// brand.typ, logo) and every provided asset, sorted by normalized path, each as
// path length, path, content length and content.
func sourceDigest(files map[string][]byte, assets map[string][]byte) string {
	h := sha256.New()
	frame(h, []byte(strconv.Itoa(len(files))))
	for _, name := range sortedKeys(files) {
		frame(h, []byte("file"))
		frame(h, []byte(name))
		frame(h, files[name])
	}
	frame(h, []byte(strconv.Itoa(len(assets))))
	for _, name := range sortedKeys(assets) {
		frame(h, []byte("asset"))
		frame(h, []byte(name))
		frame(h, assets[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// projectDirs are directories the runner creates at the root of the Typst project.
var projectDirs = []string{".typst-pkg", ".typst-cache", ".typst-home"}

// checkProjectPaths checks every path written into the Typst project together:
// template files, assets, fragments and the names generated by the platform
// (brand.typ, values.json, out.pdf, page-*.png and the runner directories). No
// normalized path may equal a directory prefix of another (p and p/...), and no
// path may start with a generated name or a runner directory. All violations are
// collected, sorted and the first is reported, so the message is stable.
func checkProjectPaths(files map[string]string, assets map[string][]byte, fragments map[string]string) error {
	all := map[string]string{} // path -> origin
	for name := range files {
		all[name] = "template file"
	}
	for name := range assets {
		all[name] = "asset"
	}
	for name := range fragments {
		all[name] = "fragment"
	}
	for name := range generatedNames {
		all[name] = "generated file"
	}
	var problems []string
	for _, p := range sortedKeys(all) {
		parts := strings.Split(p, "/")
		first := parts[0]
		if len(parts) > 1 {
			for _, d := range projectDirs {
				if first == d {
					problems = append(problems, "path "+quote(p)+" starts with the runner directory "+quote(d))
				}
			}
			if generatedNames[first] || reservedName(first) && !strings.HasPrefix(first, "fragments") {
				problems = append(problems, "path "+quote(p)+" starts with the reserved name "+quote(first))
			}
		}
		for i := 1; i < len(parts); i++ {
			prefix := strings.Join(parts[:i], "/")
			if _, ok := all[prefix]; ok {
				problems = append(problems, "path "+quote(prefix)+" ("+all[prefix]+") is both a file and a directory: conflicts with "+quote(p)+" ("+all[p]+")")
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errf(CodeFilePathInvalid, "%s", problems[0])
	}
	return nil
}

func quote(s string) string { return "\"" + s + "\"" }

// checkReservedDataKeys refuses, at render time, data whose top-level keys are
// platform namespaces (company, brand, doc, i18n). The input_schema check can
// be bypassed with allOf or additionalProperties; the data stay under .data and
// the context at the root, so this is a naming confusion, not a substitution,
// but the contract forbids it.
func checkReservedDataKeys(data any) error {
	m, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	for _, k := range sortedKeys(m) {
		if reserved(k) {
			return errf(CodeReservedNamespace, "data has the top-level key %q, a namespace reserved to the platform (company, brand, doc, i18n)", k)
		}
	}
	return nil
}

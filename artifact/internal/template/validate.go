package template

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed contract/*.json contract/fragments/*.typ contract/fragments/message/*.mjml
var contractFS embed.FS

// Template is a template/v1 folder provided inline: the manifest (an object)
// and the text files of the folder, keyed by relative path.
type Template struct {
	Manifest json.RawMessage   `json:"manifest"`
	Files    map[string]string `json:"files"`
}

// Manifest is the part of the template/v1 manifest used by the renderer.
type Manifest struct {
	Schema  string `json:"schema"`
	Key     string `json:"key"`
	Version string `json:"version"`
	Source  struct {
		Engine string `json:"engine"`
		Entry  string `json:"entry"`
	} `json:"source"`
	Outputs     []string `json:"outputs"`
	InputSchema string   `json:"input_schema"`
	Fragments   []string `json:"fragments"`
	// Message holds the optional settings of the message profile; an absent
	// category reads as transactional.
	Message struct {
		Category string `json:"category"`
	} `json:"message"`
	Limits struct {
		MaxSourceBytes int `json:"max_source_bytes"`
		MaxPages       int `json:"max_pages"`
	} `json:"limits"`
}

// Prepared is a validated template ready to execute.
type Prepared struct {
	Manifest    Manifest
	ManifestRaw any
	Entry       string
	InputSchema any
	Fragments   map[string]string
	Files       map[string]string
}

var relativePath = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

var (
	schemaOnce sync.Once
	schemaErr  error
	schemas    map[string]*jsonschema.Schema
)

const contractBase = "https://contracts.agentmaurice.app/template/v1/"

type denyLoader struct{}

func (denyLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("remote schema loading is disabled: %s", url)
}

func contractSchemas() (map[string]*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		c := jsonschema.NewCompiler()
		c.UseLoader(denyLoader{})
		names := []string{"manifest.schema.json", "namespaces.schema.json", "tokens.schema.json"}
		for _, name := range names {
			body, err := contractFS.ReadFile("contract/" + name)
			if err != nil {
				schemaErr = err
				return
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
			if err != nil {
				schemaErr = err
				return
			}
			if err := c.AddResource(contractBase+name, doc); err != nil {
				schemaErr = err
				return
			}
		}
		schemas = map[string]*jsonschema.Schema{}
		for _, name := range names {
			s, err := c.Compile(contractBase + name)
			if err != nil {
				schemaErr = err
				return
			}
			schemas[name] = s
		}
	})
	return schemas, schemaErr
}

func decodeJSON(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(target); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data after the JSON value")
	}
	return nil
}

func short(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1200 {
		return s[:1200] + "..."
	}
	return s
}

// ValidateContext checks the reserved namespaces company, brand, doc and i18n
// (and data being an object) against namespaces.schema.json.
func ValidateContext(ctx map[string]any) error {
	s, err := contractSchemas()
	if err != nil {
		return errf(CodeContextInvalid, "contract schemas unavailable: %v", err)
	}
	if err := s["namespaces.schema.json"].Validate(ctx); err != nil {
		return errf(CodeContextInvalid, "%s", describeSchemaError(err))
	}
	return nil
}

// Validate checks a template before execution: manifest against the contract
// schema, paths, engine and output, input_schema (closed keywords, reserved
// namespaces), data against input_schema, limits, fragments, forbidden and
// unknown functions, and the Typst context rules that are cheap to check.
// Static analysis of optional-property guards stays with `maurice template
// check` (AGENT-1783); an unguarded absent value still fails at execution.
func Validate(t Template, data any, output string, maxTemplateBytes int) (*Prepared, error) {
	return ValidateCtx(context.Background(), t, data, output, maxTemplateBytes)
}

// TestSkipPackageFilter disables the static package filter so a test can check
// that the runtime alone makes a package import fail. Never set in production.
var TestSkipPackageFilter bool

// TestValidateHook, when set by a test, runs once per executed file during
// validation (to make it deliberately slow). Never set in production.
var TestValidateHook func()

func deadlineErr(ctx context.Context) error {
	if ctx.Err() != nil {
		return errf(CodeRenderTimeout, "validation exceeded the time limit")
	}
	return nil
}

// ValidateCtx is Validate under a deadline: the loops over files check ctx and
// fail with render_timeout.
func ValidateCtx(ctx context.Context, t Template, data any, output string, maxTemplateBytes int) (*Prepared, error) {
	s, err := contractSchemas()
	if err != nil {
		return nil, errf(CodeManifestSchema, "contract schemas unavailable: %v", err)
	}
	if len(t.Manifest) == 0 {
		return nil, errf(CodeManifestSchema, "manifest is required")
	}
	var raw any
	if err := decodeJSON(t.Manifest, &raw); err != nil {
		return nil, errf(CodeManifestSchema, "manifest is not valid JSON: %v", err)
	}
	if err := s["manifest.schema.json"].Validate(raw); err != nil {
		return nil, errf(CodeManifestSchema, "%s", describeSchemaError(err))
	}
	var m Manifest
	if err := decodeJSON(t.Manifest, &m); err != nil {
		return nil, errf(CodeManifestSchema, "manifest: %v", err)
	}
	// Normalize every path with path.Clean and refuse duplicates after
	// normalization ("parts/body.typ" and "parts/./body.typ"): the project
	// written for Typst must not depend on map iteration order.
	files := make(map[string]string, len(t.Files))
	total := len(t.Manifest)
	for _, name := range sortedKeys(t.Files) {
		if err := deadlineErr(ctx); err != nil {
			return nil, err
		}
		body := t.Files[name]
		if !relativePath.MatchString(name) || strings.Contains(name, "..") {
			return nil, errf(CodeFilePathInvalid, "file path %q is not a safe relative path", name)
		}
		clean := path.Clean(name)
		if _, dup := files[clean]; dup {
			return nil, errf(CodeFilePathInvalid, "file path %q is a duplicate after normalization (%q)", name, clean)
		}
		files[clean] = body
		total += len(body)
	}
	t.Files = files
	m.Source.Entry = path.Clean(m.Source.Entry)
	m.InputSchema = path.Clean(m.InputSchema)
	p := &Prepared{Manifest: m, ManifestRaw: raw, Entry: m.Source.Entry, Files: files, Fragments: map[string]string{}}
	if err := checkFileNames(ctx, t.Files, m.Source.Entry); err != nil {
		return nil, err
	}
	if maxTemplateBytes > 0 && total > maxTemplateBytes {
		return nil, errf(CodeLimitsExceeded, "template folder is %d bytes, above the %d byte limit", total, maxTemplateBytes)
	}

	engine := m.Source.Engine
	if _, err := newExecutor(context.Background(), engine, nil, 0); err != nil {
		return nil, err
	}
	if !contains(m.Outputs, output) {
		return nil, errf(CodeOutputUnsupported, "output %q is not declared by the template (outputs: %s)", output, strings.Join(m.Outputs, ", "))
	}
	if !engineProduces(engine, output) {
		return nil, errf(CodeOutputUnsupported, "engine %q cannot produce %q", engine, output)
	}

	entry, ok := t.Files[m.Source.Entry]
	if !ok {
		return nil, errf(CodeEntryMissing, "entry %q is not in files", m.Source.Entry)
	}
	if limit := m.Limits.MaxSourceBytes; limit > 0 && len(entry) > limit {
		return nil, errf(CodeLimitsExceeded, "entry is %d bytes, above limits.max_source_bytes=%d", len(entry), limit)
	}
	schemaText, ok := t.Files[m.InputSchema]
	if !ok {
		return nil, errf(CodeInputSchemaMissing, "input_schema %q is not in files", m.InputSchema)
	}
	if err := validateInputSchema(schemaText, &p.InputSchema); err != nil {
		return nil, err
	}
	if dataDepth(data, MaxDataDepth) > MaxDataDepth {
		return nil, errf(CodeLimitsExceeded, "the data is nested deeper than %d levels", MaxDataDepth)
	}
	if root, ok := p.InputSchema.(map[string]any); ok {
		cost, _ := schemaCost(root)
		if cost < 1 {
			cost = 1
		}
		// work = schema cost x data work (nodes + bytes of strings and keys)
		if work := dataWork(data, MaxValidationWork/cost); work > MaxValidationWork/cost {
			return nil, errf(CodeLimitsExceeded, "validating the data would take too long: schema cost %d x data work above %d (limit %d)", cost, MaxValidationWork/cost, MaxValidationWork)
		}
	}
	if err := validateData(schemaText, data); err != nil {
		return nil, err
	}

	for _, fragment := range m.Fragments {
		body, err := contractFS.ReadFile("contract/" + fragment)
		// mjml: only the library fragments of the message profile; every other
		// engine keeps its former rule (no .mjml fragment).
		wrongKind := strings.HasSuffix(fragment, ".mjml")
		if engine == EngineMJML {
			wrongKind = !strings.HasPrefix(fragment, messageFragmentDir+"/") || !strings.HasSuffix(fragment, ".mjml")
		}
		if err != nil || !strings.HasPrefix(fragment, "fragments/") || wrongKind {
			return nil, errf(CodeFragmentMissing, "fragment %q is not in the library", fragment)
		}
		p.Fragments[fragment] = string(body)
	}
	// Every executed source goes through the same checks: the entry and, for
	// Typst, every other .typ file of the folder (they are executed too).
	for _, name := range executedFiles(t.Files, m.Source.Entry, engine) {
		if TestValidateHook != nil {
			TestValidateHook()
		}
		if err := deadlineErr(ctx); err != nil {
			return nil, err
		}
		if engine == EngineTypst {
			if err := checkTypstSource(ctx, t.Files[name], m.Fragments); err != nil {
				return nil, err
			}
		}
		if err := checkFunctions(t.Files[name]); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func engineProduces(engine, output string) bool {
	switch engine {
	case EngineTypst:
		return output == "pdf" || output == "png"
	case EngineHTML:
		return output == "html"
	case EngineText:
		return output == "txt"
	case EngineMJML:
		return output == "html" || output == "txt" || output == "eml"
	}
	return false
}

var (
	// Any string literal that looks like a package specification ("@preview/name:1.0"),
	// wherever it appears (parenthesized imports included). Defense in depth: the
	// runtime has no network and empty package directories.
	packageSpec   = regexp.MustCompile(`"@[A-Za-z]+/`)
	packageImport = regexp.MustCompile(`#\s*(import|include)\s+"@`)
	fragmentRef   = regexp.MustCompile(`#\s*(?:import|include)\s+"/?(fragments/[^"]+)"`)
)

func checkTypstSource(ctx context.Context, src string, declared []string) error {
	if !TestSkipPackageFilter && (packageImport.MatchString(src) || packageSpec.MatchString(src)) {
		return errf(CodePackageImport, "Typst packages (`@namespace/name`) are not available: the renderer has no network")
	}
	if err := typstCodeContextCtx(ctx, src); err != nil {
		return err
	}
	for _, match := range fragmentRef.FindAllStringSubmatch(src, -1) {
		if !contains(declared, match[1]) {
			return errf(CodeFragmentUndeclared, "fragment %q is used but not declared in manifest.fragments", match[1])
		}
	}
	return nil
}

// checkFunctions parses the entry with the real text/template and refuses
// forbidden and unknown functions.
func checkFunctions(src string) error {
	funcs := funcMap()
	for name := range reservedNames {
		funcs[name] = func(...any) any { return nil }
	}
	entry := newEntryName()
	t, err := template.New(entry).Funcs(funcs).Option("missingkey=zero").Parse(src)
	if err != nil {
		if strings.Contains(err.Error(), "not defined") {
			return errf(CodeUnknownFunction, "%v", err)
		}
		return errf(CodeTemplateSyntax, "%v", err)
	}
	if n := len(t.Templates()) - 1; n > DefaultMaxDefines {
		return errf(CodeLimitsExceeded, "%d define blocks, above the limit of %d", n, DefaultMaxDefines)
	}
	// A define or block cannot take an internal name (t, __present, __tick, escTypst,
	// ...) or start like the entry name. Checked on the parsed trees, not on text.
	for _, tpl := range t.Templates() {
		name := tpl.Name()
		if name != entry && (name == "t" || strings.HasPrefix(name, entryPrefix) || reservedNames[name] || forbiddenFunctions[name]) {
			return errf(CodeForbiddenFunction, "template name %q is reserved: it cannot be used by define or block", name)
		}
	}
	var bad string
	nilLiteral := false
	dollarAssigned := false
	for _, tpl := range t.Templates() {
		if tpl.Tree == nil {
			continue
		}
		walk(tpl.Tree.Root, func(n parse.Node) {
			// $ is the root (or the argument of a define) and is exempt from the absent
			// value check in the entry tree: it cannot be declared or reassigned.
			if pipe, ok := n.(*parse.PipeNode); ok && pipe != nil && !dollarAssigned {
				for _, v := range pipe.Decl {
					if len(v.Ident) > 0 && v.Ident[0] == "$" {
						dollarAssigned = true
					}
				}
			}
			// A literal nil would bypass __present: it is only allowed as an argument of default.
			if cmd, ok := n.(*parse.CommandNode); ok && bad == "" && !isDefaultCmd(cmd) {
				for _, a := range cmd.Args {
					if _, isNil := a.(*parse.NilNode); isNil {
						nilLiteral = true
					}
				}
			}
			if id, ok := n.(*parse.IdentifierNode); ok && bad == "" {
				if forbiddenFunctions[id.Ident] || reservedNames[id.Ident] {
					bad = id.Ident
				}
			}
		})
	}
	if dollarAssigned {
		return errf(CodeForbiddenFunction, "$ cannot be declared or reassigned")
	}
	if nilLiteral {
		return errf(CodeForbiddenFunction, "the literal nil is forbidden except as an argument of default")
	}
	if bad != "" {
		return errf(CodeForbiddenFunction, "function %q is forbidden in templates", bad)
	}
	return nil
}

// walk visits every node of a parse tree, including pipelines nested in
// parentheses and chains.
func walk(n parse.Node, visit func(parse.Node)) {
	if n == nil {
		return
	}
	switch x := n.(type) {
	case *parse.ListNode:
		if x == nil {
			return
		}
		visit(x)
		for _, c := range x.Nodes {
			walk(c, visit)
		}
	case *parse.ActionNode:
		visit(x)
		walk(x.Pipe, visit)
	case *parse.PipeNode:
		if x == nil {
			return
		}
		visit(x)
		for _, c := range x.Cmds {
			walk(c, visit)
		}
	case *parse.CommandNode:
		visit(x)
		for _, a := range x.Args {
			walk(a, visit)
		}
	case *parse.ChainNode:
		visit(x)
		walk(x.Node, visit)
	case *parse.IfNode:
		visit(x)
		walkBranch(&x.BranchNode, visit)
	case *parse.RangeNode:
		visit(x)
		walkBranch(&x.BranchNode, visit)
	case *parse.WithNode:
		visit(x)
		walkBranch(&x.BranchNode, visit)
	case *parse.TemplateNode:
		visit(x)
		walk(x.Pipe, visit)
	default:
		visit(n)
	}
}

func walkBranch(b *parse.BranchNode, visit func(parse.Node)) {
	walk(b.Pipe, visit)
	walk(b.List, visit)
	if b.ElseList != nil {
		walk(b.ElseList, visit)
	}
}

// ---- input_schema ----

var (
	forbiddenSchemaKeywords = []string{"$anchor", "$dynamicAnchor", "$dynamicRef", "$recursiveRef", "$recursiveAnchor"}
	localRef                = regexp.MustCompile(`^#/(\$defs|definitions)/`)
	reservedSpaces          = []string{"company", "brand", "doc", "i18n"}
)

func validateInputSchema(text string, out *any) error {
	var root any
	if err := decodeJSON([]byte(text), &root); err != nil {
		return errf(CodeInputSchemaInvalid, "input_schema is not valid JSON: %v", err)
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return errf(CodeInputSchemaInvalid, "input_schema must be a JSON object schema, not %T", root)
	}
	if t, _ := obj["type"].(string); t != "object" {
		return errf(CodeInputSchemaInvalid, `input_schema must declare "type": "object" at the root`)
	}
	if err := checkDialect(obj); err != nil {
		return err
	}
	if err := checkClosedSchema(obj, true); err != nil {
		return err
	}
	// Error priority, always in this order: structure (dialect, closed keywords,
	// non-local or unresolvable or cyclic references: input_schema_invalid), then
	// reserved namespaces, then the cost bound (limits_exceeded), then compilation.
	if err := checkRefs(obj); err != nil {
		return err
	}
	if err := checkReserved(obj, obj, map[string]bool{}, true); err != nil {
		return err
	}
	// Bound the evaluation cost before compiling: the validator cannot be interrupted.
	if c, err := schemaCost(obj); err != nil {
		return err
	} else if c > MaxSchemaCost {
		return errf(CodeLimitsExceeded, "input_schema evaluation cost is above %d (a chain of $ref/allOf definitions grows exponentially)", MaxSchemaCost)
	}
	// Compile once to catch invalid schemas and unresolvable local $ref.
	c := jsonschema.NewCompiler()
	c.UseLoader(denyLoader{})
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return errf(CodeInputSchemaInvalid, "%v", err)
	}
	if err := c.AddResource("mem:///input.schema.json", doc); err != nil {
		return errf(CodeInputSchemaInvalid, "%v", err)
	}
	if _, err := c.Compile("mem:///input.schema.json"); err != nil {
		return errf(CodeInputSchemaInvalid, "%s", sortedLines(err.Error()))
	}
	*out = root
	return nil
}

func validateData(schemaText string, data any) error {
	if _, ok := data.(map[string]any); !ok {
		return errf(CodeDataInvalid, "data must be a JSON object")
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(denyLoader{})
	doc, _ := jsonschema.UnmarshalJSON(strings.NewReader(schemaText))
	if err := c.AddResource("mem:///input.schema.json", doc); err != nil {
		return errf(CodeInputSchemaInvalid, "%v", err)
	}
	s, err := c.Compile("mem:///input.schema.json")
	if err != nil {
		return errf(CodeInputSchemaInvalid, "%s", sortedLines(err.Error()))
	}
	if err := s.Validate(data); err != nil {
		return errf(CodeDataInvalid, "%s", describeSchemaError(err))
	}
	return nil
}

// subschemaKeys lists keywords whose value is a map of subschemas, a list of
// subschemas or a single subschema.
var (
	mapKeys    = []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"}
	listKeys   = []string{"allOf", "anyOf", "oneOf", "prefixItems"}
	singleKeys = []string{"items", "additionalProperties", "if", "then", "else", "not", "contains", "propertyNames", "unevaluatedProperties", "unevaluatedItems"}
)

func checkClosedSchema(s map[string]any, isRoot bool) error {
	for _, k := range forbiddenSchemaKeywords {
		if _, ok := s[k]; ok {
			return errf(CodeInputSchemaInvalid, "keyword %s is not allowed in input_schema", k)
		}
	}
	if _, ok := s["$id"]; ok && !isRoot {
		return errf(CodeInputSchemaInvalid, "keyword $id is only allowed at the root of input_schema")
	}
	if ref, ok := s["$ref"]; ok {
		str, _ := ref.(string)
		if !localRef.MatchString(str) {
			return errf(CodeInputSchemaInvalid, "$ref %q is not a local pointer (#/$defs/... or #/definitions/...)", str)
		}
	}
	var err error
	each := func(v any) {
		if m, ok := v.(map[string]any); ok && err == nil {
			err = checkClosedSchema(m, false)
		}
	}
	for _, k := range mapKeys {
		if m, ok := s[k].(map[string]any); ok {
			for _, name := range sortedAny(m) {
				each(m[name])
			}
		}
	}
	for _, k := range listKeys {
		if l, ok := s[k].([]any); ok {
			for _, v := range l {
				each(v)
			}
		}
	}
	for _, k := range singleKeys {
		each(s[k])
	}
	return err
}

func reserved(name string) bool {
	for _, r := range reservedSpaces {
		if r == name {
			return true
		}
	}
	return false
}

// checkReserved refuses an input_schema that declares company, brand, doc or
// i18n at the root, directly or through composition keywords, local $ref,
// patternProperties and a schema-valued additionalProperties.
func checkReserved(root, s map[string]any, seen map[string]bool, isRoot bool) error {
	fail := func(how string) error {
		return errf(CodeReservedNamespace, "input_schema redefines a reserved namespace (%s): company, brand, doc and i18n are injected by the platform", how)
	}
	if props, ok := s["properties"].(map[string]any); ok {
		for _, name := range sortedAny(props) {
			if reserved(name) {
				return fail("property " + name)
			}
		}
	}
	if req, ok := s["required"].([]any); ok {
		for _, r := range req {
			if name, _ := r.(string); reserved(name) {
				return fail("required " + name)
			}
		}
	}
	if dr, ok := s["dependentRequired"].(map[string]any); ok {
		for _, name := range sortedAny(dr) {
			list := dr[name]
			if reserved(name) {
				return fail("dependentRequired " + name)
			}
			if l, ok := list.([]any); ok {
				for _, r := range l {
					if n, _ := r.(string); reserved(n) {
						return fail("dependentRequired " + n)
					}
				}
			}
		}
	}
	if pp, ok := s["patternProperties"].(map[string]any); ok {
		for _, pattern := range sortedAny(pp) {
			re, err := regexp.Compile(pattern)
			if err != nil {
				continue
			}
			for _, r := range reservedSpaces {
				if re.MatchString(r) {
					return fail("patternProperties " + pattern)
				}
			}
		}
	}
	if isRoot {
		if ap, ok := s["additionalProperties"].(map[string]any); ok && len(ap) > 0 {
			return fail("additionalProperties schema at the root")
		}
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		if l, ok := s[k].([]any); ok {
			for _, v := range l {
				if m, ok := v.(map[string]any); ok {
					if err := checkReserved(root, m, seen, false); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, k := range []string{"if", "then", "else"} {
		if m, ok := s[k].(map[string]any); ok {
			if err := checkReserved(root, m, seen, false); err != nil {
				return err
			}
		}
	}
	if ds, ok := s["dependentSchemas"].(map[string]any); ok {
		for _, name := range sortedAny(ds) {
			if m, ok := ds[name].(map[string]any); ok {
				if err := checkReserved(root, m, seen, false); err != nil {
					return err
				}
			}
		}
	}
	if ref, ok := s["$ref"].(string); ok && !seen[ref] {
		seen[ref] = true
		if target := resolveLocal(root, ref); target != nil {
			if err := checkReserved(root, target, seen, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveLocal(root map[string]any, ref string) map[string]any {
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var cur any = root
	for _, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	m, _ := cur.(map[string]any)
	return m
}

// LibraryFragments lists the fragments of the embedded library, sorted.
func LibraryFragments() []string {
	var out []string
	for _, dir := range []string{"fragments", messageFragmentDir} {
		entries, _ := contractFS.ReadDir("contract/" + dir)
		for _, e := range entries {
			if !e.IsDir() {
				out = append(out, path.Join(dir, e.Name()))
			}
		}
	}
	sort.Strings(out)
	return out
}

// allowedExtensions are the extensions a template folder may contain (besides
// the extension of its own entry). Paths are plain strings in a JSON object, so
// links cannot exist; absolute paths and `..` are refused by relativePath.
var allowedExtensions = map[string]bool{".typ": true, ".svg": true, ".png": true, ".jpg": true, ".json": true, ".csv": true}

// generatedNames are written by the platform into the Typst project: a
// template cannot provide them.
var generatedNames = map[string]bool{"brand.typ": true, "values.json": true, "out.pdf": true}

func reservedName(name string) bool {
	if generatedNames[name] || strings.HasPrefix(name, "fragments/") {
		return true
	}
	return strings.HasPrefix(name, "page-") && strings.HasSuffix(name, ".png") && !strings.Contains(name, "/")
}

func checkFileNames(ctx context.Context, files map[string]string, entry string) error {
	for _, name := range sortedKeys(files) {
		if err := deadlineErr(ctx); err != nil {
			return err
		}
		if reservedName(name) {
			return errf(CodeReservedFileName, "file %q is reserved: brand.typ, values.json, out.pdf, page-*.png and fragments/ are written by the platform", name)
		}
		ext := strings.ToLower(path.Ext(name))
		if !allowedExtensions[ext] && ext != strings.ToLower(path.Ext(entry)) {
			return errf(CodeFileExtension, "file %q: extension %q is not allowed (typ, svg, png, jpg, json, csv)", name, ext)
		}
		for other := range files {
			if strings.HasPrefix(other, name+"/") {
				return errf(CodeFilePathInvalid, "file path %q is both a file and a directory", name)
			}
		}
	}
	return nil
}

// executedFiles lists the files run through the Go template execution, entry first.
func executedFiles(files map[string]string, entry, engine string) []string {
	out := []string{entry}
	if engine != EngineTypst {
		return out
	}
	var rest []string
	for name := range files {
		if name != entry && strings.EqualFold(path.Ext(name), ".typ") {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// describeSchemaError renders a validation error deterministically: the
// validator may report its causes in any order, so the leaves are collected,
// sorted and joined (at most 8).
func describeSchemaError(err error) string {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return short(err.Error())
	}
	set := map[string]bool{}
	var walkErr func(e *jsonschema.ValidationError)
	walkErr = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaf := &jsonschema.ValidationError{ErrorKind: e.ErrorKind, InstanceLocation: e.InstanceLocation, SchemaURL: e.SchemaURL}
			set[strings.Join(strings.Fields(leaf.Error()), " ")] = true
			return
		}
		for _, c := range e.Causes {
			walkErr(c)
		}
	}
	walkErr(ve)
	lines := make([]string, 0, len(set))
	for l := range set {
		lines = append(lines, l)
	}
	sort.Strings(lines)
	if len(lines) > 8 {
		lines = append(lines[:8], "...")
	}
	return short(strings.Join(lines, "; "))
}

// sortedLines makes a multi-line error message order-independent.
func sortedLines(msg string) string {
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	sort.Strings(lines)
	return short(strings.Join(lines, "; "))
}

// sortedAny returns the keys of a JSON object in sorted order.
func sortedAny(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

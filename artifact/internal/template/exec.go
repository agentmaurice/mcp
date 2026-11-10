package template

// This file reproduces the execution semantics of the reference renderer of
// the template/v1 contract (contracts/template/v1/tools/render_reference/main.go,
// see contract/PIN): Go templates run against the single object
// {data, company, brand, doc, i18n} with missingkey=zero; Typst values are
// passed BY REFERENCE (`#__v.at(N)` plus a values table) so a value is never
// part of the Typst source; html and mjml use html/template; text escapes nothing.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"text/template"
	"text/template/parse"
	"time"
)

// Engines supported by this renderer.
const (
	EngineTypst = "typst"
	EngineHTML  = "html"
	EngineText  = "text"
	EngineMJML  = "mjml"
)

// presentName is appended after the author's commands (so after a `default`)
// and before escaping: it fails when the value is absent (nil).
const presentName = "__present"

// tickName is a hidden function run at the start of every range iteration: it
// reads the clock every 1024 calls and fails once the render deadline passed,
// because text/template cannot be interrupted from outside.
const tickName = "__tick"

const noValueMsg = "a missing key is rendered unprotected (`<no value>`): use default, if or with"

// Reserved names: a template that calls them is refused (forbidden_function).
var reservedNames = map[string]bool{
	presentName: true, tickName: true, "escTypst": true, "escHTML": true, "escMarkdown": true,
	"escMJML": true, "escText": true, "escJSON": true, "raw": true,
}

// forbiddenFunctions are refused in templates: `now` (non reproducible, use
// doc.date), `call` and `raw`.
var forbiddenFunctions = map[string]bool{"now": true, "call": true, "raw": true}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"default": func(def, actual any) any {
			if isZero(actual) {
				return def
			}
			return actual
		},
		"json": func(v any) (string, error) {
			if v == nil {
				return "", nil
			}
			b, err := json.MarshalIndent(v, "", "  ")
			return string(b), err
		},
		"now": func() string { return time.Now().UTC().Format(time.RFC3339) },
	}
}

func isZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case markedString:
		return x == ""
	case bool:
		return !x
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// present is the __present function. It also checks the render deadline on
// every output action, so an abandoned execution stops soon after the timeout.
func (e *executor) tick() (string, error) {
	if e.ticks.Add(1)&1023 == 0 {
		if err := e.gctx.Err(); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (e *executor) present(v any) (any, error) {
	if err := e.gctx.Err(); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("absent value: protect with default, if or with")
	}
	return v, nil
}

func toString(v any) string {
	if str, ok := v.(string); ok {
		return str
	}
	return fmt.Sprint(v)
}

// executor runs the templates of one render against one context. For the
// typst engine, values holds the table of values passed by reference.
type executor struct {
	entry     string // name of the entry template (random per executor)
	gctx      context.Context
	maxOutput int
	// values.json budget: each value is counted before it is stored.
	maxValues   int
	valuesBytes int
	engine      string
	ctx         map[string]any
	values      []string
	ticks       atomic.Uint64
}

func newExecutor(gctx context.Context, engine string, ctx map[string]any, maxOutput int) (*executor, error) {
	switch engine {
	case EngineTypst, EngineHTML, EngineText, EngineMJML:
		if maxOutput <= 0 {
			maxOutput = DefaultMaxOutputBytes
		}
		return &executor{entry: newEntryName(), gctx: gctx, maxOutput: maxOutput, maxValues: DefaultMaxValuesBytes, valuesBytes: 2, engine: engine, ctx: ctx}, nil
	}
	return nil, &Error{Code: CodeEngineUnsupported, Message: fmt.Sprintf("engine %q is not supported", engine)}
}

var (
	errOutputLimit = errors.New("output size limit exceeded")
	errValuesLimit = errors.New("values table size limit exceeded")
)

// limitWriter fails once more than max bytes are written.
type limitWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		return 0, errOutputLimit
	}
	return w.buf.Write(p)
}

var _ io.Writer = (*limitWriter)(nil)

// render executes src under the render deadline and the output size limit.
// The execution runs in a goroutine: on timeout the caller gets render_timeout
// at once and the abandoned execution stops at its next output action.
func (e *executor) render(src string) (string, error) {
	type outcome struct {
		out string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := e.renderNow(src)
		done <- outcome{out, err}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			var coded *Error
			if errors.As(o.err, &coded) {
				return "", coded
			}
			if errors.Is(o.err, errOutputLimit) {
				return "", errf(CodeLimitsExceeded, "rendered output exceeds %d bytes", e.maxOutput)
			}
			if errors.Is(o.err, errValuesLimit) {
				return "", errf(CodeLimitsExceeded, "the values table (values.json) exceeds %d bytes", e.maxValues)
			}
			if e.gctx.Err() != nil {
				return "", errf(CodeRenderTimeout, "template execution exceeded the time limit")
			}
		}
		return o.out, o.err
	case <-e.gctx.Done():
		return "", errf(CodeRenderTimeout, "template execution exceeded the time limit")
	}
}

func (e *executor) renderNow(src string) (string, error) {
	switch e.engine {
	case EngineHTML, EngineMJML:
		// mjml: MJML is XML-like markup, so a value is escaped like html/template
		// escapes it in HTML text and attributes (the contract names this escaper escMJML).
		return e.renderHTML(src)
	case EngineTypst:
		return e.renderTypst(src)
	}
	return e.renderText(src)
}

// renderHTML passes the text/template tree (with __present) to html/template
// through AddParseTree, which appends its contextual escapers.
func (e *executor) renderHTML(src string) (string, error) {
	t, err := template.New(e.entry).Funcs(e.funcs()).Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", err
	}
	for _, tpl := range t.Templates() {
		if tpl.Tree != nil {
			escapeNode(tpl.Tree.Root, tpl.Tree, presentName)
			tickTree(tpl.Tree)
		}
	}
	funcs := e.funcs()
	funcs[presentName] = e.present
	funcs[tickName] = e.tick
	root, err := htmltemplate.New(e.entry).Funcs(htmltemplate.FuncMap(funcs)).Option("missingkey=zero").AddParseTree(e.entry, t.Tree)
	if err != nil {
		return "", err
	}
	for _, tpl := range t.Templates() {
		if tpl.Name() != e.entry && tpl.Tree != nil {
			if _, err := root.AddParseTree(tpl.Name(), tpl.Tree); err != nil {
				return "", err
			}
		}
	}
	buf := &limitWriter{max: e.maxOutput}
	if err := root.Execute(buf, e.ctx); err != nil {
		return "", err
	}
	// Absence is detected by __present (an absent key is nil under missingkey=zero),
	// never by looking for a text in the output: a value may legitimately be "<no value>".
	return buf.buf.String(), nil
}

// renderTypst stores each action output in the table and renders `#__v.at(N)`.
func (e *executor) renderTypst(src string) (string, error) {
	const name = "escTypst"
	t, err := template.New(e.entry).Funcs(e.funcs()).Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", err
	}
	missing := false
	t.Funcs(template.FuncMap{presentName: e.present, tickName: e.tick, name: func(v any) (string, error) {
		if v == nil {
			missing = true
			return "", nil
		}
		str := toString(v)
		// Count the real JSON size in values.json (escapes such as < -> \u003c
		// included) plus the comma: the file is written with the same json.Marshal.
		if e.valuesBytes+len(str)+1 > e.maxValues {
			return "", errValuesLimit
		}
		enc, _ := json.Marshal(str)
		if e.valuesBytes+len(enc)+1 > e.maxValues {
			return "", errValuesLimit
		}
		e.valuesBytes += len(enc) + 1
		e.values = append(e.values, str)
		return fmt.Sprintf("#__v.at(%d)", len(e.values)-1), nil
	}})
	for _, tpl := range t.Templates() {
		if tpl.Tree != nil {
			escapeNode(tpl.Tree.Root, tpl.Tree, presentName, name)
			tickTree(tpl.Tree)
		}
	}
	buf := &limitWriter{max: e.maxOutput}
	if err := t.Execute(buf, e.ctx); err != nil {
		return "", err
	}
	if missing {
		return "", fmt.Errorf("%s", noValueMsg)
	}
	return "#let __v = json(\"/values.json\")\n" + buf.buf.String(), nil
}

// renderText runs text/template with the engine's escape function appended
// after the parse (a template that calls it itself fails: undefined function).
func (e *executor) renderText(src string) (string, error) {
	const name = "escText"
	t, err := template.New(e.entry).Funcs(e.funcs()).Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", err
	}
	missing := false
	t.Funcs(template.FuncMap{presentName: e.present, tickName: e.tick, name: func(v any) string {
		if v == nil {
			missing = true
			return ""
		}
		return toString(v)
	}})
	for _, tpl := range t.Templates() {
		if tpl.Tree != nil {
			escapeNode(tpl.Tree.Root, tpl.Tree, presentName, name)
			tickTree(tpl.Tree)
		}
	}
	buf := &limitWriter{max: e.maxOutput}
	if err := t.Execute(buf, e.ctx); err != nil {
		return "", err
	}
	if missing {
		return "", fmt.Errorf("%s", noValueMsg)
	}
	return buf.buf.String(), nil
}

// valuesJSON is the content of values.json (the table of referenced values).
func (e *executor) valuesJSON() []byte {
	b, _ := json.Marshal(e.values)
	return b
}

// escapeNode appends the named functions as the last commands of the pipeline
// of every action that produces output (like html/template). Declarations and
// assignments of variables produce nothing and are left alone.
func escapeNode(node parse.Node, tree *parse.Tree, names ...string) {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, child := range n.Nodes {
			escapeNode(child, tree, names...)
		}
	case *parse.ActionNode:
		// Declarations get the same argument checks; only their final result stays
		// free (`$x := .data.absent` is checked where $x is used).
		guardPipe(n.Pipe, false, tree)
		if len(n.Pipe.Decl) == 0 {
			command := func(name string) *parse.CommandNode {
				return &parse.CommandNode{NodeType: parse.NodeCommand, Pos: n.Pos,
					Args: []parse.Node{parse.NewIdentifier(name).SetTree(tree).SetPos(n.Pos)}}
			}
			for k, name := range names {
				if k == 0 && name == presentName {
					// html/template refuses a predefined escaper that is not last in the
					// pipeline: the final __present goes just before a trailing block of
					// html, urlquery or js (they never turn nil into a value).
					at := len(n.Pipe.Cmds)
					for at > 0 && isPredefinedEscaper(n.Pipe.Cmds[at-1]) {
						at--
					}
					explicit := false // `html .data.x`: a call with arguments; they are guarded already
					for _, c := range n.Pipe.Cmds[at:] {
						explicit = explicit || len(c.Args) > 1
					}
					if explicit {
						continue
					}
					cmds := append([]*parse.CommandNode{}, n.Pipe.Cmds[:at]...)
					cmds = append(cmds, command(name))
					n.Pipe.Cmds = append(cmds, n.Pipe.Cmds[at:]...)
					continue
				}
				n.Pipe.Cmds = append(n.Pipe.Cmds, command(name))
			}
		}
	case *parse.TemplateNode:
		// {{ template "x" pipeline }}: same argument checks; the result is the
		// body's output, checked where the body prints it.
		if n.Pipe != nil {
			guardPipe(n.Pipe, false, tree)
		}
	case *parse.IfNode:
		escapeBranch(&n.BranchNode, tree, names...)
	case *parse.RangeNode:
		escapeBranch(&n.BranchNode, tree, names...)
	case *parse.WithNode:
		escapeBranch(&n.BranchNode, tree, names...)
	}
}

func escapeBranch(b *parse.BranchNode, tree *parse.Tree, names ...string) {
	if b.Pipe != nil {
		// Conditions of if, with and range: only the final result stays free (so
		// `if .data.flag` works); arguments of functions other than default and
		// intermediate commands are checked like everywhere else.
		guardPipe(b.Pipe, false, tree)
	}
	escapeNode(b.List, tree, names...)
	escapeNode(b.ElseList, tree, names...)
}

// guardPipe makes an absent value visible inside an output action. __present
// only checks the final result, but printf and friends turn a nil argument into
// a plain string ("%!s(<nil>)"). So every operand (field, chain, variable with
// fields, parenthesized sub-pipeline) given to a function other than default is
// wrapped as `(arg | __present)`; a bare operand piped into a function other
// than default is checked too. Arguments of default and the conditions of if,
// with and range stay free: they are the guards. checkResult appends the final
// check, used for parenthesized sub-pipelines (the top pipeline gets it from
// escapeNode).
func guardPipe(p *parse.PipeNode, checkResult bool, tree *parse.Tree) {
	var cmds []*parse.CommandNode
	for i, cmd := range p.Cmds {
		cmds = append(cmds, cmd)
		// The result of a parenthesized operand stays free when it feeds default
		// directly, or when it is the final result of a pipeline whose result is
		// free (the caller checks it where it is used); its inside is still guarded.
		next := i+1 < len(p.Cmds)
		free := (next && isDefaultCmd(p.Cmds[i+1])) || (!next && !checkResult)
		guardCommand(cmd, tree, free)
		// The result of a command that feeds the next one is checked, unless the
		// next command is default (the guard). This covers index, printf, ... chains.
		if i+1 < len(p.Cmds) && !isDefaultCmd(p.Cmds[i+1]) {
			cmds = append(cmds, presentCommand(cmd.Pos, tree))
		}
	}
	p.Cmds = cmds
	if checkResult {
		p.Cmds = append(p.Cmds, presentCommand(p.Pos, tree))
	}
}

// guardCommand guards the arguments of one command: operands of a function
// other than default are wrapped; arguments of default stay free.
func guardCommand(cmd *parse.CommandNode, tree *parse.Tree, free bool) {
	if len(cmd.Args) == 0 {
		return
	}
	if id, ok := cmd.Args[0].(*parse.IdentifierNode); ok {
		if id.Ident == "default" {
			for _, a := range cmd.Args[1:] {
				if sp, ok := a.(*parse.PipeNode); ok {
					guardPipe(sp, false, tree)
				}
				guardChain(a, tree)
			}
			return
		}
		for j := 1; j < len(cmd.Args); j++ {
			cmd.Args[j] = guardArg(cmd.Args[j], tree)
		}
		return
	}
	if len(cmd.Args) == 1 {
		if sp, ok := cmd.Args[0].(*parse.PipeNode); ok {
			guardPipe(sp, !free, tree)
		}
	}
	for _, a := range cmd.Args {
		guardChain(a, tree)
	}
}

func isDefaultCmd(c *parse.CommandNode) bool {
	if len(c.Args) == 0 {
		return false
	}
	id, ok := c.Args[0].(*parse.IdentifierNode)
	return ok && id.Ident == "default"
}

// entryPrefix starts the name of the entry template: it carries a random nonce
// per executor (`__entry_<nonce>`), so no define can collide with it, and any
// define name starting with the prefix is reserved.
const entryPrefix = "__entry_"

func newEntryName() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return entryPrefix + hex.EncodeToString(b[:])
}

func needsGuard(n parse.Node, tree *parse.Tree) bool {
	switch x := n.(type) {
	case *parse.FieldNode, *parse.ChainNode, *parse.DotNode:
		// the dot of a sub-template or range body may hold an absent value too
		return true
	case *parse.VariableNode:
		// `$` alone is the root and never nil in the entry tree; in a define it is the
		// argument of the call and may be nil (like the dot). `$x` may hold an absent value.
		if len(x.Ident) == 1 && x.Ident[0] == "$" {
			return tree != nil && !strings.HasPrefix(tree.Name, entryPrefix)
		}
		return true
	}
	return false
}

func presentCommand(pos parse.Pos, tree *parse.Tree) *parse.CommandNode {
	return &parse.CommandNode{NodeType: parse.NodeCommand, Pos: pos,
		Args: []parse.Node{parse.NewIdentifier(presentName).SetTree(tree).SetPos(pos)}}
}

// guardArg wraps an operand as `(arg | __present)` and guards sub-pipelines.
func guardArg(a parse.Node, tree *parse.Tree) parse.Node {
	switch x := a.(type) {
	case *parse.PipeNode:
		guardPipe(x, true, tree)
		return x
	case *parse.FieldNode, *parse.ChainNode, *parse.VariableNode, *parse.DotNode:
		guardChain(a, tree)
		if !needsGuard(a, tree) {
			return a
		}
		return &parse.PipeNode{NodeType: parse.NodePipe, Pos: a.Position(), Cmds: []*parse.CommandNode{
			{NodeType: parse.NodeCommand, Pos: a.Position(), Args: []parse.Node{a}},
			presentCommand(a.Position(), tree),
		}}
	}
	return a
}

// insertTicks adds `{{ $__tick := __tick }}` at the start of every range body.
// It is a declaration: it writes nothing and html/template leaves it alone.
func insertTicks(node parse.Node, tree *parse.Tree) {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			insertTicks(c, tree)
		}
	case *parse.IfNode:
		insertTicks(n.List, tree)
		insertTicks(n.ElseList, tree)
	case *parse.WithNode:
		insertTicks(n.List, tree)
		insertTicks(n.ElseList, tree)
	case *parse.RangeNode:
		insertTicks(n.List, tree)
		insertTicks(n.ElseList, tree)
		if n.List == nil {
			return
		}
		act := tickAction(n.Pos, tree)
		n.List.Nodes = append([]parse.Node{act}, n.List.Nodes...)
	}
}

// funcs is the FuncMap exposed to templates. Functions that can build a larger
// value than their inputs (printf, print, println, html, js, urlquery, json)
// are wrapped: after each call the render deadline is checked (same counter as
// __tick) and a string result above the output budget is limits_exceeded;
// printf refuses before allocating when printfBound (a walk of the format) is above the budget.
// index, slice, len, eq and the other builtins cannot grow a value, so they are
// left as they are.
func (e *executor) funcs() template.FuncMap {
	m := funcMap()
	checked := func(s string) (string, error) {
		if err := e.checkTick(); err != nil {
			return "", err
		}
		if len(s) > e.maxOutput {
			return "", errOutputLimit
		}
		return s, nil
	}
	// estimate refuses before the call when the cautious bound of the result
	// (see argsBound) is above the output budget.
	estimate := func(args []any, factor, extra int) error {
		for _, a := range args {
			if unsafeKind(a) {
				return errf(CodeForbiddenFunction, "a pointer, function, channel or unsafe value cannot be printed")
			}
		}
		if argsBound(args, factor, extra, e.maxOutput) > e.maxOutput {
			return errOutputLimit
		}
		return nil
	}
	m["printf"] = func(format string, args ...any) (string, error) {
		bound, err := printfBound(format, args, e.maxOutput)
		if err != nil {
			return "", err
		}
		if bound > e.maxOutput {
			return "", errOutputLimit
		}
		return checked(fmt.Sprintf(format, args...))
	}
	m["print"] = func(args ...any) (string, error) {
		if err := estimate(args, 1, 0); err != nil {
			return "", err
		}
		return checked(fmt.Sprint(args...))
	}
	m["println"] = func(args ...any) (string, error) {
		if err := estimate(args, 1, 1); err != nil {
			return "", err
		}
		return checked(fmt.Sprintln(args...))
	}
	m["html"] = func(args ...any) (string, error) {
		if err := estimate(args, 6, 0); err != nil {
			return "", err
		}
		return checked(template.HTMLEscaper(args...))
	}
	m["js"] = func(args ...any) (string, error) {
		if err := estimate(args, 6, 0); err != nil {
			return "", err
		}
		return checked(template.JSEscaper(args...))
	}
	m["urlquery"] = func(args ...any) (string, error) {
		if err := estimate(args, 3, 0); err != nil {
			return "", err
		}
		return checked(template.URLQueryEscaper(args...))
	}
	baseJSON := m["json"].(func(any) (string, error))
	jsonBounds := map[identityKey]int{} // cache by identity of maps and slices (see identityKey)
	m["json"] = func(v any) (string, error) {
		if err := e.checkTick(); err != nil {
			return "", err
		}
		if jsonBound(v, e.maxOutput, jsonBounds) > e.maxOutput {
			return "", errOutputLimit
		}
		out, err := baseJSON(v)
		if err != nil {
			return "", err
		}
		return checked(out)
	}
	baseDefault := m["default"].(func(any, any) any)
	m["default"] = func(def, actual any) (any, error) {
		if err := e.checkTick(); err != nil {
			return nil, err
		}
		return baseDefault(def, actual), nil
	}
	return m
}

// checkTick counts a function call and reads the clock every 1024 calls.
func (e *executor) checkTick() error {
	_, err := e.tick()
	return err
}

// tickAction builds `{{ $__tick := __tick }}`: a declaration, it writes nothing.
func tickAction(pos parse.Pos, tree *parse.Tree) *parse.ActionNode {
	v := &parse.VariableNode{NodeType: parse.NodeVariable, Pos: pos, Ident: []string{"$__tick"}}
	cmd := &parse.CommandNode{NodeType: parse.NodeCommand, Pos: pos,
		Args: []parse.Node{parse.NewIdentifier(tickName).SetTree(tree).SetPos(pos)}}
	return &parse.ActionNode{NodeType: parse.NodeAction, Pos: pos,
		Pipe: &parse.PipeNode{NodeType: parse.NodePipe, Pos: pos, Decl: []*parse.VariableNode{v}, Cmds: []*parse.CommandNode{cmd}}}
}

// tickTree adds the tick to every range body of a tree and at the start of the
// tree itself (the entry and every define), so a chain of sub-templates that
// writes nothing still reaches the deadline check.
func tickTree(tree *parse.Tree) {
	insertTicks(tree.Root, tree)
	if tree.Root != nil {
		tree.Root.Nodes = append([]parse.Node{tickAction(tree.Root.Pos, tree)}, tree.Root.Nodes...)
	}
}

// guardChain guards the pipeline hidden in a chain: `(pipeline).field`. The
// result of that pipeline is checked, since a field is read from it.
func guardChain(n parse.Node, tree *parse.Tree) {
	if c, ok := n.(*parse.ChainNode); ok {
		if sp, ok := c.Node.(*parse.PipeNode); ok {
			guardPipe(sp, true, tree)
		}
	}
}

// isPredefinedEscaper reports whether a command is a call to html, urlquery or js.
func isPredefinedEscaper(c *parse.CommandNode) bool {
	if len(c.Args) == 0 {
		return false
	}
	id, ok := c.Args[0].(*parse.IdentifierNode)
	return ok && (id.Ident == "html" || id.Ident == "urlquery" || id.Ident == "js")
}

// jsonBound bounds the size of json (MarshalIndent) before it allocates: a
// string is escaped up to 6 times; a collection costs its compact JSON length
// plus, for every node, the newline and the indentation (2 x depth). Data depth
// is at most MaxDataDepth (checked at validation), so the walks are bounded.
// The result is cached per map or slice identity, so a json call in a range
// over a big value is measured once.
// identityKey identifies a value for a cache. A slice and its sub-slices share
// the data pointer, so the key also holds the length and the capacity: `slice $a
// 0 1` and `$a` get different keys. A map cannot be sliced or partially viewed
// by any template function (slice works on slices, arrays and strings; index
// returns an element, which is another value), so its pointer is enough.
type identityKey struct {
	ptr      uintptr
	len, cap int
}

func keyOf(rv reflect.Value) identityKey {
	switch rv.Kind() {
	case reflect.Slice:
		return identityKey{rv.Pointer(), rv.Len(), rv.Cap()}
	case reflect.Map:
		return identityKey{rv.Pointer(), 0, 0}
	}
	return identityKey{}
}

func jsonBound(v any, limit int, cache map[identityKey]int) int {
	if s, ok := v.(string); ok {
		return len(s)*6 + 2
	}
	rv := reflect.ValueOf(v)
	var key identityKey
	switch rv.Kind() {
	case reflect.Map, reflect.Slice:
		key = keyOf(rv)
		if key.ptr != 0 {
			if b, ok := cache[key]; ok {
				return b
			}
		}
	case reflect.Array, reflect.Struct:
	default:
		return fixedArgBound
	}
	compact := valueSize(v, limit)
	nodes := countNodes(rv, limit)
	depth := dataDepth(v, MaxDataDepth+1)
	bound := compact + nodes*(2*depth+2)
	if bound > limit || bound < 0 {
		bound = limit + 1
	}
	if key.ptr != 0 {
		cache[key] = bound
	}
	return bound
}

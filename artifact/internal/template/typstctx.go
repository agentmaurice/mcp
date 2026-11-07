package template

import (
	"context"
	"regexp"
	"strings"
)

// typstCodeContext refuses an action that produces output in a Typst code
// context (a "..." string, a #{ } or #( ) block, the arguments of #f( ) outside
// brackets, or a #let/#set/#show/#import/#include/#if/#for/#while line). It
// follows the `typst_code_context` rule of template_check.py in the contract:
// a small automaton with a stack of modes (markup M, code C, content block B).
// Control actions (if, range, with, end, ...) and variable assignments produce
// nothing and are allowed anywhere. This is a guard against author mistakes;
// the security barrier stays the by-reference passing of values.
func typstCodeContext(src string) error { return typstCodeContextCtx(context.Background(), src) }

// typstCodeContextCtx is typstCodeContext under a deadline (checked every 4096 steps).
func typstCodeContextCtx(ctx context.Context, src string) error {
	type frame struct {
		mode     byte // 'M', 'C', 'B'
		codeline bool
	}
	// The context is computed on the text as it will be after text/template trims
	// whitespace ({{- and -}}) and drops comment actions: the neighbours of an
	// action are the characters that remain.
	src = stripCommentActions(applyTrim(src))
	table := newActionTable(src)
	stack := []frame{{mode: 'M'}}
	top := func() *frame { return &stack[len(stack)-1] }
	inString := false
	n := len(src)
	// flag refuses every output action found in src[start:end].
	flag := func(start, end int, code, where string) error {
		for i := start; i < end; {
			k := strings.Index(src[i:end], "{{")
			if k < 0 {
				return nil
			}
			at := i + k
			stop := actionEnd(src, at)
			body := strings.TrimSpace(strings.Trim(src[at+2:stop], "-"))
			if producesOutput(body) {
				return errf(code, "action `{{ %s }}` %s: its value would be rendered wrongly; place it in ordinary markup or in content ([...])", short(body), where)
			}
			i = stop + 2
		}
		return nil
	}
	steps := 0
	for i := 0; i < n; {
		if steps++; steps&4095 == 0 {
			if err := deadlineErr(ctx); err != nil {
				return err
			}
		}
		if strings.HasPrefix(src[i:], "{{") {
			end := actionEnd(src, i)
			body := strings.TrimSpace(strings.Trim(src[i+2:end], "-"))
			if end < n {
				end += 2
			}
			if producesOutput(body) {
				code := inString || top().mode == 'C' || (top().mode != 'C' && top().codeline)
				before := table.before(src, i)
				// A backslash before the action (after trim and after the actions that print
				// nothing) turns the reference `#__v.at(N)` into literal text.
				if trailingBackslashes(before)%2 == 1 {
					return errf(CodeTypstEscapedAction, "action `{{ %s }}` is preceded by a backslash: `\\#` neutralizes the value and displays `#__v.at(N)`; remove the backslash", short(body))
				}
				glued := gluedToHash(before)
				if !glued && !code {
					for _, lit := range table.followingLiterals(i) {
						if afterRe.MatchString(lit[:min(2, len(lit))]) {
							return errf(CodeTypstCodeContext, "action `{{ %s }}` is followed by `%s`: Typst would read it as a call or an access on `#__v.at(N)`; insert text or a space", short(body), lit[:1])
						}
					}
				}
				if code || glued {
					where := "in a Typst code context"
					if inString {
						where = "inside a Typst string"
					} else if glued && !code {
						where = "glued to a `#` (Typst expression)"
					}
					return errf(CodeTypstCodeContext, "action `{{ %s }}` %s: place it in content ([...]) or markup", short(body), where)
				}
			}
			i = end
			continue
		}
		c := src[i]
		if inString {
			switch c {
			case '\\':
				if strings.HasPrefix(src[i+1:], "{{") {
					i++
				} else {
					i += 2
				}
				continue
			case '"':
				inString = false
			}
			i++
			continue
		}
		// Markup at any depth (the root and every [..] block): a line starting with
		// #let, #set, #show, #import, #include, #if, #for, #while is code until its end.
		code := top().mode == 'C' || (top().mode != 'C' && top().codeline)
		switch {
		case c == '\\':
			if strings.HasPrefix(src[i+1:], "{{") {
				i++
			} else {
				i += 2
			}
			continue
		case c == '\n':
			if top().mode != 'C' {
				top().codeline = false
			}
		case strings.HasPrefix(src[i:], "//") && (i == 0 || src[i-1] != ':'):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				j = n - i
			}
			if err := flag(i, i+j, CodeTypstCommentContext, "in a Typst comment"); err != nil {
				return err
			}
			i += j
			continue
		case strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i+2:], "*/")
			end := n
			if j >= 0 {
				end = i + 2 + j + 2
			}
			if err := flag(i, end, CodeTypstCommentContext, "in a Typst comment"); err != nil {
				return err
			}
			i = end
			continue
		case code:
			switch c {
			case '"':
				inString = true
			case '(', '{':
				stack = append(stack, frame{mode: 'C'})
			case '[':
				stack = append(stack, frame{mode: 'B'})
			case ')', '}':
				if top().mode == 'C' && len(stack) > 1 {
					stack = stack[:len(stack)-1]
				}
			}
		case c == '`': // raw: ` or ``` blocks do not render an action output
			fence := "`"
			if strings.HasPrefix(src[i:], "```") {
				fence = "```"
			}
			end := n
			if j := strings.Index(src[i+len(fence):], fence); j >= 0 {
				end = i + len(fence) + j + len(fence)
			}
			if err := flag(i, end, CodeTypstRawContext, "in a Typst raw block"); err != nil {
				return err
			}
			i = end
			continue
		case c == '$': // math
			end := n
			if j := strings.IndexByte(src[i+1:], '$'); j >= 0 {
				end = i + 1 + j + 1
			}
			if err := flag(i, end, CodeTypstMathContext, "in a Typst math formula"); err != nil {
				return err
			}
			i = end
			continue
		case c == '[':
			if top().mode == 'B' {
				stack = append(stack, frame{mode: 'B'})
			}
		case c == ']':
			if top().mode == 'B' && len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case c == '#':
			rest := src[i+1:]
			if strings.HasPrefix(rest, "{{") {
				i++ // glued action: reported by the glued-# rule
				continue
			}
			if strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, "(") {
				stack = append(stack, frame{mode: 'C'})
				i += 2
				continue
			}
			if id := typstIdent.FindString(rest); id != "" {
				lineStart := strings.TrimSpace(src[strings.LastIndexByte(src[:i], '\n')+1:i]) == ""
				if lineStart && typstCodeLine.MatchString(src[i:]) && top().mode != 'C' {
					top().codeline = true
				}
				j := i + 1 + len(id)
				if j < n && src[j] == '(' {
					stack = append(stack, frame{mode: 'C'})
					j++
				} else if j < n && src[j] == '[' {
					stack = append(stack, frame{mode: 'B'})
					j++
				}
				i = j
				continue
			}
		}
		i++
	}
	return nil
}

var (
	typstIdent    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)*`)
	typstCodeLine = regexp.MustCompile(`^#(let|set|show|import|include|if|else|for|while|context)\b`)
)

// actionEnd returns the index of the closing "}}" of the action opened at i.
func actionEnd(src string, i int) int {
	quote := byte(0)
	for j := i + 2; j < len(src); j++ {
		c := src[j]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				j++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '`':
			quote = c
		case c == '}' && j+1 < len(src) && src[j+1] == '}':
			return j
		}
	}
	return len(src)
}

var (
	controlWord = regexp.MustCompile(`^(if|else|end|range|with|define|break|continue)\b`)
	assignment  = regexp.MustCompile(`^\$[A-Za-z_]\w*(\s*,\s*\$[A-Za-z_]\w*)?\s*:?=`)
)

// producesOutput reports whether an action prints a value. `template` and `block`
// print (the output of the sub-template), so they follow the same context rules
// as `{{ .x }}`; template_check.py counts them as control words (no output).
func producesOutput(body string) bool {
	if strings.HasPrefix(body, "/*") {
		return false
	}
	return !controlWord.MatchString(body) && !assignment.MatchString(body)
}

// afterRe matches what Typst would read as the continuation of `#__v.at(N)`.
var afterRe = regexp.MustCompile(`^(?:\(|\[|\.[A-Za-z_])`)

var (
	actionBlanks = " \t\r\n"
	commentRe    = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	gluedRe      = regexp.MustCompile(`#(?:[A-Za-z_][A-Za-z0-9_.-]*)?$`)
)

func trailingBackslashes(s string) int {
	return len(s) - len(strings.TrimRight(s, "\\"))
}

// gluedToHash reports whether the text just before an action ends with an
// unescaped `#` or `#ident`.
func gluedToHash(before string) bool {
	loc := gluedRe.FindStringIndex(before)
	return loc != nil && (loc[0] == 0 || before[loc[0]-1] != '\\')
}

// applyTrim reproduces the whitespace trimming of text/template: `{{- ` removes
// the blanks before the action, ` -}}` those after it (same as template_check.py).
func applyTrim(text string) string {
	var out strings.Builder
	pos, trimNext := 0, false
	for i := 0; i < len(text); {
		k := strings.Index(text[i:], "{{")
		if k < 0 {
			break
		}
		start := i + k
		end := actionEnd(text, start)
		if end >= len(text) {
			break
		}
		raw := text[start : end+2]
		gap := text[pos:start]
		if trimNext {
			gap = strings.TrimLeft(gap, actionBlanks)
		}
		if strings.HasPrefix(raw, "{{-") && len(raw) > 3 && strings.IndexByte(actionBlanks, raw[3]) >= 0 {
			gap = strings.TrimRight(gap, actionBlanks)
		}
		out.WriteString(gap)
		out.WriteString(raw)
		trimNext = strings.HasSuffix(raw, "-}}") && len(raw) > 4 && strings.IndexByte(actionBlanks, raw[len(raw)-4]) >= 0
		pos = end + 2
		i = pos
	}
	tail := text[pos:]
	if trimNext {
		tail = strings.TrimLeft(tail, actionBlanks)
	}
	out.WriteString(tail)
	return out.String()
}

func stripCommentActions(text string) string { return commentRe.ReplaceAllString(text, "") }

// actionTable describes every action of the (trimmed) text: position, whether it
// prints, its control kind, and the structure of if/range/with blocks, to find
// what can follow an action.
type actionTable struct {
	starts, ends []int
	output       []bool
	kinds        []string
	index        map[int]int
	openerOf     map[int]int
	elseOf       map[int]int
	endOf        map[int]int
	text         string
}

var controlKinds = map[string]bool{"if": true, "range": true, "with": true, "else": true, "end": true, "define": true, "block": true, "template": true, "break": true, "continue": true}

func newActionTable(text string) *actionTable {
	t := &actionTable{index: map[int]int{}, openerOf: map[int]int{}, elseOf: map[int]int{}, endOf: map[int]int{}, text: text}
	for i := 0; i < len(text); {
		k := strings.Index(text[i:], "{{")
		if k < 0 {
			break
		}
		start := i + k
		end := actionEnd(text, start)
		stop := end
		if end < len(text) {
			stop = end + 2
		}
		body := strings.TrimSpace(strings.Trim(text[start+2:end], "-"))
		t.index[start] = len(t.starts)
		t.starts = append(t.starts, start)
		t.ends = append(t.ends, stop)
		t.output = append(t.output, producesOutput(body))
		kind := ""
		if m := firstWordRe.FindStringSubmatch(body); m != nil && controlKinds[m[1]] {
			kind = m[1]
		}
		t.kinds = append(t.kinds, kind)
		i = stop
	}
	var pending []int
	for k, kind := range t.kinds {
		switch kind {
		case "if", "range", "with", "define", "block":
			pending = append(pending, k)
		case "else":
			if len(pending) > 0 {
				t.openerOf[k] = pending[len(pending)-1]
				t.elseOf[pending[len(pending)-1]] = k
			}
		case "end":
			if len(pending) > 0 {
				opener := pending[len(pending)-1]
				pending = pending[:len(pending)-1]
				t.openerOf[k] = opener
				t.endOf[opener] = k
				if e, ok := t.elseOf[opener]; ok {
					t.endOf[e] = k
				}
			}
		}
	}
	return t
}

var firstWordRe = regexp.MustCompile(`^\s*([A-Za-z]+)`)

// before returns the text from the last newline up to the action at position
// pos, with the actions that print nothing removed.
func (t *actionTable) before(text string, pos int) string {
	lineStart := strings.LastIndexByte(text[:pos], '\n') + 1
	var b strings.Builder
	cur := lineStart
	for k, st := range t.starts {
		if st < lineStart || st >= pos {
			continue
		}
		b.WriteString(text[cur:st])
		if t.output[k] {
			b.WriteString(text[st:t.ends[k]])
		}
		cur = t.ends[k]
	}
	b.WriteString(text[cur:pos])
	return b.String()
}

// gap is the text between the end of action k and the next action.
func (t *actionTable) gap(k int) string {
	next := len(t.text)
	if k+1 < len(t.starts) {
		next = t.starts[k+1]
	}
	return t.text[t.ends[k]:next]
}

// followingLiterals returns the first literal texts that can follow the action at
// position pos, skipping the actions that print nothing in every possible branch
// (if, else, end, range...), as template_check.py does.
func (t *actionTable) followingLiterals(pos int) []string {
	var out []string
	seen := map[int]bool{}
	var visit func(i int)
	visit = func(i int) {
		if seen[i] {
			return
		}
		seen[i] = true
		// The end of a define is not followed inline by the text after it: a define
		// prints nothing at its own position, its body is a separate template.
		if t.kinds[i] == "end" {
			if o, ok := t.openerOf[i]; ok && t.kinds[o] == "define" {
				return
			}
		}
		if g := t.gap(i); g != "" {
			out = append(out, g)
			return
		}
		j := i + 1
		if j >= len(t.starts) || t.output[j] {
			return
		}
		visit(j)
		switch kind := t.kinds[j]; {
		case kind == "if" || kind == "with" || kind == "range":
			alt, ok := t.elseOf[j]
			if !ok {
				alt, ok = t.endOf[j]
			}
			if ok {
				visit(alt)
			}
		case kind == "else":
			if e, ok := t.endOf[j]; ok {
				visit(e)
			}
		case kind == "end":
			if o, ok := t.openerOf[j]; ok && t.kinds[o] == "range" {
				visit(o)
			}
		}
	}
	visit(t.index[pos])
	return out
}

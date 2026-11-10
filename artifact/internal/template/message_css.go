package template

// Small CSS reading helpers of the message profile: references to URLs (url(),
// image-set(), @import) and rules that hide an element. Comments are removed and
// escapes (\72 , \u) decoded before anything is looked for, so an escaped
// "u\72l(" is the function url(.

import (
	"regexp"
	"strings"
)

var cssComment = regexp.MustCompile(`(?s)/\*.*?(?:\*/|$)`)

func stripCSSComments(s string) string { return cssComment.ReplaceAllString(s, "") }

func isHexDigit(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}

func hexVal(r rune) int {
	switch {
	case r >= 'a':
		return int(r-'a') + 10
	case r >= 'A':
		return int(r-'A') + 10
	}
	return int(r - '0')
}

// cssUnescape decodes CSS escapes: "\" and up to 6 hexadecimal digits followed
// by one optional white space, or "\" and any other character (a backslash
// before a newline is a line continuation and disappears).
func cssUnescape(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] != '\\' {
			b.WriteRune(rs[i])
			i++
			continue
		}
		i++
		if i >= len(rs) {
			b.WriteRune(0xFFFD)
			break
		}
		switch {
		case isHexDigit(rs[i]):
			v := 0
			for n := 0; n < 6 && i < len(rs) && isHexDigit(rs[i]); n++ {
				v = v*16 + hexVal(rs[i])
				i++
			}
			if i+1 < len(rs) && rs[i] == '\r' && rs[i+1] == '\n' {
				i += 2
			} else if i < len(rs) && strings.ContainsRune(" \t\n\r\f", rs[i]) {
				i++
			}
			if v == 0 || v > 0x10FFFF || v >= 0xD800 && v <= 0xDFFF {
				v = 0xFFFD
			}
			b.WriteRune(rune(v))
		case rs[i] == '\n' || rs[i] == '\r' || rs[i] == '\f':
			i++
		default:
			b.WriteRune(rs[i])
			i++
		}
	}
	return b.String()
}

var (
	cssURLFn    = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s]*))\s*\)`)
	cssImport   = regexp.MustCompile(`(?i)@import\s*(?:"([^"]*)"|'([^']*)')`)
	cssString   = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)
	cssImageSet = regexp.MustCompile(`(?i)image-set\(`)
)

// cssRefs lists every URL a piece of CSS refers to: url(), @import and the
// strings of image-set().
func cssRefs(css string) []string {
	css = cssUnescape(stripCSSComments(css))
	var refs []string
	for _, m := range cssURLFn.FindAllStringSubmatch(css, -1) {
		refs = append(refs, m[1]+m[2]+m[3])
	}
	for _, m := range cssImport.FindAllStringSubmatch(css, -1) {
		refs = append(refs, m[1]+m[2])
	}
	for _, loc := range cssImageSet.FindAllStringIndex(css, -1) {
		depth, end := 1, len(css)
		for j := loc[1]; j < len(css); j++ {
			if css[j] == '(' {
				depth++
			} else if css[j] == ')' {
				if depth--; depth == 0 {
					end = j
					break
				}
			}
		}
		for _, m := range cssString.FindAllStringSubmatch(css[loc[1]:end], -1) {
			refs = append(refs, m[1]+m[2])
		}
	}
	return refs
}

// ---- rules that hide an element ----

type cssBlock struct {
	prelude, body string
	block         bool
}

// skipCSS returns the index after the token at i: an escape, a string, or one byte.
func skipCSS(s string, i int) int {
	switch s[i] {
	case '\\':
		return min(i+2, len(s))
	case '"', '\'':
		q := s[i]
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\\' {
				j++
			} else if s[j] == q {
				return j + 1
			}
		}
		return len(s)
	}
	return i + 1
}

// scanCSS splits a stylesheet (comments already removed) into top-level
// statements and blocks; the braces of strings and escapes do not count.
func scanCSS(s string) []cssBlock {
	var out []cssBlock
	for i := 0; i < len(s); {
		start := i
		for i < len(s) && s[i] != '{' && s[i] != ';' && s[i] != '}' {
			i = skipCSS(s, i)
		}
		prelude := strings.TrimSpace(s[start:i])
		if i >= len(s) {
			if prelude != "" {
				out = append(out, cssBlock{prelude: prelude})
			}
			break
		}
		switch s[i] {
		case '{':
			depth, j := 1, i+1
			for j < len(s) && depth > 0 {
				switch s[j] {
				case '{':
					depth++
				case '}':
					depth--
				}
				if depth > 0 {
					j = skipCSS(s, j)
				}
			}
			out = append(out, cssBlock{prelude: prelude, body: s[i+1 : min(j, len(s))], block: true})
			i = j + 1
		default:
			if prelude != "" {
				out = append(out, cssBlock{prelude: prelude})
			}
			i++
		}
	}
	return out
}

// hideRule is a rule with a simple selector: an element, classes, an id.
type hideRule struct {
	tag, id string
	classes []string
}

func (r hideRule) matches(tag string, attrs map[string]string) bool {
	if r.tag != "" && r.tag != tag {
		return false
	}
	if r.id != "" && attrs["id"] != r.id {
		return false
	}
	have := strings.Fields(attrs["class"])
	for _, c := range r.classes {
		found := false
		for _, h := range have {
			found = found || h == c
		}
		if !found {
			return false
		}
	}
	return true
}

var simpleSelector = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9-]*)?((?:[.#][a-zA-Z0-9_-]+)*)$`)
var selectorPart = regexp.MustCompile(`[.#][a-zA-Z0-9_-]+`)

func parseSimpleSelector(sel string) (hideRule, bool) {
	sel = strings.TrimSpace(sel)
	m := simpleSelector.FindStringSubmatch(sel)
	if m == nil || sel == "" {
		return hideRule{}, false
	}
	r := hideRule{tag: strings.ToLower(m[1])}
	for _, p := range selectorPart.FindAllString(m[2], -1) {
		if p[0] == '#' {
			if r.id != "" {
				return hideRule{}, false
			}
			r.id = p[1:]
		} else {
			r.classes = append(r.classes, p[1:])
		}
	}
	return r, true
}

// splitCSS splits at every sep that is neither escaped nor inside a string.
func splitCSS(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
			i++
			continue
		}
		i = skipCSS(s, i)
	}
	return append(out, s[start:])
}

// cutCSS cuts at the first sep that is neither escaped nor inside a string.
func cutCSS(s string, sep byte) (string, string, bool) {
	for i := 0; i < len(s); {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
		i = skipCSS(s, i)
	}
	return s, "", false
}

// declsHide applies the inline-style hiding test to a declaration block (the
// same reading as a style attribute: comments removed, structure split, then
// escapes decoded).
func declsHide(decls string) bool {
	return hiddenElement("", map[string]string{"style": decls}, nil)
}

// nestedHides reports whether anything inside a block hides an element, whatever
// the selector (used for blocks this reader does not evaluate).
func nestedHides(body string) bool {
	for _, b := range scanCSS(body) {
		switch {
		case !b.block && declsHide(b.prelude):
			return true
		case b.block && (strings.Contains(b.body, "{") && nestedHides(b.body) || declsHide(b.body)):
			return true
		}
	}
	return false
}

var skippedAtRules = map[string]bool{"@media": true, "@font-face": true, "@keyframes": true, "@-webkit-keyframes": true, "@page": true, "@charset": true, "@namespace": true}

// cssStyleRules reads the rules of a stylesheet outside @media whose selector is
// simple (element, .class, #id, and lists of them separated by commas) and that
// set a property able to hide an element (display: block counts: it can undo a
// hiding rule, so the cascade needs it). order numbers the rules of the document.
// A hiding rule whose selector is not evaluated (descendant, attribute,
// pseudo-class, nesting, other at-rule) is an error: the caller refuses the text
// version rather than risk a leak.
func cssStyleRules(css string, order *int) ([]styleRule, error) {
	var rules []styleRule
	for _, b := range scanCSS(stripCSSComments(css)) {
		if !b.block {
			continue
		}
		if strings.HasPrefix(b.prelude, "@") {
			name := strings.ToLower(strings.Fields(b.prelude + " ")[0])
			if !skippedAtRules[name] && nestedHides(b.body) {
				return nil, errf(CodeMessageHiddenRule, "a stylesheet rule %q hides content and is not evaluated", short40(b.prelude))
			}
			continue
		}
		if strings.Contains(b.body, "{") {
			if nestedHides(b.body) || declsHide(b.body) {
				return nil, errf(CodeMessageHiddenRule, "a nested stylesheet rule under %q hides content and is not evaluated", short40(b.prelude))
			}
			continue
		}
		*order++
		var decls []cssDecl
		for _, d := range parseDecls(b.body) {
			if cascadeProps[d.prop] {
				decls = append(decls, d)
			}
		}
		if len(decls) == 0 {
			continue
		}
		hides := declsHide(b.body)
		for _, sel := range strings.Split(cssUnescape(b.prelude), ",") {
			r, ok := parseSimpleSelector(sel)
			if !ok {
				if hides {
					return nil, errf(CodeMessageHiddenRule, "the selector %q hides content and is not evaluated (use a class, an element or an id)", short40(strings.TrimSpace(sel)))
				}
				continue
			}
			rules = append(rules, styleRule{sel: r, decls: decls, order: *order})
		}
	}
	return rules, nil
}

// ---- cascade of the properties that hide an element ----

// cssDecl is one declaration, decoded: comments removed, structure split, then
// escapes decoded; the name and the value are lower case, "!important" is a flag.
type cssDecl struct {
	prop, val string
	important bool
}

// cascadeProps are the properties the text version looks at.
var cascadeProps = setOf("display", "visibility", "mso-hide", "opacity", "max-height", "font-size", "line-height", "overflow", "overflow-y")

func parseDecls(style string) []cssDecl {
	var out []cssDecl
	for _, d := range splitCSS(stripCSSComments(style), ';') {
		k, v, ok := cutCSS(d, ':')
		if !ok {
			continue
		}
		v = strings.ToLower(strings.TrimSpace(cssUnescape(v)))
		imp := importantFlag.MatchString(v)
		out = append(out, cssDecl{
			prop:      strings.TrimSpace(strings.ToLower(cssUnescape(k))),
			val:       strings.TrimSpace(importantFlag.ReplaceAllString(v, "")),
			important: imp,
		})
	}
	return out
}

// styleRule is a rule with a simple selector and its declarations on the
// tracked properties; order is its place in the document.
type styleRule struct {
	sel   hideRule
	decls []cssDecl
	order int
}

// specificity of a simple selector: ids, classes, element (each below 100).
func (r hideRule) specificity() int {
	n := len(r.classes) * 10
	if r.id != "" {
		n += 10000
	}
	if r.tag != "" {
		n++
	}
	return n
}

// candidate is a declaration competing for one property of one element.
type candidate struct {
	val       string
	important bool
	inline    bool
	spec      int
	order     int
}

// beats follows the CSS cascade reduced to this reader: importance, then origin
// (inline above sheet at equal importance), then specificity, then the last.
func (a candidate) beats(b candidate) bool {
	switch {
	case a.important != b.important:
		return a.important
	case a.inline != b.inline:
		return a.inline
	case a.spec != b.spec:
		return a.spec > b.spec
	}
	return a.order >= b.order
}

// hiddenElement reports whether an element is hidden from the reader: the
// hidden attribute, or winning declarations (cascade: importance, inline over
// sheet, specificity, order) that make a hiding pattern.
func hiddenElement(tag string, attrs map[string]string, rules []styleRule) bool {
	if _, ok := attrs["hidden"]; ok {
		return true
	}
	win := map[string]candidate{}
	offer := func(prop string, c candidate) {
		if old, ok := win[prop]; !ok || c.beats(old) {
			win[prop] = c
		}
	}
	for _, r := range rules {
		if !r.sel.matches(tag, attrs) {
			continue
		}
		for i, d := range r.decls {
			offer(d.prop, candidate{d.val, d.important, false, r.sel.specificity(), r.order<<16 | i})
		}
	}
	for i, d := range parseDecls(attrs["style"]) {
		if cascadeProps[d.prop] {
			offer(d.prop, candidate{d.val, d.important, true, 0, i})
		}
	}
	v := func(prop string) string { return win[prop].val }
	switch {
	case v("display") == "none", v("visibility") == "hidden", v("visibility") == "collapse",
		v("mso-hide") == "all", v("opacity") != "" && cssZero(v("opacity")):
		return true
	}
	clipped := v("overflow") == "hidden" || v("overflow-y") == "hidden"
	zero := func(prop string) bool { return v(prop) != "" && cssZero(v(prop)) }
	return clipped && (zero("max-height") || zero("font-size") || zero("line-height"))
}

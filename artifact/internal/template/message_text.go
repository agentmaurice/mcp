package template

// Plain-text part of a message: a Go port of the minimal converter measured by
// the AGENT-1614 bench (html2text_min.py). MJML produces no text version, so the
// renderer derives it from the final HTML.
//
// Rules: head, style, script, title, comments (the MSO conditional comments
// included) and every element whose inline style hides it (display:none, the
// preheader) are ignored; a block element opens a line; the cells of a table row
// are written one per paragraph; <a href> becomes "label (url)"; <img alt>
// becomes "[alt]"; <li> becomes "- " or "1. "; entities are decoded; no-break
// spaces become spaces; invisible padding characters are removed. No wrapping.

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var (
	textVoid = setOf("area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track", "wbr")
	textSkip = setOf("head", "style", "script", "title", "template")
	textPara = setOf("p", "div", "table", "h1", "h2", "h3", "h4", "h5", "h6", "ol", "ul", "hr", "section", "article", "header", "footer")
	textLine = setOf("li", "br", "tr", "td", "th")

	importantFlag = regexp.MustCompile(`\s*!\s*important\s*$`)
	cssNumber     = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)`)
	blanks        = regexp.MustCompile(`[ \t\r\n\f]+`)
	hSpaces       = regexp.MustCompile(`[ \t]+`)
	manyLines     = regexp.MustCompile(`\n{3,}`)
)

// cssZero reports whether a CSS length or number is zero (0, 0px, 0.0em, 0%).
func cssZero(v string) bool {
	m := cssNumber.FindString(strings.TrimSpace(v))
	if m == "" {
		return false
	}
	f, err := strconv.ParseFloat(m, 64)
	return err == nil && f == 0
}

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

type textFrame struct {
	tag  string
	hide bool
}

type textLink struct {
	url   string
	start int
}

type textList struct {
	ordered bool
	n       int
}

type textConv struct {
	rules   []styleRule // stylesheet rules about the properties that hide an element
	buf     []byte
	hasText bool // buf holds something other than whitespace
	stack   []textFrame
	hidden  int // depth of ignored elements
	links   []textLink
	lists   []textList
}

func (c *textConv) put(s string) {
	c.buf = append(c.buf, s...)
	if !c.hasText && strings.TrimSpace(s) != "" {
		c.hasText = true
	}
}

// brk guarantees n line breaks at the end of the output (never a duplicate), and
// none while the output is still blank.
func (c *textConv) brk(n int) {
	if len(c.buf) > 0 && !c.hasText {
		return
	}
	have := 0
	for i := len(c.buf) - 1; i >= 0 && c.buf[i] == '\n'; i-- {
		have++
	}
	if n > have {
		c.put(strings.Repeat("\n", n-have))
	}
}

func (c *textConv) start(tag string, attrs map[string]string) {
	hideStyle := hiddenElement(tag, attrs, c.rules)
	if textVoid[tag] {
		if c.hidden > 0 || hideStyle {
			return
		}
		switch tag {
		case "br", "hr":
			c.brk(1)
		case "img":
			if alt := strings.TrimSpace(attrs["alt"]); alt != "" {
				c.brk(1)
				c.put("[" + alt + "]")
				c.brk(1)
			}
		}
		return
	}
	hide := textSkip[tag] || hideStyle
	c.stack = append(c.stack, textFrame{tag, hide})
	if hide {
		c.hidden++
	}
	if c.hidden > 0 {
		return
	}
	if textPara[tag] {
		c.brk(2)
	} else if textLine[tag] {
		c.brk(1)
	}
	switch tag {
	case "ol", "ul":
		c.lists = append(c.lists, textList{ordered: tag == "ol"})
	case "li":
		if n := len(c.lists); n > 0 && c.lists[n-1].ordered {
			c.lists[n-1].n++
			c.put(strconv.Itoa(c.lists[n-1].n) + ". ")
		} else {
			c.put("- ")
		}
	case "a":
		if href := attrs["href"]; href != "" {
			c.links = append(c.links, textLink{href, len(c.buf)})
		}
	}
}

func (c *textConv) end(tag string) {
	if textVoid[tag] {
		return
	}
	// An end tag with no open element is ignored; otherwise everything opened
	// after the matching element is closed with it.
	idx := -1
	for i := len(c.stack) - 1; i >= 0; i-- {
		if c.stack[i].tag == tag {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	for _, f := range c.stack[idx:] {
		if f.hide {
			c.hidden--
		}
	}
	c.stack = c.stack[:idx]
	if c.hidden > 0 {
		return
	}
	switch tag {
	case "a":
		if n := len(c.links); n > 0 {
			l := c.links[n-1]
			c.links = c.links[:n-1]
			start := min(l.start, len(c.buf))
			label := strings.TrimSpace(blanks.ReplaceAllString(string(c.buf[start:]), " "))
			if hasLinkScheme(l.url) && label != l.url {
				for len(c.buf) > 0 && strings.TrimSpace(string(c.buf[len(c.buf)-1:])) == "" {
					c.buf = c.buf[:len(c.buf)-1]
				}
				c.put(" (" + l.url + ")")
			}
		}
	case "ol", "ul":
		if n := len(c.lists); n > 0 {
			c.lists = c.lists[:n-1]
		}
	}
	if textPara[tag] {
		c.brk(2)
	} else if textLine[tag] {
		c.brk(1)
	}
}

func hasLinkScheme(u string) bool {
	for _, p := range []string{"http://", "https://", "mailto:", "tel:"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

func (c *textConv) data(s string) {
	if c.hidden > 0 {
		return
	}
	s = blanks.ReplaceAllString(s, " ")
	if strings.TrimSpace(s) == "" {
		if n := len(c.buf); n > 0 && c.buf[n-1] != '\n' && c.buf[n-1] != ' ' {
			c.put(" ")
		}
		return
	}
	c.put(s)
}

// invisible padding: zero-width space, non-joiner, joiner, word joiner, BOM,
// combining grapheme joiner (the "&#847;" of preheader padding), figure space and
// soft hyphen.
func dropInvisible(r rune) rune {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF, 0x034F, 0x2007, 0x00AD:
		return -1
	}
	return r
}

// styleRules reads the <style> blocks of the document (those that apply on
// screen) and returns the rules that hide an element.
func styleRules(src string) ([]styleRule, error) {
	var rules []styleRule
	order := 0
	z := html.NewTokenizer(strings.NewReader(src))
	inStyle, skip := false, false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return rules, nil
		}
		switch tt {
		case html.StartTagToken:
			tok := z.Token()
			inStyle, skip = tok.Data == "style", false
			for _, a := range tok.Attr {
				if m := strings.ToLower(strings.TrimSpace(a.Val)); a.Key == "media" && m != "" && m != "all" && m != "screen" {
					skip = true
				}
			}
		case html.EndTagToken:
			inStyle = false
		case html.TextToken:
			if inStyle && !skip {
				r, err := cssStyleRules(string(z.Text()), &order)
				if err != nil {
					return nil, err
				}
				rules = append(rules, r...)
			}
		}
	}
}

// HTMLToText derives the plain-text version of an HTML message. It fails
// (message_hidden_rule_unsupported) when a stylesheet rule hides content through
// a selector it does not evaluate, rather than risk leaking hidden text.
func HTMLToText(src string) (string, error) {
	rules, err := styleRules(src)
	if err != nil {
		return "", err
	}
	c := &textConv{rules: rules}
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.TextToken:
			c.data(string(z.Text()))
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			attrs := make(map[string]string, len(tok.Attr))
			for _, a := range tok.Attr {
				if _, dup := attrs[a.Key]; !dup {
					attrs[a.Key] = a.Val
				}
			}
			c.start(tok.Data, attrs)
			if tt == html.SelfClosingTagToken {
				c.end(tok.Data)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			c.end(string(name))
		}
	}
	text := strings.Map(dropInvisible, string(c.buf))
	text = strings.NewReplacer(" ", " ", " ", " ").Replace(text)
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimSpace(hSpaces.ReplaceAllString(ln, " "))
	}
	text = manyLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.Trim(text, "\n") + "\n", nil
}

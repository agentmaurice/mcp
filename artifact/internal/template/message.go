package template

// Message profile (AGENT-1614 lot 3): the mjml engine. The template and its
// fragments are executed by Go (html/template escaping), written to a working
// directory, compiled by MJML in a sub-process, then the HTML is checked before
// it is returned as html, txt or eml. Nothing is ever sent.

import (
	"context"
	"html"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
)

// MJMLVersion is the only MJML version this renderer accepts (bench AGENT-1614).
const MJMLVersion = "5.4.1"

// MaxMessageHTMLBytes is the weight the HTML must stay under (Gmail clips a
// message at 102 KiB).
const MaxMessageHTMLBytes = 102 * 1024

const messageFragmentDir = "fragments/message"

// MJMLRequest asks the MJML engine to compile the resolved project.
type MJMLRequest struct {
	Dir            string // working directory: the only place mj-include may read
	Entry          string // resolved entry, relative to Dir
	MaxOutputBytes int    // the HTML is refused above this size
}

// MJMLCompiler is the MJML engine. Compile returns the HTML.
type MJMLCompiler interface {
	Version(ctx context.Context) (string, error)
	Compile(ctx context.Context, req MJMLRequest) ([]byte, error)
}

var includeTag = regexp.MustCompile(`(?is)<mj-include\b([^>]*)>`)
var includePath = regexp.MustCompile(`(?is)\bpath\s*=\s*(?:"([^"]*)"|'([^']*)')`)
var fragmentInclude = regexp.MustCompile(`^fragments/message/[A-Za-z0-9_-][A-Za-z0-9_.-]*\.mjml$`)

// checkIncludes refuses, before MJML runs, every mj-include that does not target
// a declared fragment of fragments/message (contract rule 15, enforced again at render).
func checkIncludes(name, src string, declared []string) error {
	for _, m := range includeTag.FindAllStringSubmatch(src, -1) {
		pm := includePath.FindStringSubmatch(m[1])
		p := ""
		if pm != nil {
			p = pm[1] + pm[2]
		}
		if pm == nil || !fragmentInclude.MatchString(p) || strings.Contains(p, "..") {
			return errf(CodeMJMLIncludeOutside, "%s: mj-include must target fragments/message/*.mjml, got %q", name, p)
		}
		if !contains(declared, p) {
			return errf(CodeFragmentUndeclared, "fragment %q is used but not declared in manifest.fragments", p)
		}
	}
	return nil
}

// markedString is a data or context string seen by the residue pass: printing it
// (fmt, html/template) gives the value between two private-use markers, while
// comparisons and tests (eq, if, default, len...) see the plain value, so the
// pass takes exactly the branches of the real render.
type markedString string

const (
	markStart = "\uE000"
	markEnd   = "\uE001"
)

func (s markedString) String() string { return markStart + string(s) + markEnd }

// markValues returns a copy of a context value with every string marked.
func markValues(v any) any {
	switch x := v.(type) {
	case string:
		return markedString(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = markValues(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = markValues(e)
		}
		return out
	}
	return v
}

// hasReservedChar reports whether a context value holds a marker character.
func hasReservedChar(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.ContainsAny(x, markStart+markEnd)
	case []any:
		for _, e := range x {
			if hasReservedChar(e) {
				return true
			}
		}
	case map[string]any:
		for k, e := range x {
			if strings.ContainsAny(k, markStart+markEnd) || hasReservedChar(e) {
				return true
			}
		}
	}
	return false
}

var (
	markedZone        = regexp.MustCompile(`(?s)\x{E000}.*?\x{E001}`)
	markedZoneEncoded = regexp.MustCompile(`(?is)%EE%80%80.*?%EE%80%81`)
	markerLeft        = regexp.MustCompile(`(?i)[\x{E000}\x{E001}]|%EE%80%8[01]`)
)

// checkResidue refuses template braces that the template itself writes. The
// entry and the fragments are executed a second time against the very same
// data, with every string marked (markedString): the same branches are taken,
// and what a value prints sits between two private-use markers (raw, or
// percent-encoded where an escaper put it in a URL). The marked zones are
// removed from that resolved source; any brace left was written by the template
// or a fragment, whatever the data hold. Data that hold a marker character are
// refused (message_reserved_character).
func checkResidue(ctx context.Context, full map[string]any, maxOut int, sources map[string]string) error {
	marked := map[string]any{}
	for k, v := range full {
		if hasReservedChar(v) {
			return errf(CodeMessageReservedChar, "the data or the context hold U+E000 or U+E001, private-use characters reserved to the renderer")
		}
		marked[k] = markValues(v)
	}
	ex, err := newExecutor(ctx, EngineMJML, marked, maxOut)
	if err != nil {
		return err
	}
	for _, name := range sortedKeys(sources) {
		out, err := ex.render(sources[name])
		if err != nil {
			return execErr("residue check of "+name, err)
		}
		out = markedZone.ReplaceAllString(markedZoneEncoded.ReplaceAllString(out, ""), "")
		if markerLeft.MatchString(out) {
			return errf(CodeMessageTemplateResidue, "%s: a value marker was split by the template (unbalanced)", name)
		}
		text := html.UnescapeString(out)
		for _, brace := range []string{"{{", "}}"} {
			if i := strings.Index(text, brace); i >= 0 {
				return errf(CodeMessageTemplateResidue, "%s writes a template brace (not a data value) near %q", name, snippet(text, i))
			}
		}
	}
	return nil
}

func snippet(s string, i int) string {
	a, b := max(0, i-20), min(len(s), i+20)
	return strings.ReplaceAll(s[a:b], "\n", " ")
}

func short40(u string) string {
	r := []rune(u)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return u
}

func absoluteHTTPS(raw string) bool {
	if !strings.HasPrefix(raw, "https://") || strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

var (
	commentAttr = regexp.MustCompile(`(?is)\b(href|src|srcset|background)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+))`)
)

// checkURLs refuses any href, src or background (attribute, CSS url() or
// conditional-comment markup) that is not an absolute https URL.
func checkURLs(doc string) error {
	bad := func(where, raw string) error {
		return errf(CodeMessageURLNotHTTPS, "%s is not an absolute https URL: %q", where, short40(raw))
	}
	cssCheck := func(where, css string) error {
		for _, raw := range cssRefs(css) {
			if !absoluteHTTPS(html.UnescapeString(raw)) {
				return bad(where+" url", raw)
			}
		}
		return nil
	}
	z := xhtml.NewTokenizer(strings.NewReader(doc))
	inStyle := false
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			return nil
		}
		switch tt {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tok := z.Token()
			inStyle = tok.Data == "style" && tt == xhtml.StartTagToken
			for _, a := range tok.Attr {
				switch a.Key {
				case "href", "src", "srcset", "background":
					// srcset is refused whatever its value (contract rule 16: use src)
					if a.Key == "srcset" || !absoluteHTTPS(a.Val) {
						return bad(a.Key+" of <"+tok.Data+">", a.Val)
					}
				case "style":
					if err := cssCheck("style attribute", a.Val); err != nil {
						return err
					}
				}
			}
		case xhtml.EndTagToken:
			inStyle = false
		case xhtml.TextToken:
			if inStyle {
				if err := cssCheck("<style>", string(z.Text())); err != nil {
					return err
				}
			}
		case xhtml.CommentToken:
			c := string(z.Text())
			for _, m := range commentAttr.FindAllStringSubmatch(c, -1) {
				if raw := html.UnescapeString(m[2] + m[3] + m[4]); m[1] == "srcset" || !absoluteHTTPS(raw) {
					return bad(m[1]+" in a conditional comment", raw)
				}
			}
			if err := cssCheck("conditional comment", html.UnescapeString(c)); err != nil {
				return err
			}
		}
	}
}

// urlKey splits an absolute URL into scheme, host (lower case, default port
// removed) and the rest (path, query, fragment: kept byte for byte).
func urlKey(raw string) (string, string, string, bool) {
	scheme, after, ok := strings.Cut(raw, "://")
	if !ok {
		return "", "", "", false
	}
	scheme = strings.ToLower(scheme)
	i := strings.IndexAny(after, "/?#")
	if i < 0 {
		i = len(after)
	}
	host := strings.ToLower(after[:i])
	if scheme == "https" {
		host = strings.TrimSuffix(host, ":443")
	}
	return scheme, host, after[i:], true
}

// sameURL compares two URLs without decoding them: scheme and host are case
// insensitive, the default port is ignored, the rest is compared exactly (so
// ".../a%2Fb" and ".../a/b" differ).
func sameURL(a, b string) bool {
	as, ah, ar, ok1 := urlKey(a)
	bs, bh, br, ok2 := urlKey(b)
	return ok1 && ok2 && as == bs && ah == bh && ar == br
}

// hasLink reports whether the document holds an <a href> equal to want (the
// attribute is already unescaped by the HTML tokenizer).
func hasLink(doc, want string) bool {
	z := xhtml.NewTokenizer(strings.NewReader(doc))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			return false
		}
		if tt != xhtml.StartTagToken && tt != xhtml.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		if tok.Data != "a" {
			continue
		}
		for _, a := range tok.Attr {
			if a.Key == "href" && sameURL(a.Val, want) {
				return true
			}
		}
	}
}

// CheckMessageHTML runs the output checks of the message profile on the HTML
// produced by MJML. category and data drive the unsubscribe check. (Template
// braces are checked before MJML, see checkResidue.)
func CheckMessageHTML(doc string, category string, data any) error {
	if strings.TrimSpace(doc) == "" || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(doc)), "<!doctype html") {
		return errf(CodeMessageOutputInvalid, "MJML produced no HTML document")
	}
	if !strings.Contains(strings.ToLower(doc), "<body") {
		return errf(CodeMessageOutputInvalid, "the HTML document has no body")
	}
	if n := len(doc); n >= MaxMessageHTMLBytes {
		return errf(CodeMessageTooHeavy, "the HTML is %d bytes, not under %d bytes (Gmail clips above 102 KiB)", n, MaxMessageHTMLBytes)
	}
	if n := strings.Count(doc, "mj-include denied"); n > 0 {
		return errf(CodeMJMLIncludeDenied, "MJML refused %d mj-include (outside the working directory or missing file)", n)
	}
	if err := checkURLs(doc); err != nil {
		return err
	}
	if category == "marketing" {
		m, _ := data.(map[string]any)
		want, _ := m["unsubscribe_url"].(string)
		if want == "" || !hasLink(doc, want) {
			return errf(CodeMessageUnsubscribeMiss, "marketing message: the rendered HTML holds no link to data.unsubscribe_url")
		}
	}
	return nil
}

// sourcesOf lists the unexecuted sources of the render: the entry and the declared fragments.
func sourcesOf(p *Prepared) map[string]string {
	src := map[string]string{p.Entry: p.Files[p.Entry]}
	for name, body := range p.Fragments {
		src[name] = body
	}
	return src
}

func renderMessage(ctx context.Context, req Request, p *Prepared, entryName, rendered string, fragments map[string]string,
	full map[string]any, opts Options, res *Result) (*Result, error) {
	if opts.MJML == nil {
		return nil, errf(CodeEngineUnavailable, "no MJML engine is configured (set MJML_NODE_BIN and MJML_DIR)")
	}
	files := map[string]string{p.Entry: rendered}
	for name, body := range fragments {
		files[name] = body
	}
	for _, name := range sortedKeys(files) {
		if err := checkIncludes(name, files[name], p.Manifest.Fragments); err != nil {
			return nil, err
		}
	}
	maxOut := opts.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = DefaultMaxOutputBytes
	}
	if err := checkResidue(ctx, full, maxOut, sourcesOf(p)); err != nil {
		return nil, err
	}
	got, err := opts.MJML.Version(ctx)
	if err != nil {
		return nil, asEngineError(ctx, err)
	}
	if got != MJMLVersion {
		return nil, errf(CodeEngineVersion, "MJML %s is required, found %s", MJMLVersion, got)
	}
	dir, err := os.MkdirTemp("", "artifact-mjml-")
	if err != nil {
		return nil, errf(CodeMJMLFailed, "temporary directory: %v", err)
	}
	defer os.RemoveAll(dir)
	raw := map[string][]byte{}
	for _, name := range sortedKeys(files) {
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, errf(CodeMJMLFailed, "writing %s: %v", name, err)
		}
		if err := os.WriteFile(target, []byte(files[name]), 0o644); err != nil {
			return nil, errf(CodeMJMLFailed, "writing %s: %v", name, err)
		}
		raw[name] = []byte(files[name])
	}
	res.Provenance.SourceSHA256 = sourceDigest(raw, nil)
	res.Provenance.Engine.Name, res.Provenance.Engine.Version = "mjml", got

	out, err := opts.MJML.Compile(ctx, MJMLRequest{Dir: dir, Entry: p.Entry, MaxOutputBytes: maxOut})
	if err != nil {
		return nil, asEngineError(ctx, err)
	}
	doc := string(out)
	if err := CheckMessageHTML(doc, p.Manifest.Message.Category, req.Data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(path.Base(entryName), path.Ext(entryName))
	switch req.Output {
	case "html":
		res.Content = out
		res.Filename, res.MimeType = base+".html", "text/html; charset=utf-8"
	case "txt":
		text, err := HTMLToText(doc)
		if err != nil {
			return nil, err
		}
		res.Content = []byte(text)
		res.Filename, res.MimeType = base+".txt", "text/plain; charset=utf-8"
	case "eml":
		subject := ""
		if m, ok := req.Data.(map[string]any); ok {
			subject, _ = m["subject"].(string)
		}
		text, err := HTMLToText(doc)
		if err != nil {
			return nil, err
		}
		eml, err := BuildEML(subject, text, doc)
		if err != nil {
			return nil, err
		}
		res.Content = eml
		res.Filename, res.MimeType = base+".eml", "message/rfc822"
	}
	return res, nil
}

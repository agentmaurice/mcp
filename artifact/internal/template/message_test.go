package template_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/mjml"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/mjml/mjmltest"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template/templatetest"
)

func renderMsg(t *testing.T, c template.MJMLCompiler, tpl template.Template, data any, output string) (*template.Result, error) {
	t.Helper()
	ctx, assets := templatetest.Context(t)
	return template.Render(context.Background(), template.Request{
		Template: tpl, Data: data, Context: ctx, Assets: assets, Output: output, Mode: template.ModeFinal,
	}, nil, template.Options{Timeout: 120 * time.Second, MJML: c})
}

func mustMsg(t *testing.T, c template.MJMLCompiler, tpl template.Template, data any, output string) *template.Result {
	t.Helper()
	res, err := renderMsg(t, c, tpl, data, output)
	if err != nil {
		t.Fatalf("render %s: %v", output, err)
	}
	return res
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var terr *template.Error
	if !errors.As(err, &terr) || terr.Code != code {
		t.Fatalf("want error code %q, got %v", code, err)
	}
}

// edit returns a copy of the support-reply golden with files and the manifest changed.
func edit(t *testing.T, files map[string]func(string) string, manifest func(map[string]any)) template.Template {
	t.Helper()
	tpl := templatetest.Load(t, "valid/support-reply")
	out := template.Template{Manifest: tpl.Manifest, Files: map[string]string{}}
	for k, v := range tpl.Files {
		out.Files[k] = v
	}
	for name, f := range files {
		before := out.Files[name]
		out.Files[name] = f(before)
		if out.Files[name] == before {
			t.Fatalf("edit of %s changed nothing", name)
		}
	}
	if manifest != nil {
		var m map[string]any
		if err := json.Unmarshal(tpl.Manifest, &m); err != nil {
			t.Fatal(err)
		}
		manifest(m)
		out.Manifest, _ = json.Marshal(m)
	}
	return out
}

func replace(old, new string) func(string) string {
	return func(s string) string { return strings.Replace(s, old, new, 1) }
}

func dataOf(t *testing.T, tpl template.Template) map[string]any {
	t.Helper()
	return templatetest.Data(t, tpl, "default").(map[string]any)
}

func TestMessageGoldenRendersHTMLTextAndEML(t *testing.T) {
	c := mjmltest.Runner(t)
	tpl := templatetest.Load(t, "valid/support-reply")
	data := templatetest.Data(t, tpl, "default")
	html := mustMsg(t, c, tpl, data, "html")
	txt := mustMsg(t, c, tpl, data, "txt")
	eml := mustMsg(t, c, tpl, data, "eml")
	t.Logf("support-reply: html %d bytes, txt %d bytes, eml %d bytes", len(html.Content), len(txt.Content), len(eml.Content))

	if html.Filename != "main.html" || txt.Filename != "main.txt" || eml.Filename != "main.eml" {
		t.Fatalf("filenames: %s %s %s", html.Filename, txt.Filename, eml.Filename)
	}
	if html.Provenance.Engine.Name != "mjml" || html.Provenance.Engine.Version != template.MJMLVersion {
		t.Fatalf("provenance engine: %+v", html.Provenance.Engine)
	}
	if n := len(html.Content); n == 0 || n >= template.MaxMessageHTMLBytes {
		t.Fatalf("html weight %d", n)
	}
	h := string(html.Content)
	for _, want := range []string{"<!doctype html>", "{{ 1&#43;1 }}", "&lt;b&gt;aucun calcul&lt;/b&gt;", "R&amp;D", "https://support.atelier-exemple.invalid/tickets/SUP-48213?utm_source=email&amp;utm_medium=reply"} {
		if !strings.Contains(h, want) {
			t.Errorf("html lacks %q", want)
		}
	}
	// text: no tag, no entity, no preheader, links as "label (url)", the literals kept
	text := string(txt.Content)
	for _, want := range []string{
		"Bonjour Camille Durand,", "{{ 1+1 }}", "<b>aucun calcul</b>", "R&D", "1 250,00 € HT",
		"1. Ouvrez la page « Exports » et cliquez sur « Relancer ».", "3. Téléchargez le nouveau fichier",
		"Suivre ce ticket (https://support.atelier-exemple.invalid/tickets/SUP-48213?utm_source=email&utm_medium=reply)",
		"Se désinscrire (https://app.atelier-exemple.invalid/preferences?u=c4f9&list=support&lang=fr)",
		"Cordialement,\nLéa Martin\nSupport client - ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "Nous avons corrigé l'export : voici") {
		t.Errorf("the preheader is visible in the text:\n%s", text)
	}
	if strings.Contains(text, "&nbsp;") || strings.Contains(text, "&amp;") || strings.ContainsAny(text, "​‌͏ ") {
		t.Errorf("entity or invisible character left in the text")
	}

	// eml: multipart/alternative, text then html, decodable, no recipient, no date
	m, err := mail.ReadMessage(bytes.NewReader(eml.Content))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"From", "To", "Date", "Message-Id", "Subject"} {
		if m.Header.Get(h) != "" {
			t.Errorf("header %s must be absent, got %q", h, m.Header.Get(h))
		}
	}
	typ, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || typ != "multipart/alternative" || params["boundary"] == "" {
		t.Fatalf("content-type %q: %v", m.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var types, bodies []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(p) // mime/multipart decodes quoted-printable itself
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, strings.SplitN(p.Header.Get("Content-Type"), ";", 2)[0])
		bodies = append(bodies, string(b))
	}
	if n := bytes.Count(eml.Content, []byte("Content-Transfer-Encoding: quoted-printable\r\n")); n != 2 {
		t.Fatalf("both parts must be quoted-printable, got %d", n)
	}
	if len(types) != 2 || types[0] != "text/plain" || types[1] != "text/html" {
		t.Fatalf("parts: %v", types)
	}
	if strings.ReplaceAll(bodies[0], "\r\n", "\n") != text || strings.ReplaceAll(bodies[1], "\r\n", "\n") != strings.ReplaceAll(h, "\r\n", "\n") {
		t.Fatalf("the eml parts differ from the txt and html outputs")
	}
	if bytes.Contains(eml.Content, []byte("\n")) && bytes.Count(eml.Content, []byte("\n")) != bytes.Count(eml.Content, []byte("\r\n")) {
		t.Fatalf("eml lines must all end in CRLF")
	}

	// the minimal example renders too (no steps)
	min := templatetest.Data(t, tpl, "minimal")
	mt := string(mustMsg(t, c, tpl, min, "txt").Content)
	if strings.Contains(mt, "Ouvrez la page") || !strings.Contains(mt, "Suivre ce ticket") {
		t.Fatalf("minimal text:\n%s", mt)
	}
}

func TestMessageRendersAreIdenticalToTheByte(t *testing.T) {
	c := mjmltest.Runner(t)
	tpl := templatetest.Load(t, "valid/support-reply")
	data := templatetest.Data(t, tpl, "default")
	for _, out := range []string{"html", "txt", "eml"} {
		a, b := mustMsg(t, c, tpl, data, out), mustMsg(t, c, tpl, data, out)
		if !bytes.Equal(a.Content, b.Content) || a.Provenance != b.Provenance {
			t.Fatalf("%s is not reproducible", out)
		}
	}
}

func TestMessageEMLSubjectFromData(t *testing.T) {
	c := mjmltest.Runner(t)
	tpl := edit(t, map[string]func(string) string{
		"input.schema.json": replace(`"steps_title"`, `"subject": { "type": "string" }, "steps_title"`),
	}, nil)
	data := dataOf(t, tpl)
	data["subject"] = "Re : l'export CSV est incomplet (ticket SUP-48213)\r\nBcc: x@example.invalid"
	eml := mustMsg(t, c, tpl, data, "eml")
	m, err := mail.ReadMessage(bytes.NewReader(eml.Content))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != "Re : l'export CSV est incomplet (ticket SUP-48213) Bcc: x@example.invalid" {
		t.Fatalf("subject %q: %v", subject, err)
	}
	if m.Header.Get("Bcc") != "" {
		t.Fatal("header injection through the subject")
	}
}

func TestMessageFaultsAreRejectedWithTheExpectedCode(t *testing.T) {
	c := mjmltest.Runner(t)
	t.Run("residual brace", func(t *testing.T) {
		tpl := edit(t, map[string]func(string) string{"main.mjml": replace(`<mj-text>{{ . }}</mj-text>`, `<mj-text>{{ . }} {{ "{{ .data.oups }}" }}</mj-text>`)}, nil)
		_, err := renderMsg(t, c, tpl, dataOf(t, tpl), "html")
		wantCode(t, err, template.CodeMessageTemplateResidue)
	})
	t.Run("residual brace that an unused data value also holds", func(t *testing.T) {
		tpl := edit(t, map[string]func(string) string{
			"main.mjml":         replace(`<mj-text>{{ . }}</mj-text>`, `<mj-text>{{ . }} {{ "{{ .data.oups }}" }}</mj-text>`),
			"input.schema.json": replace(`"steps_title"`, `"unused": { "type": "string" }, "steps_title"`),
		}, nil)
		data := dataOf(t, tpl)
		data["unused"] = "{{ .data.oups }}"
		_, err := renderMsg(t, c, tpl, data, "html")
		wantCode(t, err, template.CodeMessageTemplateResidue)
	})
	t.Run("residual brace in a closing position", func(t *testing.T) {
		tpl := edit(t, map[string]func(string) string{"main.mjml": replace(`<mj-text>{{ . }}</mj-text>`, `<mj-text>{{ . }} {{ "}}" }}</mj-text>`)}, nil)
		_, err := renderMsg(t, c, tpl, dataOf(t, tpl), "html")
		wantCode(t, err, template.CodeMessageTemplateResidue)
	})
	t.Run("a condition on a data value does not change the branch of the check", func(t *testing.T) {
		tpl := edit(t, map[string]func(string) string{
			"main.mjml": replace(`<mj-text>{{ . }}</mj-text>`, `<mj-text>{{ . }}{{ if eq $.data.customer_name "{{" }}{{ "{{ .data.oups }}" }}{{ end }}</mj-text>`),
		}, nil)
		data := dataOf(t, tpl)
		data["customer_name"] = "{{"
		_, err := renderMsg(t, c, tpl, data, "html")
		wantCode(t, err, template.CodeMessageTemplateResidue)
		data["customer_name"] = "Camille"
		if _, err := renderMsg(t, c, tpl, data, "html"); err != nil {
			t.Fatalf("branch not taken: %v", err)
		}
	})
	t.Run("a data value with braces in a URL and in a default is legitimate", func(t *testing.T) {
		tpl := templatetest.Load(t, "valid/support-reply")
		data := dataOf(t, tpl)
		data["ticket_url"] = "https://support.atelier-exemple.invalid/t/{{id}}?a={{b}}"
		data["steps_title"] = ""
		if _, err := renderMsg(t, c, tpl, data, "html"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a marker character in the data is refused", func(t *testing.T) {
		tpl := templatetest.Load(t, "valid/support-reply")
		for _, bad := range []string{"a\ue000b", "\ue001"} {
			data := dataOf(t, tpl)
			data["customer_name"] = bad
			_, err := renderMsg(t, c, tpl, data, "html")
			wantCode(t, err, template.CodeMessageReservedChar)
		}
	})
	t.Run("hidden by a class, end to end", func(t *testing.T) {
		hide := edit(t, map[string]func(string) string{
			"main.mjml": func(s string) string {
				s = strings.Replace(s, `<mj-text>{{ .i18n.strings.message_greeting }}`, `<mj-text css-class="cache">{{ .i18n.strings.message_greeting }}`, 1)
				return strings.Replace(s, "<mj-style>\n", "<mj-style>\n      .cache { display:none !important; }\n", 1)
			},
		}, nil)
		res := mustMsg(t, c, hide, dataOf(t, hide), "txt")
		if strings.Contains(string(res.Content), "Bonjour Camille") || !strings.Contains(string(res.Content), "Suivre ce ticket") {
			t.Fatalf("hidden text leaked or visible text lost:\n%s", res.Content)
		}
		// a hiding rule with a selector that is not evaluated: the text is refused, the html is not
		unk := edit(t, map[string]func(string) string{
			"main.mjml": func(s string) string {
				return strings.Replace(s, "<mj-style>\n", "<mj-style>\n      td .cache { display:none; }\n", 1)
			},
		}, nil)
		if _, err := renderMsg(t, c, unk, dataOf(t, unk), "html"); err != nil {
			t.Fatalf("html must render: %v", err)
		}
		for _, out := range []string{"txt", "eml"} {
			_, err := renderMsg(t, c, unk, dataOf(t, unk), out)
			wantCode(t, err, template.CodeMessageHiddenRule)
		}
	})
	t.Run("a closing brace in a static source", func(t *testing.T) {
		for _, bad := range []string{"Bonjour }}", "x }} y", "}}"} {
			tpl := edit(t, map[string]func(string) string{"main.mjml": replace(`<mj-button `, `<mj-text>`+bad+`</mj-text><mj-button `)}, nil)
			_, err := renderMsg(t, c, tpl, dataOf(t, tpl), "html")
			wantCode(t, err, template.CodeMessageTemplateResidue)
		}
	})
	t.Run("a data value emitted with braces is legitimate", func(t *testing.T) {
		tpl := templatetest.Load(t, "valid/support-reply")
		data := dataOf(t, tpl)
		data["customer_name"] = "{{ .data.x }} et }} seul {{{"
		data["steps"] = []any{"étape {{ 1+1 }}", "}}"}
		res := mustMsg(t, c, tpl, data, "txt")
		for _, want := range []string{"{{ .data.x }} et }} seul {{{", "2. }}"} {
			if !strings.Contains(string(res.Content), want) {
				t.Fatalf("text lacks %q:\n%s", want, res.Content)
			}
		}
	})
	t.Run("include outside the fragments", func(t *testing.T) {
		for _, p := range []string{"../secret.mjml", "/etc/passwd", "parts/other.mjml", "fragments/message/../../x.mjml", "https://example.invalid/x.mjml"} {
			tpl := edit(t, map[string]func(string) string{"main.mjml": replace(`<mj-include path="fragments/message/header.mjml" />`, `<mj-include path="`+p+`" />`)}, nil)
			_, err := renderMsg(t, c, tpl, dataOf(t, tpl), "html")
			wantCode(t, err, template.CodeMJMLIncludeOutside)
		}
	})
	t.Run("include of an undeclared library fragment", func(t *testing.T) {
		tpl := edit(t, nil, func(m map[string]any) { m["fragments"] = []any{"fragments/message/header.mjml"} })
		_, err := renderMsg(t, c, tpl, dataOf(t, tpl), "html")
		wantCode(t, err, template.CodeFragmentUndeclared)
	})
	t.Run("http URL from the data", func(t *testing.T) {
		tpl := edit(t, map[string]func(string) string{"input.schema.json": replace(`"ticket_url": { "type": "string", "pattern": "^https://[^\\s]+$" }`, `"ticket_url": { "type": "string" }`)}, nil)
		for _, bad := range []string{"http://support.atelier-exemple.invalid/t/1", "javascript:alert(1)", "/relatif", "//hote.invalid/x", "https://"} {
			data := dataOf(t, tpl)
			data["ticket_url"] = bad
			_, err := renderMsg(t, c, tpl, data, "html")
			wantCode(t, err, template.CodeMessageURLNotHTTPS)
		}
	})
	t.Run("weight above 102 KiB", func(t *testing.T) {
		tpl := templatetest.Load(t, "valid/support-reply")
		data := dataOf(t, tpl)
		data["paragraphs"] = []any{strings.Repeat("mot ", 30000)}
		_, err := renderMsg(t, c, tpl, data, "html")
		wantCode(t, err, template.CodeMessageTooHeavy)
	})
	t.Run("marketing without the rendered unsubscribe link", func(t *testing.T) {
		tpl := edit(t, map[string]func(string) string{
			"main.mjml":         replace(`<mj-include path="fragments/message/footer-unsubscribe.mjml" />`, `{{ if .data.show_footer }}<mj-include path="fragments/message/footer-unsubscribe.mjml" />{{ end }}`),
			"input.schema.json": replace(`"steps_title"`, `"show_footer": { "type": "boolean" }, "steps_title"`),
		}, func(m map[string]any) { m["message"] = map[string]any{"category": "marketing"} })
		data := dataOf(t, tpl)
		data["show_footer"] = false
		_, err := renderMsg(t, c, tpl, data, "html")
		wantCode(t, err, template.CodeMessageUnsubscribeMiss)
		data["show_footer"] = true
		if _, err := renderMsg(t, c, tpl, data, "html"); err != nil {
			t.Fatalf("marketing with the footer must render: %v", err)
		}
	})
	t.Run("MJML refuses the document", func(t *testing.T) {
		tpl := edit(t, map[string]func(string) string{"main.mjml": replace(`<mj-button `, `<mj-bogus /><mj-button `)}, nil)
		_, err := renderMsg(t, c, tpl, dataOf(t, tpl), "html")
		wantCode(t, err, template.CodeMJMLFailed)
		if !strings.Contains(err.Error(), "mj-bogus") || strings.Contains(err.Error(), os.TempDir()+"/artifact-mjml-") {
			t.Fatalf("the message must carry the validation error without the working directory: %v", err)
		}
	})
	t.Run("output not allowed", func(t *testing.T) {
		tpl := templatetest.Load(t, "valid/support-reply")
		_, err := renderMsg(t, c, tpl, dataOf(t, tpl), "pdf")
		wantCode(t, err, template.CodeOutputUnsupported)
	})
}

func TestMessageEngineAbsent(t *testing.T) {
	tpl := templatetest.Load(t, "valid/support-reply")
	data := templatetest.Data(t, tpl, "default")
	_, err := renderMsg(t, nil, tpl, data, "html")
	wantCode(t, err, template.CodeEngineUnavailable)
	_, err = renderMsg(t, mjml.Runner{NodeBin: "node", Dir: filepath.Join(t.TempDir(), "nope")}, tpl, data, "html")
	wantCode(t, err, template.CodeEngineUnavailable)
	_, err = renderMsg(t, mjml.Runner{}, tpl, data, "html")
	wantCode(t, err, template.CodeEngineUnavailable)
}

func TestMessageEngineTimeout(t *testing.T) {
	c := mjmltest.Runner(t)
	tpl := templatetest.Load(t, "valid/support-reply")
	ctx, assets := templatetest.Context(t)
	_, err := template.Render(context.Background(), template.Request{
		Template: tpl, Data: templatetest.Data(t, tpl, "default"), Context: ctx, Assets: assets, Output: "html",
	}, nil, template.Options{Timeout: time.Millisecond, MJML: c})
	wantCode(t, err, template.CodeRenderTimeout)
}

// ---- output checks, without MJML ----

const okDoc = `<!doctype html><html><head><title>t</title></head><body><a href="https://a.invalid/x">x</a></body></html>`

func TestCheckMessageHTML(t *testing.T) {
	cases := []struct {
		name, doc, category, code string
		data                      any
	}{
		{"ok", okDoc, "", "", nil},
		{"include denied", strings.Replace(okDoc, "<body>", "<body><!-- mj-include denied -->", 1), "", template.CodeMJMLIncludeDenied, nil},
		{"empty", "", "", template.CodeMessageOutputInvalid, nil},
		{"not a document", "<p>x</p>", "", template.CodeMessageOutputInvalid, nil},
		{"heavy", strings.Replace(okDoc, "x</a>", strings.Repeat("a", template.MaxMessageHTMLBytes), 1), "", template.CodeMessageTooHeavy, nil},
		{"http href", strings.Replace(okDoc, "https://a", "http://a", 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"src relative", strings.Replace(okDoc, "<a ", `<img src="logo.png"><a `, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"srcset", strings.Replace(okDoc, "<a ", `<img srcset="https://a.invalid/1.png 2x"><a `, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"background attribute", strings.Replace(okDoc, "<a ", `<td background="http://a.invalid/b.png"></td><a `, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"css url in style attribute", strings.Replace(okDoc, "<a ", `<td style="background:url('data:image/png;base64,AAAA')"></td><a `, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"css url in style element", strings.Replace(okDoc, "<head>", `<head><style>.a{background:url(http://a.invalid/b.png)}</style>`, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"url in a conditional comment", strings.Replace(okDoc, "<body>", `<body><!--[if mso]><v:fill src="http://a.invalid/b.png"><![endif]-->`, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"https url in a conditional comment", strings.Replace(okDoc, "<body>", `<body><!--[if mso]><v:fill src="https://a.invalid/b.png"><![endif]-->`, 1), "", "", nil},
		{"escaped url function", strings.Replace(okDoc, "<a ", `<td style="background-image:u\72l('http://a.invalid/b.png')"></td><a `, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"hex escape with space", strings.Replace(okDoc, "<a ", `<td style="background:\75 \72 l(http://a.invalid/b.png)"></td><a `, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"escaped letter without hex", strings.Replace(okDoc, "<a ", `<td style="background:\u\r\l(http://a.invalid/b.png)"></td><a `, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"comment inside url", strings.Replace(okDoc, "<a ", `<td style="background:ur/**/l(http://a.invalid/b.png)"></td><a `, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"escaped url in a style element", strings.Replace(okDoc, "<head>", `<head><style>.a{background:\75rl("http://a.invalid/b.png")}</style>`, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"import with a string", strings.Replace(okDoc, "<head>", `<head><style>@import "http://a.invalid/x.css";</style>`, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"escaped import", strings.Replace(okDoc, "<head>", `<head><style>@\69mport url(http://a.invalid/x.css);</style>`, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"image-set string", strings.Replace(okDoc, "<a ", `<td style="background-image:-webkit-image-set('https://a.invalid/1.png' 1x, 'http://a.invalid/2.png' 2x)"></td><a `, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"image-set https", strings.Replace(okDoc, "<a ", `<td style="background-image:image-set(&quot;https://a.invalid/1.png&quot; 1x)"></td><a `, 1), "", "", nil},
		{"escaped https url is fine", strings.Replace(okDoc, "<a ", `<td style="background:u\72l(https://a.invalid/b.png)"></td><a `, 1), "", "", nil},
		{"escaped url in a conditional comment", strings.Replace(okDoc, "<body>", `<body><!--[if mso]><v:rect style="background:u\72l(http://a.invalid/b.png)"><![endif]-->`, 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"ZgotmplZ", strings.Replace(okDoc, "https://a.invalid/x", "#ZgotmplZ", 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"credentials in the URL", strings.Replace(okDoc, "https://a", "https://u:p@a", 1), "", template.CodeMessageURLNotHTTPS, nil},
		{"marketing with the link", okDoc, "marketing", "", map[string]any{"unsubscribe_url": "https://a.invalid/x"}},
		{"marketing without the link", okDoc, "marketing", template.CodeMessageUnsubscribeMiss, map[string]any{"unsubscribe_url": "https://a.invalid/other"}},
		{"marketing without the data", okDoc, "marketing", template.CodeMessageUnsubscribeMiss, map[string]any{}},
		{"marketing: %2F is not /", okDoc, "marketing", template.CodeMessageUnsubscribeMiss, map[string]any{"unsubscribe_url": "https://a.invalid/a/b"}},
		{"marketing: encoded slash in both", strings.Replace(okDoc, "https://a.invalid/x", "https://a.invalid/a%2Fb?q=1", 1), "marketing", "", map[string]any{"unsubscribe_url": "https://a.invalid/a%2Fb?q=1"}},
		{"marketing: encoded slash only in the link", strings.Replace(okDoc, "https://a.invalid/x", "https://a.invalid/a%2Fb", 1), "marketing", template.CodeMessageUnsubscribeMiss, map[string]any{"unsubscribe_url": "https://a.invalid/a/b"}},
		{"marketing: host case differs", strings.Replace(okDoc, "https://a.invalid/x", "https://A.Invalid:443/x?u=1#f", 1), "marketing", "", map[string]any{"unsubscribe_url": "https://a.invalid/x?u=1#f"}},
		{"marketing: path case differs", strings.Replace(okDoc, "https://a.invalid/x", "https://a.invalid/X", 1), "marketing", template.CodeMessageUnsubscribeMiss, map[string]any{"unsubscribe_url": "https://a.invalid/x"}},
		{"marketing: query differs", strings.Replace(okDoc, "https://a.invalid/x", "https://a.invalid/x?a=%41", 1), "marketing", template.CodeMessageUnsubscribeMiss, map[string]any{"unsubscribe_url": "https://a.invalid/x?a=A"}},
		{"transactional needs no link", okDoc, "transactional", "", map[string]any{"unsubscribe_url": "https://a.invalid/other"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := template.CheckMessageHTML(tc.doc, tc.category, tc.data)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			wantCode(t, err, tc.code)
		})
	}
}

// ---- text conversion (cases of the bench check.py) ----

func TestHTMLToText(t *testing.T) {
	const preheader = "Texte d'aperçu caché"
	src := `<!doctype html><html><head><title>Titre</title><style>p{color:red}</style><script>var a=1</script></head><body>
<!--[if mso]><table><tr><td>MSO</td></tr></table><![endif]-->
<div style="display:none;font-size:1px">` + preheader + `&#847;&zwnj;&nbsp;&#847;</div>
<table><tr><td><h1>Bonjour</h1></td></tr><tr><td><p>Un &amp; deux &lt;b&gt;litt&eacute;ral&lt;/b&gt; {{ 1+1 }}</p>
<ol><li>premier</li><li>second</li></ol><ul><li>puce</li></ul>
<p><a href="https://a.invalid/t?x=1&amp;y=2">Suivre ce ticket</a> et <a href="https://a.invalid/u">https://a.invalid/u</a> et <a href="/relatif">rel</a></p>
<p><img src="https://a.invalid/i.png" alt="Logo Atelier"><img src="https://a.invalid/j.png" alt=""></p>
<p>a<br>b</p><p>espace&nbsp;insécable&#8239;fin</p></td></tr></table></body></html>`
	got, _ := htmlText(t, src)
	want := "Bonjour\n\nUn & deux <b>littéral</b> {{ 1+1 }}\n\n1. premier\n2. second\n\n- puce\n\n" +
		"Suivre ce ticket (https://a.invalid/t?x=1&y=2) et https://a.invalid/u et rel\n\n[Logo Atelier]\n\na\nb\n\nespace insécable fin\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	if strings.Contains(got, "caché") || strings.Contains(got, "MSO") || strings.Contains(got, "Titre") || strings.Contains(got, "color:red") {
		t.Fatalf("hidden content leaked:\n%s", got)
	}
	if mustText(t, "") != "\n" || mustText(t, "<p> </p>") != "\n" {
		t.Fatal("an empty document gives a single newline")
	}
	// a stray end tag does not close the hidden block of the preheader
	if got := mustText(t, `</span><div style="display:none">cache</div><p>vu</p>`); got != "vu\n" {
		t.Fatalf("stray end tag: %q", got)
	}
}

func TestHTMLToTextDropsEveryHidingPattern(t *testing.T) {
	hidden := []string{
		`<span hidden>X</span>`,
		`<span HIDDEN="">X</span>`,
		`<span style="visibility:hidden">X</span>`,
		`<span style="  Visibility : Hidden ;color:red">X</span>`,
		`<span style="visibility:collapse">X</span>`,
		`<span style="mso-hide:all">X</span>`,
		`<span style="MSO-HIDE : ALL !important">X</span>`,
		`<span style="display : none">X</span>`,
		`<span style="color:red;DISPLAY:NONE;">X</span>`,
		`<div style="max-height:0px;overflow:hidden">X</div>`,
		`<div style="overflow: hidden; max-height: 0">X</div>`,
		`<div style="opacity:0">X</div>`,
		`<div style="opacity: 0.0 !important">X</div>`,
		`<div style="font-size:0;overflow:hidden">X</div>`,
		`<div style="line-height:0px;overflow:hidden">X</div>`,
		`<div style="max-height:0;overflow:hidden"><p>X</p></div>`,
		`<span style="display:/**/none">X</span>`,
		`<span style="display:\6e one">X</span>`,
		`<span style="d\69splay:\6eone">X</span>`,
		`<span style="visibility:/* c */hidden">X</span>`,
		`<span style="visibility:\68 idden">X</span>`,
		`<span style="vis/**/ibility:hidden">X</span>`,
		`<span style="display:none/**/!important">X</span>`,
		`<span style="display:none ! important /* x */">X</span>`,
		`<span style="color:red;/* ; */display:none">X</span>`,
		`<span style="mso-hide:\61ll">X</span>`,
		`<img hidden src="https://a.invalid/i.png" alt="X">`,
	}
	for _, h := range hidden {
		if got := mustText(t, "<p>vu</p>"+h+"<p>fin</p>"); got != "vu\n\nfin\n" {
			t.Errorf("%s leaks: %q", h, got)
		}
	}
	visible := []string{
		`<span style="font-size:0px">X</span>`, // MJML wrappers of visible content
		`<span style="max-height:0px">X</span>`,
		`<span style="overflow:hidden">X</span>`,
		`<span style="opacity:0.5">X</span>`,
		`<span style="opacity:1">X</span>`,
		`<span style="display:block;visibility:visible">X</span>`,
		`<span style="font-size:10px;line-height:1.5">X</span>`,
		`<span style="content:'display:none'">X</span>`,
		`<span title="display:none">X</span>`,
	}
	for _, v := range visible {
		if got := mustText(t, "<p>vu</p>"+v+"<p>fin</p>"); !strings.Contains(got, "X") {
			t.Errorf("%s must stay visible: %q", v, got)
		}
	}
	if got := mustText(t, `<span hidden>Interne</span><span style="visibility:hidden">Confidentiel</span><p>ok</p>`); got != "ok\n" {
		t.Fatalf("review case: %q", got)
	}
}

func htmlText(t *testing.T, src string) (string, error) {
	t.Helper()
	return template.HTMLToText(src)
}

func mustText(t *testing.T, src string) string {
	t.Helper()
	got, err := template.HTMLToText(src)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestHTMLToTextStylesheetRules(t *testing.T) {
	const page = "<p>vu</p>%s<p>fin</p>"
	hidden := map[string]string{
		"class":               `<style>.cache { display:none !important; }</style><span class="cache">X</span>`,
		"element and class":   `<style>span.cache{visibility:hidden}</style><span class="cache">X</span>`,
		"list":                `<style>.a, p.b , .cache{opacity:0}</style><span class="cache">X</span>`,
		"two classes":         `<style>.a.b{mso-hide:all}</style><span class="x b a">X</span>`,
		"element":             `<style>em{display:none}</style><em>X</em>`,
		"id":                  `<style>#p1{display:none}</style><span id="p1">X</span>`,
		"escaped selector":    `<style>.ca\63 he{display:none}</style><span class="cache">X</span>`,
		"escaped property":    `<style>.cache{d\69splay:none}</style><span class="cache">X</span>`,
		"comment in the rule": `<style>/* x */ .cache /* y */ { display /* z */ : none }</style><span class="cache">X</span>`,
		"preheader pattern":   `<style>.pre{max-height:0;overflow:hidden}</style><div class="pre">X</div>`,
		"void element":        `<style>.cache{display:none}</style><img class="cache" src="https://a.invalid/i.png" alt="X">`,
		"screen media":        `<style media="screen">.cache{display:none}</style><span class="cache">X</span>`,
		"after a media rule":  `<style>@media (max-width:480px){.z{display:none}} .cache{display:none}</style><span class="cache">X</span>`,
		"after a font-face":   `<style>@font-face{font-family:a;src:url(https://a.invalid/f.woff)} .cache{display:none}</style><span class="cache">X</span>`,
		"string with a brace": `<style>.q:after{content:"}"} .cache{display:none}</style><span class="cache">X</span>`,
		"two style blocks":    `<style>.a{color:red}</style><style>.cache{display:none}</style><span class="cache">X</span>`,
	}
	for name, h := range hidden {
		if got := mustText(t, strings.Replace(page, "%s", h, 1)); got != "vu\n\nfin\n" {
			t.Errorf("%s leaks: %q", name, got)
		}
	}
	visible := map[string]string{
		"rule that does not hide": `<style>.cache{color:red;font-size:0}</style><span class="cache">X</span>`,
		"other class":             `<style>.cache{display:none}</style><span class="cachee">X</span>`,
		"inside a media query":    `<style>@media (max-width:480px){.cache{display:none}}</style><span class="cache">X</span>`,
		"dark mode media":         `<style>@media (prefers-color-scheme: dark){.cache{display:none !important}}</style><span class="cache">X</span>`,
		"print only":              `<style media="print">.cache{display:none}</style><span class="cache">X</span>`,
		"conditional comment":     `<!--[if mso]><style>.cache{display:none}</style><![endif]--><span class="cache">X</span>`,
		"not hiding, not simple":  `<style>td .cache > a[href]:hover{color:red}</style><span class="cache">X</span>`,
		"other element":           `<style>em{display:none}</style><span>X</span>`,
		"keyframes":               `<style>@keyframes k{from{opacity:0}to{opacity:1}}</style><span>X</span>`,
	}
	for name, v := range visible {
		if got := mustText(t, strings.Replace(page, "%s", v, 1)); !strings.Contains(got, "X") {
			t.Errorf("%s must stay visible: %q", name, got)
		}
	}
	refused := map[string]string{
		"descendant":      `<style>td .cache{display:none}</style>`,
		"child":           `<style>.a > .cache{display:none}</style>`,
		"attribute":       `<style>[hidden-x]{display:none}</style>`,
		"pseudo-class":    `<style>.cache:first-child{display:none}</style>`,
		"in a list":       `<style>.ok, td .cache{display:none}</style>`,
		"universal":       `<style>*{visibility:hidden}</style>`,
		"supports":        `<style>@supports (display:grid){.cache{display:none}}</style>`,
		"nested":          `<style>.a{ .cache{display:none} }</style>`,
		"escaped bracket": `<style>.a\5b b\5d{display:none}</style>`,
	}
	for name, r := range refused {
		_, err := template.HTMLToText(strings.Replace(page, "%s", r, 1))
		wantCode(t, err, template.CodeMessageHiddenRule)
		_ = name
	}
}

func TestHTMLToTextCascade(t *testing.T) {
	const page = "<p>vu</p>%s<p>fin</p>"
	cases := []struct {
		name, src string
		hidden    bool
	}{
		{"important then block, inline", `<span style="display:none !important;display:block">X</span>`, true},
		{"none then block, inline", `<span style="display:none;display:block">X</span>`, false},
		{"block important then none, inline", `<span style="display:block !important;display:none">X</span>`, false},
		{"none important after comment", `<span style="display:none/**/!important;display:block">X</span>`, true},
		{"visibility last wins", `<span style="visibility:hidden;visibility:visible">X</span>`, false},
		{"same rule twice", `<style>.contenu{display:none}.contenu{display:block}</style><p class="contenu">X</p>`, false},
		{"later rule hides", `<style>.contenu{display:block}.contenu{display:none}</style><p class="contenu">X</p>`, true},
		{"important sheet over inline", `<style>.a{display:none !important}</style><span class="a" style="display:block">X</span>`, true},
		{"normal sheet under inline", `<style>.a{display:none}</style><span class="a" style="display:block">X</span>`, false},
		{"inline important over important sheet", `<style>.a{display:none !important}</style><span class="a" style="display:block !important">X</span>`, false},
		{"id over class, id first", `<style>#x{display:block} .a{display:none}</style><span id="x" class="a">X</span>`, false},
		{"id over class, id last", `<style>.a{display:none} #x{display:block}</style><span id="x" class="a">X</span>`, false},
		{"id hides over class", `<style>.a{display:block} #x{display:none}</style><span id="x" class="a">X</span>`, true},
		{"equal specificity, last wins (visible)", `<style>.a{display:none} .b{display:block}</style><span class="a b">X</span>`, false},
		{"equal specificity, last wins (hidden)", `<style>.b{display:block} .a{display:none}</style><span class="a b">X</span>`, true},
		{"element and class over class", `<style>span.a{display:none} .a{display:block}</style><span class="a">X</span>`, true},
		{"class over element", `<style>span{display:none} .a{display:block}</style><span class="a">X</span>`, false},
		{"two classes over one", `<style>.a.b{display:none} .a{display:block}</style><span class="a b">X</span>`, true},
		{"important in an earlier rule", `<style>.a{display:none !important} .a{display:block}</style><span class="a">X</span>`, true},
		{"later block in another style element", `<style>.a{display:none}</style><style>.a{display:block}</style><span class="a">X</span>`, false},
		{"combination undone", `<style>.p{max-height:0;overflow:hidden} .p{overflow:visible}</style><div class="p">X</div>`, false},
		{"combination from two rules", `<style>.p{max-height:0} .q{overflow:hidden}</style><div class="p q">X</div>`, true},
		{"combination from rule and inline", `<style>.p{max-height:0}</style><div class="p" style="overflow:hidden">X</div>`, true},
		{"opacity overridden", `<style>.o{opacity:0}</style><div class="o" style="opacity:1">X</div>`, false},
		{"descendants of a hidden element", `<style>.a{display:none}</style><div class="a"><p style="display:block">X</p></div>`, true},
	}
	for _, tc := range cases {
		got := mustText(t, strings.Replace(page, "%s", tc.src, 1))
		if has := strings.Contains(got, "X"); has == tc.hidden {
			t.Errorf("%s: hidden=%v but text is %q", tc.name, tc.hidden, got)
		}
	}
	// a rule that cannot be evaluated still fails closed, whatever follows it
	_, err := template.HTMLToText(strings.Replace(page, "%s", `<style>td .a{display:none} .a{display:block}</style>`, 1))
	wantCode(t, err, template.CodeMessageHiddenRule)
}

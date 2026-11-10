package template

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"mime/quotedprintable"
	"strings"
)

// BuildEML assembles a multipart/alternative message (plain text first, HTML
// last, the preferred part). It is a file, never a sent message: no From, To,
// Date or Message-ID. Same inputs, same bytes: the boundary is derived from the
// content, lines end in CRLF.
//
// Both parts are quoted-printable: the content is mostly ASCII French text, so
// it stays readable and diffable, at a cost of a few percent where base64 costs
// 33 %. Subject (optional, from data.subject) is RFC 2047 encoded when needed.
func BuildEML(subject, text, htmlDoc string) ([]byte, error) {
	sum := sha256.Sum256([]byte(text + "\x00" + htmlDoc))
	boundary := "artifact-" + hex.EncodeToString(sum[:])[:32]
	var b bytes.Buffer
	b.WriteString("MIME-Version: 1.0\r\n")
	if subject = cleanHeader(subject); subject != "" {
		if len(subject) > 900 {
			return nil, errf(CodeMessageOutputInvalid, "data.subject is %d bytes, above 900", len(subject))
		}
		b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	}
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
	for _, part := range []struct{ typ, body string }{{"text/plain", text}, {"text/html", htmlDoc}} {
		if strings.Contains(part.body, boundary) {
			return nil, errf(CodeMessageOutputInvalid, "a message part holds the MIME boundary")
		}
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + part.typ + "; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		w := quotedprintable.NewWriter(&b)
		if _, err := w.Write([]byte(part.body)); err != nil {
			return nil, errf(CodeMessageOutputInvalid, "quoted-printable: %v", err)
		}
		if err := w.Close(); err != nil {
			return nil, errf(CodeMessageOutputInvalid, "quoted-printable: %v", err)
		}
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

// cleanHeader makes a value safe for a header: control characters (CR and LF
// first, which would inject headers) become spaces.
func cleanHeader(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// Package interchange renders the utility outputs of Artifact: iCalendar
// (RFC 5545), vCard 4.0 (RFC 6350) and normalized CSV. The input is typed JSON
// validated against a closed schema and decoded strictly; no free text is ever
// interpreted. Standard library only, output is a pure function of the input.
package interchange

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Output kinds produced by this package.
const (
	OutputICS = "ics"
	OutputVCF = "vcf"
	OutputCSV = "csv"
)

// Limits of the utility outputs.
const (
	MaxItems        = 500 // events or cards per document
	MaxAttendees    = 100 // attendees per event
	MaxListItems    = 50  // emails, phones, addresses, urls per card
	MaxTextBytes    = 32 << 10
	MaxLineSource   = 2048 // single-line fields (summary, names, ...)
	MaxCSVRows      = 50000
	MaxCSVColumns   = 256
	MaxCSVCellBytes = 32 << 10
	MaxOutputBytes  = 8 << 20
	foldWidth       = 75
)

// ErrData marks a rejected input; ErrLimits a bound that is exceeded.
var (
	ErrData   = errors.New("data_invalid")
	ErrLimits = errors.New("limits_exceeded")
)

type failure struct {
	kind error
	msg  string
}

func (f *failure) Error() string { return f.msg }
func (f *failure) Unwrap() error { return f.kind }

func dataErr(format string, args ...any) error {
	return &failure{ErrData, fmt.Sprintf(format, args...)}
}

func limitErr(format string, args ...any) error {
	return &failure{ErrLimits, fmt.Sprintf(format, args...)}
}

// Fold wraps one content line at 75 octets (RFC 5545 3.1, RFC 6350 3.2): each
// continuation starts with one space, counted in the 75 octets, and a multi-byte
// UTF-8 sequence is never split. The result ends with CRLF.
func Fold(line string) string {
	var b strings.Builder
	width := foldWidth
	used := 0
	for _, r := range line {
		n := utf8.RuneLen(r)
		if used+n > width {
			b.WriteString("\r\n ")
			width, used = foldWidth-1, 0
		}
		b.WriteRune(r)
		used += n
	}
	b.WriteString("\r\n")
	return b.String()
}

// EscapeText escapes a TEXT value: backslash, semicolon, comma and line breaks
// (CRLF, LF and CR all become the two characters \n).
func EscapeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case ';':
			b.WriteString(`\;`)
		case ',':
			b.WriteString(`\,`)
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			b.WriteString(`\n`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// paramValue renders a parameter value: double-quoted when it holds , ; or :.
// RFC 5545 (and 6350) forbid a double quote in a parameter value: checkParam
// refuses such a value beforehand, it is never altered.
func paramValue(s string) string {
	if strings.ContainsAny(s, ",;:") {
		return `"` + s + `"`
	}
	return s
}

// checkParam validates a value used as a parameter (CN, TZID): a single line
// without double quote.
func checkParam(field, s string) error {
	if err := checkLine(field, s); err != nil {
		return err
	}
	if strings.ContainsRune(s, '"') {
		return dataErr("%s %q holds a double quote, which a parameter value cannot contain (RFC 5545)", field, s)
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// checkText validates a TEXT value: valid UTF-8, no control character other than
// tab and line breaks (multiline) or none at all (single line).
func checkText(field, s string, multiline bool, max int) error {
	if len(s) > max {
		return limitErr("%s is %d bytes, above the limit of %d", field, len(s), max)
	}
	if !utf8.ValidString(s) {
		return dataErr("%s is not valid UTF-8", field)
	}
	for _, r := range s {
		if !isControl(r) || r == '\t' {
			continue
		}
		if multiline && (r == '\n' || r == '\r') {
			continue
		}
		return dataErr("%s holds a control character (U+%04X)", field, r)
	}
	return nil
}

func checkLine(field, s string) error { return checkText(field, s, false, MaxLineSource) }

func checkEmail(field, s string) error {
	if err := checkLine(field, s); err != nil {
		return err
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return dataErr("%s %q is not a plain e-mail address", field, s)
	}
	return nil
}

func checkURI(field, s string) error {
	if err := checkLine(field, s); err != nil {
		return err
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(s, " \t") {
		return dataErr("%s %q is not an absolute http(s) URL", field, s)
	}
	return nil
}

// derivedID returns a hex digest of a canonical value, the basis of every
// derived identifier.
func derivedID(kind string, canonical []byte) string {
	h := sha256.New()
	h.Write([]byte("agentmaurice.artifact." + kind + "\x00"))
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil))
}

// document accumulates folded content lines.
type document struct{ b strings.Builder }

func (d *document) line(s string) { d.b.WriteString(Fold(s)) }

func (d *document) prop(name string, params []string, value string) {
	var sb strings.Builder
	sb.WriteString(name)
	for _, p := range params {
		sb.WriteByte(';')
		sb.WriteString(p)
	}
	sb.WriteByte(':')
	sb.WriteString(value)
	d.line(sb.String())
}

func (d *document) bytes() ([]byte, error) {
	if d.b.Len() > MaxOutputBytes {
		return nil, limitErr("output is %d bytes, above the limit of %d", d.b.Len(), MaxOutputBytes)
	}
	return []byte(d.b.String()), nil
}

package interchange

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CSVInput is the typed data of the csv output. Rows are arrays of cells or
// objects keyed by header; a cell is a string, a number, a boolean or null.
type CSVInput struct {
	Headers []string `json:"headers,omitempty"`
	Rows    []any    `json:"rows"`
}

// CSVOptions controls the encoding. The zero value is the historical
// artifact_create_v1 output: comma, LF, minimal quoting, no BOM.
type CSVOptions struct {
	Separator          string   `json:"separator,omitempty"`   // "," (default), ";", "\t", "|"
	BOM                bool     `json:"bom,omitempty"`         // UTF-8 byte order mark
	LineEnding         string   `json:"line_ending,omitempty"` // "lf" (zero value) or "crlf"
	Quote              string   `json:"quote,omitempty"`       // "minimal" (default) or "all"
	ExpectedColumns    int      `json:"expected_columns,omitempty"`
	ExpectedHeaders    []string `json:"expected_headers,omitempty"`
	NeutralizeFormulas bool     `json:"neutralize_formulas,omitempty"` // prefix cells starting with = + - @ by an apostrophe
}

// Table is a header row (optional) and string rows.
type Table struct {
	Headers []string
	Rows    [][]string
}

// WriteCSV encodes the table. It checks the options, the column count of every
// row and the expected headers; encoding/csv does the quoting.
//
// The bounds of the utility output (rows, columns, cell size, output size) apply.
func WriteCSV(t Table, o CSVOptions) ([]byte, error) { return writeCSV(t, o, true) }

// WriteCSVUnbounded is WriteCSV without the bounds of the utility output: it is
// the writer of artifact_create_v1, whose only limit is the global size of the
// artifact (checked by the caller), as in v0.1.
func WriteCSVUnbounded(t Table, o CSVOptions) ([]byte, error) { return writeCSV(t, o, false) }

func writeCSV(t Table, o CSVOptions, bounded bool) ([]byte, error) {
	comma := ','
	switch o.Separator {
	case "", ",":
	case ";", "\t", "|":
		comma = rune(o.Separator[0])
	default:
		return nil, dataErr("separator %q is not one of , ; tab |", o.Separator)
	}
	crlf := false
	switch o.LineEnding {
	case "", "lf":
	case "crlf":
		crlf = true
	default:
		return nil, dataErr("line_ending %q is not lf or crlf", o.LineEnding)
	}
	switch o.Quote {
	case "", "minimal", "all":
	default:
		return nil, dataErr("quote %q is not minimal or all", o.Quote)
	}
	if bounded && len(t.Rows) > MaxCSVRows {
		return nil, limitErr("%d rows, above the limit of %d", len(t.Rows), MaxCSVRows)
	}
	width := len(t.Headers)
	if width == 0 && len(t.Rows) > 0 {
		width = len(t.Rows[0])
	}
	if width == 0 {
		width = o.ExpectedColumns
	}
	if bounded && width > MaxCSVColumns {
		return nil, limitErr("%d columns, above the limit of %d", width, MaxCSVColumns)
	}
	if o.ExpectedColumns != 0 && width != o.ExpectedColumns {
		return nil, dataErr("expected %d columns, got %d", o.ExpectedColumns, width)
	}
	if o.ExpectedHeaders != nil {
		if len(t.Headers) != len(o.ExpectedHeaders) {
			return nil, dataErr("headers %q differ from the expected headers %q", t.Headers, o.ExpectedHeaders)
		}
		for i, h := range o.ExpectedHeaders {
			if t.Headers[i] != h {
				return nil, dataErr("header %d is %q, expected %q", i, t.Headers[i], h)
			}
		}
	}
	for i, r := range t.Rows {
		if len(r) != width {
			return nil, dataErr("row %d has %d columns, expected %d", i, len(r), width)
		}
	}
	records := make([][]string, 0, len(t.Rows)+1)
	prep := func(src []string) ([]string, error) {
		out := make([]string, len(src))
		for i, c := range src {
			if bounded && len(c) > MaxCSVCellBytes {
				return nil, limitErr("a cell is %d bytes, above the limit of %d", len(c), MaxCSVCellBytes)
			}
			if bounded && !utf8.ValidString(c) {
				return nil, dataErr("a cell is not valid UTF-8")
			}
			if o.NeutralizeFormulas && c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
				c = "'" + c
			}
			out[i] = c
		}
		return out, nil
	}
	if t.Headers != nil {
		h, err := prep(t.Headers)
		if err != nil {
			return nil, err
		}
		records = append(records, h)
	}
	for _, r := range t.Rows {
		p, err := prep(r)
		if err != nil {
			return nil, err
		}
		records = append(records, p)
	}
	eol := "\n"
	if crlf {
		eol = "\r\n"
	}
	var buf bytes.Buffer
	if o.BOM {
		buf.WriteString("\xEF\xBB\xBF")
	}
	// One writer for both quoting policies. It follows the quoting rule of
	// encoding/csv (checked against it by a test) but never rewrites a cell:
	// encoding/csv with UseCRLF drops a lone CR and turns LF into CRLF inside a
	// cell. A cell with a line break is quoted and written as given.
	needsQuotes := func(field string) bool {
		if field == "" {
			return false
		}
		if field == `\.` || strings.ContainsRune(field, comma) || strings.ContainsAny(field, "\"\r\n") {
			return true
		}
		r1, _ := utf8.DecodeRuneInString(field)
		return unicode.IsSpace(r1)
	}
	for _, r := range records {
		for i, c := range r {
			if i > 0 {
				buf.WriteRune(comma)
			}
			if o.Quote == "all" || needsQuotes(c) {
				buf.WriteString(`"` + strings.ReplaceAll(c, `"`, `""`) + `"`)
			} else {
				buf.WriteString(c)
			}
		}
		buf.WriteString(eol)
	}
	if bounded && buf.Len() > MaxOutputBytes {
		return nil, limitErr("output is %d bytes, above the limit of %d", buf.Len(), MaxOutputBytes)
	}
	return buf.Bytes(), nil
}

func cellString(v any) (string, error) {
	switch c := v.(type) {
	case nil:
		return "", nil
	case string:
		return c, nil
	case bool:
		return fmt.Sprint(c), nil
	case json.Number:
		return c.String(), nil
	case float64:
		return json.Number(fmt.Sprint(c)).String(), nil
	}
	return "", dataErr("a cell must be a string, a number, a boolean or null")
}

// TableFromRows builds the table of rows given as arrays or as objects. With
// headers, objects are read by header (an unknown key is refused); without
// headers, objects give the sorted union of their keys, and arrays give a
// header-less table.
func TableFromRows(headers []string, rows []any) (Table, error) {
	t := Table{Headers: headers}
	keys := map[string]bool{}
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			for k := range m {
				keys[k] = true
			}
		}
	}
	if len(headers) == 0 && len(keys) > 0 {
		for k := range keys {
			t.Headers = append(t.Headers, k)
		}
		sort.Strings(t.Headers)
	}
	known := map[string]bool{}
	for _, h := range t.Headers {
		known[h] = true
	}
	for i, r := range rows {
		var out []string
		switch row := r.(type) {
		case []any:
			for _, c := range row {
				s, err := cellString(c)
				if err != nil {
					return Table{}, dataErr("rows[%d]: %v", i, err)
				}
				out = append(out, s)
			}
		case map[string]any:
			for k := range row {
				if !known[k] {
					return Table{}, dataErr("rows[%d] has the key %q, which is not a header", i, k)
				}
			}
			for _, h := range t.Headers {
				s, err := cellString(row[h])
				if err != nil {
					return Table{}, dataErr("rows[%d].%s: %v", i, h, err)
				}
				out = append(out, s)
			}
		default:
			return Table{}, dataErr("rows[%d] must be an array or an object", i)
		}
		t.Rows = append(t.Rows, out)
	}
	return t, nil
}

// RenderCSV renders data and options (already decoded JSON). Unlike the
// historical create, the default line ending is CRLF (RFC 4180).
func RenderCSV(data, options any) ([]byte, error) {
	var in CSVInput
	if err := validateAndDecode(OutputCSV, data, &in); err != nil {
		return nil, err
	}
	var o CSVOptions
	if options != nil {
		if err := validateAndDecode("csv.options", options, &o); err != nil {
			return nil, err
		}
	}
	if o.LineEnding == "" {
		o.LineEnding = "crlf"
	}
	t, err := TableFromRows(in.Headers, in.Rows)
	if err != nil {
		return nil, err
	}
	return WriteCSV(t, o)
}

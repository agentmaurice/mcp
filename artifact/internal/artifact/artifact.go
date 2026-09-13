package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Manifest struct {
	Format          string `json:"format"`
	MimeType        string `json:"mime_type"`
	Bytes           int    `json:"bytes"`
	SHA256          string `json:"sha256"`
	Renderer        string `json:"renderer"`
	RendererVersion string `json:"renderer_version"`
}
type Result struct {
	ContentBase64 string   `json:"content_base64"`
	Preview       string   `json:"preview,omitempty"`
	Manifest      Manifest `json:"manifest"`
}
type Patch struct {
	Op    string `json:"op"`
	Find  string `json:"find,omitempty"`
	Value string `json:"value"`
}

func Create(format string, content interface{}) (Result, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	var payload []byte
	var err error
	switch format {
	case "markdown":
		payload, err = textContent(content)
	case "html":
		payload, err = textContent(content)
	case "json":
		payload, err = json.MarshalIndent(content, "", "  ")
	case "csv":
		payload, err = csvContent(content)
	case "pdf":
		text, textErr := textContent(content)
		if textErr != nil {
			return Result{}, textErr
		}
		payload = simplePDF(stripMarkup(string(text)))
	default:
		return Result{}, fmt.Errorf("unsupported format %q", format)
	}
	if err != nil {
		return Result{}, err
	}
	return packageResult(format, payload), nil
}

func ApplyPatch(format, content string, patches []Patch) (Result, error) {
	if format != "markdown" && format != "html" && format != "json" {
		return Result{}, fmt.Errorf("patch supports markdown, html and json")
	}
	updated := content
	for index, patch := range patches {
		switch patch.Op {
		case "append":
			updated += patch.Value
		case "prepend":
			updated = patch.Value + updated
		case "replace":
			if patch.Find == "" {
				return Result{}, fmt.Errorf("patch %d requires find", index)
			}
			if !strings.Contains(updated, patch.Find) {
				return Result{}, fmt.Errorf("patch %d target was not found", index)
			}
			updated = strings.Replace(updated, patch.Find, patch.Value, 1)
		default:
			return Result{}, fmt.Errorf("patch %d uses unsupported op %q", index, patch.Op)
		}
	}
	if format == "json" && !json.Valid([]byte(updated)) {
		return Result{}, fmt.Errorf("patched JSON is invalid")
	}
	return packageResult(format, []byte(updated)), nil
}

func Render(format, content, outputFormat string) (Result, error) {
	if outputFormat != "pdf" {
		return Result{}, fmt.Errorf("v0.1 preview format must be pdf")
	}
	if format != "markdown" && format != "html" {
		return Result{}, fmt.Errorf("only markdown and html can be rendered")
	}
	return packageResult("pdf", simplePDF(stripMarkup(content))), nil
}

func Inspect(format string, payload []byte) (Manifest, error) {
	format = strings.ToLower(format)
	switch format {
	case "json":
		if !json.Valid(payload) {
			return Manifest{}, fmt.Errorf("invalid JSON")
		}
	case "pdf":
		if !bytes.HasPrefix(payload, []byte("%PDF-1.4")) {
			return Manifest{}, fmt.Errorf("invalid PDF header")
		}
	case "markdown", "html", "csv":
	default:
		return Manifest{}, fmt.Errorf("unsupported format %q", format)
	}
	return manifest(format, payload), nil
}

func DecodeContent(encoded string) ([]byte, error) {
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 content: %w", err)
	}
	return payload, nil
}

func packageResult(format string, payload []byte) Result {
	preview := string(payload)
	if len(preview) > 240 {
		preview = preview[:240]
	}
	if format == "pdf" {
		preview = "PDF preview generated"
	}
	return Result{ContentBase64: base64.StdEncoding.EncodeToString(payload), Preview: preview, Manifest: manifest(format, payload)}
}
func manifest(format string, payload []byte) Manifest {
	hash := sha256.Sum256(payload)
	return Manifest{Format: format, MimeType: mimeType(format), Bytes: len(payload), SHA256: fmt.Sprintf("%x", hash[:]), Renderer: "agentmaurice-artifact", RendererVersion: "0.1.0"}
}
func mimeType(format string) string {
	switch format {
	case "markdown":
		return "text/markdown"
	case "html":
		return "text/html"
	case "csv":
		return "text/csv"
	case "json":
		return "application/json"
	case "pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}
func textContent(content interface{}) ([]byte, error) {
	value, ok := content.(string)
	if !ok {
		return nil, fmt.Errorf("content must be a string")
	}
	return []byte(value), nil
}

func csvContent(content interface{}) ([]byte, error) {
	rows, ok := content.([]interface{})
	if !ok {
		return nil, fmt.Errorf("CSV content must be an array of objects")
	}
	columnsSet := map[string]bool{}
	objects := make([]map[string]interface{}, 0, len(rows))
	for _, item := range rows {
		object, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("CSV rows must be objects")
		}
		objects = append(objects, object)
		for key := range object {
			columnsSet[key] = true
		}
	}
	columns := make([]string, 0, len(columnsSet))
	for key := range columnsSet {
		columns = append(columns, key)
	}
	sort.Strings(columns)
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	_ = writer.Write(columns)
	for _, object := range objects {
		record := make([]string, len(columns))
		for index, column := range columns {
			record[index] = fmt.Sprint(object[column])
		}
		_ = writer.Write(record)
	}
	writer.Flush()
	return buffer.Bytes(), writer.Error()
}

var tags = regexp.MustCompile(`<[^>]+>`)

func stripMarkup(value string) string {
	value = tags.ReplaceAllString(value, "")
	replacer := strings.NewReplacer("# ", "", "## ", "", "**", "", "__", "")
	return replacer.Replace(value)
}

func simplePDF(text string) []byte {
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	if len(lines) > 48 {
		lines = lines[:48]
	}
	var stream strings.Builder
	stream.WriteString("BT /F1 11 Tf 50 790 Td 14 TL ")
	for index, line := range lines {
		if index > 0 {
			stream.WriteString("T* ")
		}
		line = strings.ReplaceAll(line, "\\", "\\\\")
		line = strings.ReplaceAll(line, "(", "\\(")
		line = strings.ReplaceAll(line, ")", "\\)")
		if len(line) > 100 {
			line = line[:100]
		}
		stream.WriteString("(" + line + ") Tj ")
	}
	stream.WriteString("ET")
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream.String()), stream.String()), "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for index, object := range objects {
		offsets[index] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return pdf.Bytes()
}

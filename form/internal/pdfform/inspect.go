// Package pdfform inspects and fills the AcroForm fields of an existing PDF
// with pdfcpu (Apache-2.0, https://github.com/pdfcpu/pdfcpu) and checks that a
// filled file only differs from its source by the requested values.
package pdfform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Stable error codes of the package.
const (
	CodeInvalidPDF      = "invalid_pdf"
	CodeNoForm          = "form_no_fields"
	CodeEncrypted       = "form_encrypted"
	CodeSigned          = "form_signed"
	CodeXFA             = "form_xfa_unsupported"
	CodeUnknownField    = "form_unknown_field"
	CodeTypeMismatch    = "form_type_mismatch"
	CodeUnknownOption   = "form_unknown_option"
	CodeValueTooLong    = "form_value_too_long"
	CodeFieldReadOnly   = "form_field_readonly"
	CodeInvalidValue    = "form_invalid_value"
	CodeDuplicateField  = "form_duplicate_field"
	CodeFlattenFailed   = "form_flatten_failed"
	CodeIntegrityFailed = "form_integrity_failed"
	CodeFillFailed      = "form_fill_failed"
	CodeCancelled       = "form_cancelled"
)

// Field types reported by Inspect.
const (
	TypeText      = "text"
	TypeCheckbox  = "checkbox"
	TypeRadio     = "radio"
	TypeListbox   = "listbox"
	TypeCombobox  = "combobox"
	TypeSignature = "signature"
	TypeButton    = "button"
)

// Error is a refusal with a stable code and, when relevant, the field name.
type Error struct {
	Code    string
	Message string
	Field   string
}

func cancelled() *Error {
	return &Error{Code: CodeCancelled, Message: "the request was cancelled before the PDF was processed"}
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, field, format string, args ...any) *Error {
	return &Error{Code: code, Field: field, Message: fmt.Sprintf(format, args...)}
}

// Field is one terminal AcroForm field.
type Field struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype,omitempty"` // "date" for a formatted date text field
	Value     any      `json:"value"`             // string, bool (checkbox), []string (multiple list), nil
	Options   []string `json:"options,omitempty"`
	ReadOnly  bool     `json:"read_only"`
	Required  bool     `json:"required"`
	MaxLength int      `json:"max_length,omitempty"`
	Multiline bool     `json:"multiline,omitempty"`
	Editable  bool     `json:"editable,omitempty"`
	Multiple  bool     `json:"multiple,omitempty"`
	Signed    bool     `json:"signed,omitempty"`
	Tooltip   string   `json:"tooltip,omitempty"`
	Pages     []int    `json:"pages,omitempty"`
}

// Doc is the inspection of a PDF.
type Doc struct {
	Pages      int     `json:"pages"`
	Encrypted  bool    `json:"encrypted"`
	XFA        bool    `json:"xfa"`
	Signed     bool    `json:"signed"`
	Signatures int     `json:"signature_fields"`
	Fields     []Field `json:"fields"`
	Unnamed    int     `json:"unnamed_fields_ignored,omitempty"`

	pages  []pageDigest      // digests for the integrity check (Fill only)
	apSHA  map[string]string // by field: deep digest of every widget appearance (N, D, R)
	apDraw map[string]string // by field: digest of the appearances that a flatten would draw
}

// Refusal returns the error a fill must raise for this document, or nil.
func (d *Doc) Refusal() *Error {
	switch {
	case d.Encrypted:
		return errf(CodeEncrypted, "", "the PDF is encrypted: remove the encryption first, it is never decrypted or re-encrypted here")
	case d.Signed:
		return errf(CodeSigned, "", "the PDF carries a digital signature: filling would invalidate it, so the file is left untouched")
	case d.XFA:
		return errf(CodeXFA, "", "the PDF is an XFA form: only AcroForm fields can be filled")
	}
	return nil
}

// Find returns the field with this fully qualified name.
func (d *Doc) Find(name string) (*Field, bool) {
	for i := range d.Fields {
		if d.Fields[i].Name == name {
			return &d.Fields[i], true
		}
	}
	return nil, false
}

func newConf(maxBytes int) *model.Configuration {
	conf := model.NewStatelessConfiguration()
	conf.Offline = true
	if maxBytes > 0 {
		conf.Limits.MaxInputBytes = int64(maxBytes)
	}
	return conf
}

func looksEncryptedError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "password") || strings.Contains(s, "encrypt") || strings.Contains(s, "decrypt")
}

// Inspect reads the PDF and lists its form fields. The checks for encryption,
// XFA and signatures never depend on the file being fillable.
func Inspect(ctx context.Context, data []byte, maxBytes int) (*Doc, error) {
	return inspect(ctx, data, maxBytes, false)
}

// inspect is Inspect; with digests it also computes the page and appearance
// digests that the integrity check of a fill relies on.
func inspect(ctx context.Context, data []byte, maxBytes int, digests bool) (doc *Doc, err error) {
	defer func() {
		if r := recover(); r != nil {
			doc, err = nil, errf(CodeInvalidPDF, "", "the PDF could not be parsed")
		}
	}()
	if !bytes.HasPrefix(bytes.TrimLeft(data[:min(len(data), 1024)], "\x00\t\r\n "), []byte("%PDF-")) {
		return nil, errf(CodeInvalidPDF, "", "the input is not a PDF file")
	}
	conf := newConf(maxBytes)
	conf.Cmd = model.LISTFORMFIELDS
	pctx, err := api.ReadContext(ctx, bytes.NewReader(data), conf)
	if err != nil {
		if ctx.Err() != nil {
			return nil, cancelled()
		}
		if looksEncryptedError(err) {
			return &Doc{Encrypted: true}, nil
		}
		return nil, errf(CodeInvalidPDF, "", "the PDF could not be parsed: %v", firstLine(err))
	}
	// Encryption comes from the trailer /Encrypt as read by pdfcpu, never from a
	// substring of the file.
	doc = &Doc{Encrypted: pctx.Encrypt != nil}
	if doc.Encrypted {
		// Never go further than reporting: the file is refused.
		return doc, nil
	}
	xt := pctx.XRefTable
	// XFA and signatures are detected on the raw catalog, before the strict
	// validation: an XFA or signed file that pdfcpu cannot validate must still
	// be reported (and refused) as such, not as a corrupt PDF.
	acro := rawAcroForm(xt)
	doc.XFA = acro != nil && acro.HasEntry("XFA")
	doc.Signed = xt.RootDict != nil && xt.RootDict.HasEntry("Perms")
	if i := acro.IntEntry("SigFlags"); i != nil && *i&3 != 0 {
		doc.Signed = true
	}
	if err := api.ValidateContext(ctx, pctx); err != nil {
		if ctx.Err() != nil {
			return nil, cancelled()
		}
		if doc.XFA || doc.Signed {
			return doc, nil
		}
		return nil, errf(CodeInvalidPDF, "", "the PDF does not validate: %v", firstLine(err))
	}
	doc.Pages = xt.PageCount
	if digests {
		doc.pages = pageDigests(ctx, xt)
	}
	doc.XFA = doc.XFA || (xt.Form != nil && xt.Form.HasEntry("XFA"))
	doc.Signed = doc.Signed || xt.SignatureExist || xt.AppendOnly
	if xt.Form == nil {
		return doc, nil
	}
	walked, err := walkFields(xt)
	if err != nil {
		return nil, errf(CodeInvalidPDF, "", "the field tree could not be read: %v", firstLine(err))
	}
	exported, err := exportFields(ctx, data, conf)
	if err != nil {
		if ctx.Err() != nil {
			return nil, cancelled()
		}
		return nil, errf(CodeInvalidPDF, "", "the field values could not be read: %v", firstLine(err))
	}
	if digests {
		if doc.apSHA, doc.apDraw, err = appearanceDigests(xt, walked); err != nil {
			return nil, errf(CodeIntegrityFailed, "", "the widget appearances cannot be verified: %v", firstLine(err))
		}
	}
	seen := map[string]bool{}
	for _, w := range walked {
		if w.name == "" {
			doc.Unnamed++
			continue
		}
		f := Field{Name: w.name, ReadOnly: w.flags&flagReadOnly != 0, Required: w.flags&flagRequired != 0, Tooltip: w.tooltip}
		switch {
		case w.ft == "Sig":
			f.Type, f.Signed = TypeSignature, w.signed
			doc.Signatures++
			if w.signed {
				doc.Signed = true
			}
		case w.ft == "Btn" && w.flags&flagPushbutton != 0:
			f.Type = TypeButton
		default:
			e, ok := exported[w.name]
			if !ok {
				continue
			}
			f = e.merge(f)
			f.MaxLength = w.maxLen
			if f.Type == TypeCombobox {
				f.Editable = w.flags&flagEdit != 0 // pdfcpu does not report it
			}
			if f.Type != TypeText {
				f.MaxLength = 0
			}
		}
		if seen[w.name] {
			continue
		}
		seen[w.name] = true
		doc.Fields = append(doc.Fields, f)
	}
	sort.SliceStable(doc.Fields, func(i, j int) bool { return firstPage(doc.Fields[i]) < firstPage(doc.Fields[j]) })
	return doc, nil
}

// rawAcroForm returns the AcroForm dictionary of the catalog before validation
// (nil when absent or unreadable).
func rawAcroForm(xt *model.XRefTable) types.Dict {
	if xt.RootDict == nil {
		return nil
	}
	o, ok := xt.RootDict.Find("AcroForm")
	if !ok {
		return nil
	}
	d, err := xt.DereferenceDict(o)
	if err != nil {
		return nil
	}
	return d
}

func firstPage(f Field) int {
	if len(f.Pages) == 0 {
		return 0
	}
	return f.Pages[0]
}

func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

const (
	flagReadOnly   = 1 << 0
	flagRequired   = 1 << 1
	flagPushbutton = 1 << 16
	flagEdit       = 1 << 18
)

type walked struct {
	name, ft, tooltip string
	flags, maxLen     int
	signed            bool
	widgets           []types.Dict
}

// walkFields walks the AcroForm field tree and returns its terminal fields
// with their fully qualified name (the same one pdfcpu matches on), type,
// flags and maximum length (all inherited from the parents).
func walkFields(xt *model.XRefTable) ([]walked, error) {
	roots, err := xt.DereferenceArray(xt.Form["Fields"])
	if err != nil {
		return nil, err
	}
	var out []walked
	visited := map[int]bool{}
	var walk func(o types.Object, parentName, ft string, ff, maxLen, depth int) error
	walk = func(o types.Object, parentName, ft string, ff, maxLen, depth int) error {
		if depth > 64 {
			return errors.New("field tree too deep")
		}
		if ir, ok := o.(types.IndirectRef); ok {
			n := ir.ObjectNumber.Value()
			if visited[n] {
				return errors.New("field tree has a cycle")
			}
			visited[n] = true
			defer delete(visited, n)
		}
		d, err := xt.DereferenceDict(o)
		if err != nil || d == nil {
			return err
		}
		name := parentName
		if s, _, err := xt.DereferenceStringEntry(d, "T"); err != nil {
			return err
		} else if s != nil && *s != "" {
			if name != "" {
				name += "."
			}
			name += *s
		}
		if n, _, err := xt.DereferenceNameEntry(d, "FT"); err != nil {
			return err
		} else if n != nil {
			ft = n.Value()
		}
		if i, _, err := xt.DereferenceIntegerEntry(d, "Ff"); err != nil {
			return err
		} else if i != nil {
			ff = i.Value()
		}
		if i, _, err := xt.DereferenceIntegerEntry(d, "MaxLen"); err != nil {
			return err
		} else if i != nil {
			maxLen = i.Value()
		}
		var kids types.Array
		if k, ok := d.Find("Kids"); ok {
			if kids, err = xt.DereferenceArray(k); err != nil {
				return err
			}
		}
		// A kid with its own T is a child field; a kid without is a widget of this field.
		hasFieldKid := false
		for _, k := range kids {
			kd, err := xt.DereferenceDict(k)
			if err != nil {
				return err
			}
			if kd != nil && kd.HasEntry("T") {
				hasFieldKid = true
			}
		}
		if hasFieldKid {
			for _, k := range kids {
				if err := walk(k, name, ft, ff, maxLen, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		w := walked{name: name, ft: ft, flags: ff, maxLen: maxLen}
		if len(kids) == 0 || d.HasEntry("Subtype") {
			w.widgets = append(w.widgets, d)
		}
		for _, k := range kids {
			if kd, err := xt.DereferenceDict(k); err == nil && kd != nil {
				w.widgets = append(w.widgets, kd)
			}
		}
		if s, _, err := xt.DereferenceStringEntry(d, "TU"); err == nil && s != nil {
			w.tooltip = *s
		}
		if ft == "Sig" {
			if v, ok := d.Find("V"); ok {
				if vv, err := xt.Dereference(v); err == nil && vv != nil {
					w.signed = true
				}
			}
		}
		out = append(out, w)
		return nil
	}
	for _, r := range roots {
		if err := walk(r, "", "", 0, 0, 0); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// exported is the pdfcpu view of a field (values, options, kind).
type exported struct {
	f Field
}

func (e exported) merge(base Field) Field {
	f := e.f
	f.ReadOnly = base.ReadOnly || e.f.ReadOnly
	f.Required = base.Required
	f.Tooltip = base.Tooltip
	return f
}

func exportFields(ctx context.Context, data []byte, conf *model.Configuration) (map[string]exported, error) {
	out := map[string]exported{}
	g, err := api.ExportForm(ctx, bytes.NewReader(data), "form", conf)
	if err != nil {
		if errors.Is(err, api.ErrNoFormFieldsAffected) {
			return out, nil
		}
		return nil, err
	}
	for _, fm := range g.Forms {
		for _, t := range fm.TextFields {
			if t != nil {
				out[t.Name] = exported{Field{Name: t.Name, Type: TypeText, Value: t.Value, ReadOnly: t.Locked, MaxLength: t.MaxLen, Multiline: t.Multiline, Pages: t.Pages}}
			}
		}
		for _, t := range fm.DateFields {
			if t != nil {
				out[t.Name] = exported{Field{Name: t.Name, Type: TypeText, Subtype: "date", Value: t.Value, ReadOnly: t.Locked, Pages: t.Pages}}
			}
		}
		for _, t := range fm.CheckBoxes {
			if t != nil {
				out[t.Name] = exported{Field{Name: t.Name, Type: TypeCheckbox, Value: t.Value, ReadOnly: t.Locked, Pages: t.Pages}}
			}
		}
		for _, t := range fm.RadioButtonGroups {
			if t != nil {
				out[t.Name] = exported{Field{Name: t.Name, Type: TypeRadio, Value: t.Value, Options: t.Options, ReadOnly: t.Locked, Pages: t.Pages}}
			}
		}
		for _, t := range fm.ComboBoxes {
			if t != nil {
				out[t.Name] = exported{Field{Name: t.Name, Type: TypeCombobox, Value: t.Value, Options: t.Options, Editable: t.Editable, ReadOnly: t.Locked, Pages: t.Pages}}
			}
		}
		for _, t := range fm.ListBoxes {
			if t != nil {
				var val any
				if t.Multi {
					vals := t.Values
					if vals == nil {
						vals = []string{}
					}
					val = vals
				} else if len(t.Values) > 0 {
					val = t.Values[0]
				} else {
					val = ""
				}
				out[t.Name] = exported{Field{Name: t.Name, Type: TypeListbox, Value: val, Options: t.Options, Multiple: t.Multi, ReadOnly: t.Locked, Pages: t.Pages}}
			}
		}
	}
	return out, nil
}

package pdfform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
)

// Integrity is the proof attached to a filled file.
type Integrity struct {
	InputSHA256         string   `json:"input_sha256"`
	OutputSHA256        string   `json:"output_sha256"`
	Validated           bool     `json:"validated"`
	PagesBefore         int      `json:"pages_before"`
	PagesAfter          int      `json:"pages_after"`
	Unchanged           bool     `json:"unchanged"` // every requested value was already in place: the file is returned as is
	FieldsFilled        int      `json:"fields_filled"`
	ReadBackVerified    bool     `json:"read_back_verified"`
	FieldsUntouched     int      `json:"fields_untouched"`
	UntouchedUnchanged  bool     `json:"untouched_fields_unchanged"`
	PagesContentIntact  bool     `json:"pages_content_unchanged"`
	Flattened           bool     `json:"flattened"`
	FlattenedWidgets    int      `json:"flattened_widgets,omitempty"`
	SignatureStatus     string   `json:"signature_status"`
	FilledFieldNames    []string `json:"filled_fields"`
	ChecksPerformedList []string `json:"checks"`
}

// SHA returns the hex SHA-256 of b.
func SHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Fill writes values (field name -> value) into the PDF as one atomic batch:
// either every value is written and verified, or an error is returned and no
// output exists. Encrypted, signed and XFA files are refused before anything
// is written.
func Fill(ctx context.Context, data []byte, values map[string]any, flatten bool, maxBytes int) (out []byte, in *Integrity, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, in, err = nil, nil, errf(CodeFillFailed, "", "the PDF could not be filled")
		}
	}()
	before, err := inspect(ctx, data, maxBytes, true)
	if err != nil {
		return nil, nil, err
	}
	if e := before.Refusal(); e != nil {
		return nil, nil, e
	}
	if len(before.Fields) == 0 {
		return nil, nil, errf(CodeNoForm, "", "the PDF has no fillable AcroForm field")
	}
	if len(values) == 0 {
		return nil, nil, errf(CodeInvalidValue, "", "values is empty")
	}
	expected, err := validate(before, values)
	if err != nil {
		return nil, nil, err
	}
	src := data
	if flatten {
		// pdfcpu leaves a field alone when its value does not change, so its
		// appearance would not be regenerated: a stale /AP would then be frozen
		// into the page. Such fields are first set to another value, then to the
		// requested one, which makes pdfcpu draw a fresh appearance.
		if src, err = regenerateUnchanged(ctx, data, before, expected, maxBytes); err != nil {
			return nil, nil, err
		}
	}
	filled, err := runFill(ctx, src, before, expected, maxBytes)
	unchanged := false
	if errors.Is(err, api.ErrNoFormFieldsAffected) {
		filled, err, unchanged = append([]byte(nil), data...), nil, true
	}
	if err != nil {
		return nil, nil, err
	}
	after, err := inspect(ctx, filled, maxBytes, true)
	if err != nil {
		return nil, nil, errf(CodeIntegrityFailed, "", "the produced PDF cannot be read back: %v", err)
	}
	in = &Integrity{Unchanged: unchanged, InputSHA256: SHA(data), PagesBefore: before.Pages, PagesAfter: after.Pages, Validated: true, SignatureStatus: "none_before_none_after"}
	if err := compare(before, after, expected, in); err != nil {
		return nil, nil, err
	}
	in.ChecksPerformedList = []string{"pdfcpu_validation", "page_count", "read_back_values", "untouched_fields", "page_content", "no_signature"}
	if flatten {
		flat, n, drawn, err := doFlatten(ctx, filled, maxBytes)
		if err != nil {
			return nil, nil, err
		}
		// What was drawn is what the filled file showed, and, for the fields
		// that were not requested, what the original showed.
		for name, d := range after.apDraw {
			if drawn[name] != d {
				return nil, nil, errf(CodeIntegrityFailed, name, "the appearance drawn for field %q is not its appearance", name)
			}
		}
		for name := range before.apDraw {
			if _, touched := expected[name]; !touched && drawn[name] != before.apDraw[name] {
				return nil, nil, errf(CodeIntegrityFailed, name, "the appearance drawn for field %q is not its original appearance", name)
			}
		}
		fdoc, err := inspect(ctx, flat, maxBytes, true)
		if err != nil {
			return nil, nil, errf(CodeIntegrityFailed, "", "the flattened PDF cannot be read back: %v", err)
		}
		if fdoc.Pages != before.Pages || len(fdoc.Fields) != 0 || fdoc.Signed || fdoc.Encrypted || fdoc.XFA {
			return nil, nil, errf(CodeIntegrityFailed, "", "the flattened PDF does not match the expectations (pages %d/%d, fields left %d)", fdoc.Pages, before.Pages, len(fdoc.Fields))
		}
		if err := flattenedPages(before.pages, fdoc.pages); err != nil {
			return nil, nil, err
		}
		filled = flat
		// A flattened file always differs from its input, even when every
		// requested value was already in place.
		in.Unchanged = false
		in.Flattened, in.FlattenedWidgets, in.PagesAfter = true, n, fdoc.Pages
		in.PagesContentIntact = false // flattening appends the appearances to the page content on purpose
		in.ChecksPerformedList = append(in.ChecksPerformedList, "flatten_no_fields_left")
	}
	in.OutputSHA256 = SHA(filled)
	return filled, in, nil
}

type expectedValue struct {
	typ     string
	text    string
	boolean bool
	list    []string
}

// validate checks the whole batch against the inspected fields and returns the
// normalized values. The first refusal names the field.
func validate(doc *Doc, values map[string]any) (map[string]expectedValue, error) {
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	sort.Strings(names)
	out := map[string]expectedValue{}
	for _, name := range names {
		f, ok := doc.Find(name)
		if !ok {
			return nil, errf(CodeUnknownField, name, "no field named %q in the PDF", name)
		}
		v, err := validateOne(f, values[name])
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

func validateOne(f *Field, raw any) (expectedValue, error) {
	ev := expectedValue{typ: f.Type}
	if f.Type == TypeSignature || f.Type == TypeButton {
		return ev, errf(CodeTypeMismatch, f.Name, "field %q is a %s field and cannot be filled", f.Name, f.Type)
	}
	if f.ReadOnly {
		return ev, errf(CodeFieldReadOnly, f.Name, "field %q is read-only", f.Name)
	}
	asString := func() (string, error) {
		s, ok := raw.(string)
		if !ok {
			return "", errf(CodeTypeMismatch, f.Name, "field %q (%s) expects a string value", f.Name, f.Type)
		}
		return s, nil
	}
	switch f.Type {
	case TypeText:
		s, err := asString()
		if err != nil {
			return ev, err
		}
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return ev, errf(CodeInvalidValue, f.Name, "field %q: the value is not valid text", f.Name)
		}
		if f.MaxLength > 0 && utf8.RuneCountInString(s) > f.MaxLength {
			return ev, errf(CodeValueTooLong, f.Name, "field %q accepts at most %d characters, got %d", f.Name, f.MaxLength, utf8.RuneCountInString(s))
		}
		if strings.ContainsRune(s, '\r') {
			// Line breaks are written as a single LF: a CR would not read back identically.
			return ev, errf(CodeInvalidValue, f.Name, "field %q: use \\n for a line break, a carriage return is not accepted", f.Name)
		}
		if !f.Multiline && strings.ContainsRune(s, '\n') {
			return ev, errf(CodeInvalidValue, f.Name, "field %q is a single-line field: the value cannot contain a line break", f.Name)
		}
		ev.text = s
	case TypeCheckbox:
		switch x := raw.(type) {
		case bool:
			ev.boolean = x
		case string:
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "true", "yes", "on", "1":
				ev.boolean = true
			case "false", "no", "off", "0":
				ev.boolean = false
			default:
				return ev, errf(CodeTypeMismatch, f.Name, "field %q (checkbox) expects true or false", f.Name)
			}
		default:
			return ev, errf(CodeTypeMismatch, f.Name, "field %q (checkbox) expects true or false", f.Name)
		}
	case TypeRadio:
		s, err := asString()
		if err != nil {
			return ev, err
		}
		if !member(s, f.Options) {
			return ev, errf(CodeUnknownOption, f.Name, "field %q: %q is not one of the options %v", f.Name, s, f.Options)
		}
		ev.text = s
	case TypeCombobox:
		s, err := asString()
		if err != nil {
			return ev, err
		}
		if s != "" && !member(s, f.Options) {
			// pdfcpu drops a value that is not an option, even for an editable combo box.
			return ev, errf(CodeUnknownOption, f.Name, "field %q: %q is not one of the options %v", f.Name, s, f.Options)
		}
		ev.text = s
	case TypeListbox:
		var items []string
		switch x := raw.(type) {
		case string:
			if x != "" {
				items = []string{x}
			}
		case []any:
			for _, e := range x {
				s, ok := e.(string)
				if !ok {
					return ev, errf(CodeTypeMismatch, f.Name, "field %q (listbox) expects a string or a list of strings", f.Name)
				}
				items = append(items, s)
			}
		case []string:
			items = append(items, x...)
		default:
			return ev, errf(CodeTypeMismatch, f.Name, "field %q (listbox) expects a string or a list of strings", f.Name)
		}
		if len(items) > 1 && !f.Multiple {
			return ev, errf(CodeInvalidValue, f.Name, "field %q accepts a single selection", f.Name)
		}
		for _, s := range items {
			if !member(s, f.Options) {
				return ev, errf(CodeUnknownOption, f.Name, "field %q: %q is not one of the options %v", f.Name, s, f.Options)
			}
		}
		ev.list = items
	default:
		return ev, errf(CodeTypeMismatch, f.Name, "field %q has an unsupported type", f.Name)
	}
	return ev, nil
}

func member(s string, opts []string) bool {
	for _, o := range opts {
		if o == s {
			return true
		}
	}
	return false
}

func runFill(ctx context.Context, data []byte, doc *Doc, exp map[string]expectedValue, maxBytes int) ([]byte, error) {
	var fm form.Form
	for name, v := range exp {
		f, _ := doc.Find(name)
		switch v.typ {
		case TypeText:
			if f.Subtype == "date" {
				fm.DateFields = append(fm.DateFields, &form.DateField{Name: name, Value: v.text})
			} else {
				fm.TextFields = append(fm.TextFields, &form.TextField{Name: name, Value: v.text})
			}
		case TypeCheckbox:
			fm.CheckBoxes = append(fm.CheckBoxes, &form.CheckBox{Name: name, Value: v.boolean})
		case TypeRadio:
			fm.RadioButtonGroups = append(fm.RadioButtonGroups, &form.RadioButtonGroup{Name: name, Value: v.text, Options: f.Options})
		case TypeCombobox:
			fm.ComboBoxes = append(fm.ComboBoxes, &form.ComboBox{Name: name, Value: v.text, Options: f.Options, Editable: f.Editable})
		case TypeListbox:
			fm.ListBoxes = append(fm.ListBoxes, &form.ListBox{Name: name, Values: v.list, Options: f.Options, Multi: f.Multiple})
		}
	}
	payload, err := json.Marshal(form.FormGroup{Forms: []form.Form{fm}})
	if err != nil {
		return nil, errf(CodeFillFailed, "", "the values could not be encoded")
	}
	conf := newConf(maxBytes)
	var buf bytes.Buffer
	if err := api.FillForm(ctx, bytes.NewReader(data), bytes.NewReader(payload), &buf, conf); err != nil {
		if errors.Is(err, api.ErrNoFormFieldsAffected) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, cancelled()
		}
		if looksEncryptedError(err) {
			return nil, errf(CodeEncrypted, "", "the PDF is encrypted")
		}
		return nil, errf(CodeFillFailed, "", "pdfcpu could not fill the form: %v", firstLine(err))
	}
	return buf.Bytes(), nil
}

// compare proves the filled file against the source: same pages, every written
// value read back identically, every other field unchanged, no signature.
func compare(before, after *Doc, exp map[string]expectedValue, in *Integrity) error {
	if after.Encrypted || after.XFA || after.Signed {
		return errf(CodeIntegrityFailed, "", "the produced PDF is encrypted, signed or XFA while the source was not")
	}
	if before.Pages != after.Pages {
		return errf(CodeIntegrityFailed, "", "page count changed from %d to %d", before.Pages, after.Pages)
	}
	if len(before.Fields) != len(after.Fields) {
		return errf(CodeIntegrityFailed, "", "field count changed from %d to %d", len(before.Fields), len(after.Fields))
	}
	for name, ev := range exp {
		f, ok := after.Find(name)
		if !ok {
			return errf(CodeIntegrityFailed, name, "field %q disappeared from the produced PDF", name)
		}
		if !readBackEqual(f, ev) {
			return errf(CodeIntegrityFailed, name, "field %q does not read back as written (read %v)", name, f.Value)
		}
		in.FilledFieldNames = append(in.FilledFieldNames, name)
	}
	sort.Strings(in.FilledFieldNames)
	in.FieldsFilled = len(exp)
	in.ReadBackVerified = true
	for i := range before.Fields {
		b := &before.Fields[i]
		if _, touched := exp[b.Name]; touched {
			a, _ := after.Find(b.Name)
			if !sameDefinition(b, a) {
				return errf(CodeIntegrityFailed, b.Name, "field %q changed its definition (type, options or flags)", b.Name)
			}
			continue
		}
		a, ok := after.Find(b.Name)
		if !ok || !reflect.DeepEqual(*b, *a) {
			return errf(CodeIntegrityFailed, b.Name, "field %q was not requested but changed", b.Name)
		}
		if before.apSHA[b.Name] != after.apSHA[b.Name] || before.apDraw[b.Name] != after.apDraw[b.Name] {
			return errf(CodeIntegrityFailed, b.Name, "the appearance of field %q was not requested but changed", b.Name)
		}
		in.FieldsUntouched++
	}
	in.UntouchedUnchanged = true
	if err := samePages(before.pages, after.pages); err != nil {
		return err
	}
	in.PagesContentIntact = true
	return nil
}

func sameDefinition(a, b *Field) bool {
	return a.Type == b.Type && a.Subtype == b.Subtype && reflect.DeepEqual(a.Options, b.Options) &&
		a.ReadOnly == b.ReadOnly && a.Required == b.Required && a.MaxLength == b.MaxLength
}

func readBackEqual(f *Field, ev expectedValue) bool {
	switch ev.typ {
	case TypeText, TypeRadio, TypeCombobox:
		s, _ := f.Value.(string)
		return s == ev.text // strictly identical, no normalization
	case TypeCheckbox:
		b, _ := f.Value.(bool)
		return b == ev.boolean
	case TypeListbox:
		got := []string{}
		switch x := f.Value.(type) {
		case string:
			if x != "" {
				got = []string{x}
			}
		case []string:
			got = append(got, x...)
		}
		want := append([]string{}, ev.list...)
		sort.Strings(got)
		sort.Strings(want)
		return reflect.DeepEqual(got, want)
	}
	return false
}

// regenerateUnchanged returns the PDF in which every requested field whose
// value already equals the requested one holds a different value, so that the
// fill that follows rewrites it (and its appearance). A field for which no
// other value exists cannot have its appearance regenerated: the flatten is
// refused for it.
func regenerateUnchanged(ctx context.Context, data []byte, doc *Doc, exp map[string]expectedValue, maxBytes int) ([]byte, error) {
	alts := map[string]expectedValue{}
	for name, ev := range exp {
		f, _ := doc.Find(name)
		if !readBackEqual(f, ev) {
			continue
		}
		alt, ok := alternative(f, ev)
		if !ok {
			return nil, errf(CodeFlattenFailed, name, "field %q already holds the requested value and its appearance cannot be regenerated: it is not flattened", name)
		}
		alts[name] = alt
	}
	if len(alts) == 0 {
		return data, nil
	}
	out, err := runFill(ctx, data, doc, alts, maxBytes)
	if err != nil {
		var pe *Error
		if errors.As(err, &pe) && pe.Code == CodeFillFailed {
			return nil, errf(CodeFlattenFailed, "", "the appearances could not be regenerated: %s", pe.Message)
		}
		return nil, err
	}
	return out, nil
}

func alternative(f *Field, ev expectedValue) (expectedValue, bool) {
	alt := expectedValue{typ: ev.typ}
	switch ev.typ {
	case TypeText:
		if ev.text != "" {
			return alt, true
		}
		if f.MaxLength == 0 || f.MaxLength >= 1 {
			alt.text = "."
			return alt, true
		}
	case TypeCheckbox:
		alt.boolean = !ev.boolean
		return alt, true
	case TypeRadio, TypeCombobox:
		for _, o := range f.Options {
			if o != ev.text {
				alt.text = o
				return alt, true
			}
		}
		if ev.typ == TypeCombobox && ev.text != "" {
			return alt, true
		}
	case TypeListbox:
		if len(ev.list) > 0 {
			return alt, true
		}
		if len(f.Options) > 0 {
			alt.list = []string{f.Options[0]}
			return alt, true
		}
	}
	return alt, false
}

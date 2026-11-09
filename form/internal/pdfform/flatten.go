package pdfform

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// minScale is the smallest scale a flattened appearance may have.
const minScale = 1e-6

// num writes a number losslessly and without exponent.
func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

const (
	annotHidden = 1 << 1
	annotNoView = 1 << 5
)

// Flatten draws the current appearance of every widget into the page content
// and removes the widgets and the AcroForm dictionary. pdfcpu has no flatten
// API (v0.16.4), so this is done on its public model: the appearance stream of
// a widget is already a Form XObject, it is placed on the page with the
// transformation of the PDF reference (12.5.5, algorithm 8.1). It returns the
// number of widgets removed. Callers must have refused signed, encrypted and
// XFA files before.
func Flatten(ctx context.Context, data []byte, maxBytes int) ([]byte, int, error) {
	out, n, _, err := doFlatten(ctx, data, maxBytes)
	return out, n, err
}

// doFlatten also returns, by field name, the digest of the appearances it drew.
func doFlatten(ctx context.Context, data []byte, maxBytes int) (out []byte, removed int, drawn map[string]string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, removed, drawn, err = nil, 0, nil, errf(CodeFlattenFailed, "", "the PDF could not be flattened")
		}
	}()
	conf := newConf(maxBytes)
	conf.Cmd = model.FILLFORMFIELDS
	pctx, err := api.ReadContext(ctx, bytes.NewReader(data), conf)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, nil, cancelled()
		}
		return nil, 0, nil, errf(CodeFlattenFailed, "", "the PDF could not be read: %v", firstLine(err))
	}
	if err := api.ValidateContext(ctx, pctx); err != nil {
		return nil, 0, nil, errf(CodeFlattenFailed, "", "the PDF does not validate: %v", firstLine(err))
	}
	xt := pctx.XRefTable
	if xt.SignatureExist || xt.AppendOnly || pctx.Encrypt != nil {
		return nil, 0, nil, errf(CodeFlattenFailed, "", "a signed or encrypted PDF is never flattened")
	}
	n := 0
	h := newHasher(xt)
	perField := map[string][]string{}
	for p := 1; p <= xt.PageCount; p++ {
		k, err := flattenPage(ctx, xt, h, perField, p)
		if err != nil {
			var pe *Error
			if errors.As(err, &pe) {
				pe.Message = fmt.Sprintf("page %d: %s", p, pe.Message)
				return nil, 0, nil, pe
			}
			return nil, 0, nil, errf(CodeFlattenFailed, "", "page %d: %v", p, err)
		}
		n += k
	}
	delete(xt.RootDict, "AcroForm")
	xt.Form = nil
	var buf bytes.Buffer
	if err := api.Write(ctx, pctx, &buf, conf); err != nil {
		return nil, 0, nil, errf(CodeFlattenFailed, "", "the flattened PDF could not be written: %v", firstLine(err))
	}
	drawn = map[string]string{}
	for name, l := range perField {
		sort.Strings(l)
		drawn[name] = sum(l...)
	}
	return buf.Bytes(), n, drawn, nil
}

type draw struct {
	ref    types.IndirectRef
	matrix string
}

func flattenPage(ctx context.Context, xt *model.XRefTable, h *hasher, perField map[string][]string, pageNr int) (int, error) {
	pd, _, inh, err := xt.PageDict(ctx, pageNr, false)
	if err != nil {
		return 0, err
	}
	annotsObj, ok := pd.Find("Annots")
	if !ok {
		return 0, nil
	}
	annots, err := xt.DereferenceArray(annotsObj)
	if err != nil {
		return 0, err
	}
	var keep types.Array
	var draws []draw
	removed, visible, blank := 0, 0, 0
	for _, a := range annots {
		d, err := xt.DereferenceDict(a)
		if err != nil {
			return 0, err
		}
		if d == nil || d.NameEntry("Subtype") == nil || *d.NameEntry("Subtype") != "Widget" {
			keep = append(keep, a)
			continue
		}
		removed++
		if _, dr, err := widgetDigests(xt, h, d); err != nil {
			return 0, errf(CodeFlattenFailed, widgetName(xt, d), "field %q: %v", widgetName(xt, d), firstLine(err))
		} else {
			perField[widgetName(xt, d)] = append(perField[widgetName(xt, d)], dr)
		}
		flags := 0
		if i, _, err := xt.DereferenceIntegerEntry(d, "F"); err == nil && i != nil {
			flags = i.Value()
		}
		if flags&(annotHidden|annotNoView) != 0 {
			continue
		}
		if _, ok := d.Find("OC"); ok {
			// Optional content: visibility depends on the layers in force, which a
			// flatten cannot keep. Never make such a widget visible.
			return 0, errf(CodeFlattenFailed, widgetName(xt, d), "field %q has optional content (/OC): it is not flattened", widgetName(xt, d))
		}
		visible++
		ref, nothing, err := appearanceRef(xt, d)
		if err != nil {
			return 0, err
		}
		if ref == nil {
			if !nothing {
				return 0, errf(CodeFlattenFailed, widgetName(xt, d), "field %q has a visible widget without a usable appearance stream: flattening would lose its value", widgetName(xt, d))
			}
			blank++
			continue
		}
		matrix, err := placement(xt, d, *ref)
		if err != nil {
			return 0, errf(CodeFlattenFailed, widgetName(xt, d), "field %q: %v", widgetName(xt, d), err)
		}
		draws = append(draws, draw{*ref, matrix})
	}
	if removed == 0 {
		return 0, nil
	}
	// Every visible widget is either drawn or has nothing to draw (an empty
	// state): none may vanish without a trace.
	if len(draws)+blank != visible {
		return 0, errf(CodeFlattenFailed, "", "drawn widgets (%d) and empty widgets (%d) do not add up to the visible widgets (%d)", len(draws), blank, visible)
	}
	if len(keep) == 0 {
		pd.Delete("Annots")
	} else {
		pd.Update("Annots", keep)
	}
	if len(draws) == 0 {
		return removed, nil
	}
	xo, err := xobjectDict(xt, pd, inh)
	if err != nil {
		return 0, err
	}
	var content bytes.Buffer
	next := 1
	for _, dr := range draws {
		var name string
		for {
			name = fmt.Sprintf("FmFlat%d", next)
			next++
			if !xo.HasEntry(name) {
				break
			}
		}
		xo.Update(name, dr.ref)
		fmt.Fprintf(&content, "q %s cm /%s Do Q\n", dr.matrix, name)
	}
	if err := isolateAndAppend(xt, pd, content.Bytes()); err != nil {
		return 0, err
	}
	return removed, nil
}

// widgetName is the fully qualified name of the field a widget belongs to.
func widgetName(xt *model.XRefTable, w types.Dict) string {
	var parts []string
	d := w
	for depth := 0; d != nil && depth < 32; depth++ {
		if s, _, err := xt.DereferenceStringEntry(d, "T"); err == nil && s != nil && *s != "" {
			parts = append([]string{*s}, parts...)
		}
		p, ok := d.Find("Parent")
		if !ok {
			break
		}
		pd, err := xt.DereferenceDict(p)
		if err != nil {
			break
		}
		d = pd
	}
	return strings.Join(parts, ".")
}

// inheritedValue returns the V entry of a widget or of its ancestors.
func inheritedValue(xt *model.XRefTable, w types.Dict) types.Object {
	d := w
	for depth := 0; d != nil && depth < 32; depth++ {
		if v, ok := d.Find("V"); ok {
			if o, err := xt.Dereference(v); err == nil {
				return o
			}
			return nil
		}
		p, ok := d.Find("Parent")
		if !ok {
			return nil
		}
		pd, err := xt.DereferenceDict(p)
		if err != nil {
			return nil
		}
		d = pd
	}
	return nil
}

// appearanceRef returns the reference of the normal appearance stream in use.
// When there is none, nothing is the reason it is harmless: an empty field, or
// a button state without appearance (an unchecked box).
func appearanceRef(xt *model.XRefTable, w types.Dict) (ref *types.IndirectRef, nothing bool, err error) {
	apObj, ok := w.Find("AP")
	if !ok {
		return nil, valueIsEmpty(xt, w), nil
	}
	ap, err := xt.DereferenceDict(apObj)
	if err != nil {
		return nil, false, err
	}
	n, ok := ap.Find("N")
	if ap == nil || !ok {
		return nil, valueIsEmpty(xt, w), nil
	}
	if ir, ok := n.(types.IndirectRef); ok {
		o, err := xt.Dereference(ir)
		if err != nil {
			return nil, false, err
		}
		if _, isStream := o.(types.StreamDict); isStream {
			return &ir, false, nil
		}
		n = o
	}
	states, ok := n.(types.Dict)
	if !ok {
		return nil, valueIsEmpty(xt, w), nil
	}
	as := w.NameEntry("AS")
	if as == nil {
		return nil, valueIsEmpty(xt, w), nil
	}
	entry, ok := states.Find(*as)
	if !ok {
		// A state without appearance (typically Off) draws nothing.
		return nil, *as == "Off", nil
	}
	ir, ok := entry.(types.IndirectRef)
	if !ok {
		return nil, false, nil
	}
	if o, err := xt.Dereference(ir); err != nil {
		return nil, false, err
	} else if _, isStream := o.(types.StreamDict); !isStream {
		return nil, false, nil
	}
	return &ir, false, nil
}

// emptyText is true for "" and for a UTF-16 string that only holds its byte
// order mark (how pdfcpu stores an empty text).
func emptyText(b []byte) bool {
	return len(b) == 0 || bytes.Equal(b, []byte{0xfe, 0xff}) || bytes.Equal(b, []byte{0xff, 0xfe})
}

// stringBytes returns the bytes a string object stands for: the unescaped
// literal, or the decoded hexadecimal digits. The text "feff" in a literal
// string is four characters, not a byte order mark.
func stringBytes(o types.Object) ([]byte, bool) {
	switch v := o.(type) {
	case types.StringLiteral:
		b, err := types.Unescape(string(v))
		return b, err == nil
	case types.HexLiteral:
		h := string(v)
		if len(h)%2 == 1 {
			h += "0"
		}
		b, err := hex.DecodeString(h)
		return b, err == nil
	}
	return nil, false
}

// valueIsEmpty is true when the field has no value to lose.
func valueIsEmpty(xt *model.XRefTable, w types.Dict) bool {
	switch v := inheritedValue(xt, w).(type) {
	case nil:
		return true
	case types.StringLiteral, types.HexLiteral:
		b, ok := stringBytes(v)
		return ok && emptyText(b)
	case types.Name:
		return v == "Off" || v == ""
	case types.Array:
		return len(v) == 0
	}
	return false
}

func numbers(xt *model.XRefTable, o types.Object, n int) ([]float64, error) {
	a, err := xt.DereferenceArray(o)
	if err != nil {
		return nil, err
	}
	if len(a) != n {
		return nil, fmt.Errorf("expected %d numbers, got %d", n, len(a))
	}
	out := make([]float64, n)
	for i, e := range a {
		f, err := xt.DereferenceNumber(e)
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}

// placement computes the matrix that maps the transformed BBox of the
// appearance stream onto the widget rectangle.
func placement(xt *model.XRefTable, w types.Dict, ref types.IndirectRef) (string, error) {
	rectObj, ok := w.Find("Rect")
	if !ok {
		return "", fmt.Errorf("widget without Rect")
	}
	r, err := numbers(xt, rectObj, 4)
	if err != nil {
		return "", fmt.Errorf("Rect: %w", err)
	}
	sd, _, err := xt.DereferenceStreamDict(ref)
	if err != nil || sd == nil {
		return "", fmt.Errorf("appearance stream unreadable")
	}
	bbox := []float64{0, 0, 1, 1}
	if o, ok := sd.Dict.Find("BBox"); ok {
		if bbox, err = numbers(xt, o, 4); err != nil {
			return "", fmt.Errorf("BBox: %w", err)
		}
	}
	m := []float64{1, 0, 0, 1, 0, 0}
	if o, ok := sd.Dict.Find("Matrix"); ok {
		if m, err = numbers(xt, o, 6); err != nil {
			return "", fmt.Errorf("Matrix: %w", err)
		}
	}
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, c := range [][2]float64{{bbox[0], bbox[1]}, {bbox[2], bbox[1]}, {bbox[0], bbox[3]}, {bbox[2], bbox[3]}} {
		x := m[0]*c[0] + m[2]*c[1] + m[4]
		y := m[1]*c[0] + m[3]*c[1] + m[5]
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}
	rx0, rx1 := math.Min(r[0], r[2]), math.Max(r[0], r[2])
	ry0, ry1 := math.Min(r[1], r[3]), math.Max(r[1], r[3])
	if maxX-minX <= 0 || maxY-minY <= 0 || rx1-rx0 <= 0 || ry1-ry0 <= 0 {
		return "", fmt.Errorf("degenerate appearance box")
	}
	sx, sy := (rx1-rx0)/(maxX-minX), (ry1-ry0)/(maxY-minY)
	tx, ty := rx0-minX*sx, ry0-minY*sy
	for _, v := range []float64{sx, sy, tx, ty} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return "", fmt.Errorf("the placement matrix is not finite")
		}
	}
	// A scale that is (nearly) zero would squash the appearance out of sight.
	if math.Abs(sx) < minScale || math.Abs(sy) < minScale {
		return "", fmt.Errorf("the appearance cannot be placed: scale %g x %g is degenerate", sx, sy)
	}
	return fmt.Sprintf("%s 0 0 %s %s %s", num(sx), num(sy), num(tx), num(ty)), nil
}

// xobjectDict returns the XObject resource dictionary of the page, created
// (and the page resources made its own) when needed.
func xobjectDict(xt *model.XRefTable, pd types.Dict, inh *model.InheritedPageAttrs) (types.Dict, error) {
	var res types.Dict
	if o, ok := pd.Find("Resources"); ok {
		d, err := xt.DereferenceDict(o)
		if err != nil {
			return nil, err
		}
		res = d
	}
	if res == nil {
		if inh != nil && inh.Resources != nil {
			res = inh.Resources.Clone().(types.Dict)
		} else {
			res = types.NewDict()
		}
		pd.Update("Resources", res)
	}
	var xo types.Dict
	if o, ok := res.Find("XObject"); ok {
		d, err := xt.DereferenceDict(o)
		if err != nil {
			return nil, err
		}
		xo = d
	}
	if xo == nil {
		xo = types.NewDict()
		res.Update("XObject", xo)
	}
	return xo, nil
}

func newStream(xt *model.XRefTable, content []byte) (*types.IndirectRef, error) {
	sd, err := xt.NewStreamDictForBuf(content)
	if err != nil {
		return nil, err
	}
	if err := sd.Encode(); err != nil {
		return nil, err
	}
	return xt.IndRefForNewObject(*sd)
}

// isolateAndAppend wraps the existing content streams in q ... Q, so that the
// graphic state they leave (a transformation that is never restored, for
// example) cannot reach the appearances, then adds the appearances as a last
// content stream. This is the standard method.
func isolateAndAppend(xt *model.XRefTable, pd types.Dict, content []byte) error {
	var existing types.Array
	if o, ok := pd.Find("Contents"); ok {
		c, err := xt.Dereference(o)
		if err != nil {
			return err
		}
		if a, isArray := c.(types.Array); isArray {
			existing = append(existing, a...)
		} else if c != nil {
			existing = append(existing, o)
		}
	}
	arr := types.Array{}
	if len(existing) > 0 {
		open, err := newStream(xt, []byte("q\n"))
		if err != nil {
			return err
		}
		closing, err := newStream(xt, []byte("\nQ\n"))
		if err != nil {
			return err
		}
		arr = append(arr, *open)
		arr = append(arr, existing...)
		arr = append(arr, *closing)
	}
	app, err := newStream(xt, content)
	if err != nil {
		return err
	}
	arr = append(arr, *app)
	pd.Update("Contents", arr)
	return nil
}

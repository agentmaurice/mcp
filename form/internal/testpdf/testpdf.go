// Package testpdf generates the small AcroForm PDFs used by the tests, with
// pdfcpu itself (no binary fixture is checked in, so there is no provenance
// to doubt). The refusal fixtures (XFA, signed, encrypted) are derived from
// the generated form through the pdfcpu object model.
package testpdf

import (
	"bytes"
	"context"
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// formJSON describes two pages: page 1 holds the form, page 2 only text.
const formJSON = `{
  "paper": "A4P",
  "origin": "UpperLeft",
  "fonts": {"f": {"name": "Helvetica", "size": 11}},
  "pages": {
    "1": {
      "content": {
        "text": [{"value": "Supplier questionnaire", "pos": [50, 40], "font": {"name": "Helvetica-Bold", "size": 16}}],
        "textfield": [
          {"id": "company", "tip": "Company name", "pos": [50, 100], "width": 250, "font": {"name": "Helvetica", "size": 11}, "value": "Initial Corp"},
          {"id": "vat", "tip": "VAT number", "pos": [50, 140], "width": 160, "maxlen": 8, "font": {"name": "Helvetica", "size": 11}},
          {"id": "comments", "pos": [50, 200], "width": 250, "height": 60, "multiline": true, "font": {"name": "Helvetica", "size": 11}},
          {"id": "locked_ref", "pos": [50, 260], "width": 160, "locked": true, "value": "FIXED-1", "font": {"name": "Helvetica", "size": 11}},
          {"id": "untouched", "pos": [50, 310], "width": 160, "value": "keep me", "font": {"name": "Helvetica", "size": 11}}
        ],
        "checkbox": [
          {"id": "gdpr", "pos": [50, 345], "width": 14}
        ],
        "radiobuttongroup": [
          {"id": "size", "pos": [130, 380], "label": {"value": "Size", "width": 60, "font": {"name": "Helvetica", "size": 11}}, "buttons": {"values": ["Small", "Medium", "Large"], "label": {"value": "x", "pos": "right", "width": 60, "font": {"name": "Helvetica", "size": 11}}}, "value": "Small", "width": 14}
        ],
        "combobox": [
          {"id": "country", "pos": [50, 440], "width": 160, "options": ["France", "Germany", "Spain"], "font": {"name": "Helvetica", "size": 11}},
          {"id": "city", "pos": [250, 440], "width": 160, "edit": true, "options": ["Paris", "Berlin"], "font": {"name": "Helvetica", "size": 11}}
        ],
        "listbox": [
          {"id": "certs", "pos": [50, 510], "width": 160, "height": 60, "multi": true, "options": ["ISO 27001", "SOC 2", "HDS"], "font": {"name": "Helvetica", "size": 11}}
        ]
      }
    },
    "2": {
      "content": {
        "text": [{"value": "Page two body text, no field.", "pos": [50, 60], "font": {"name": "Helvetica", "size": 12}}],
        "textfield": [
          {"id": "second_page", "pos": [50, 100], "width": 160, "font": {"name": "Helvetica", "size": 11}}
        ]
      }
    }
  }
}`

// Form returns a two-page AcroForm: text fields (one with a maximum length,
// one multiline, one read-only), a checkbox, a radio group, a combo box and a
// multi-select list box.
func Form() ([]byte, error) {
	conf := model.NewStatelessConfiguration()
	conf.Offline = true
	var out bytes.Buffer
	if err := api.Create(context.Background(), nil, bytes.NewReader([]byte(formJSON)), &out, conf); err != nil {
		return nil, fmt.Errorf("create the test form: %w", err)
	}
	return out.Bytes(), nil
}

func mutate(data []byte, fn func(ctx *model.Context) error) ([]byte, error) {
	conf := model.NewStatelessConfiguration()
	conf.Cmd = model.FILLFORMFIELDS
	ctx, err := api.ReadValidateAndOptimize(context.Background(), bytes.NewReader(data), conf, nil)
	if err != nil {
		return nil, err
	}
	if err := fn(ctx); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := api.Write(context.Background(), ctx, &out, conf); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// WithRequired marks the named top-level fields as required (Ff bit 2).
func WithRequired(data []byte, names ...string) ([]byte, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		fields, err := xt.DereferenceArray(xt.Form["Fields"])
		if err != nil {
			return err
		}
		for _, f := range fields {
			d, err := xt.DereferenceDict(f)
			if err != nil {
				return err
			}
			if s, _, _ := xt.DereferenceStringEntry(d, "T"); s != nil && want[*s] {
				ff := 0
				if i := d.IntEntry("Ff"); i != nil {
					ff = *i
				}
				d["Ff"] = types.Integer(ff | 2)
			}
		}
		return nil
	})
}

// XFA returns the form with a well-formed /XFA entry (an array of name and
// stream pairs) in its AcroForm dictionary.
func XFA(data []byte) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		sd, err := xt.NewStreamDictForBuf([]byte("<xdp:xdp xmlns:xdp=\"http://ns.adobe.com/xdp/\"/>"))
		if err != nil {
			return err
		}
		if err := sd.Encode(); err != nil {
			return err
		}
		ref, err := xt.IndRefForNewObject(*sd)
		if err != nil {
			return err
		}
		xt.Form["XFA"] = types.Array{types.StringLiteral("template"), *ref}
		return nil
	})
}

// XFAMalformed returns the form with an /XFA entry that pdfcpu refuses to
// validate: the XFA check must not depend on the file validating.
func XFAMalformed(data []byte) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		ctx.XRefTable.Form["XFA"] = types.Array{types.StringLiteral("template"), types.StringLiteral("<xdp/>")}
		return nil
	})
}

// SignedFlags returns the form with the SignaturesExist flag of /SigFlags.
func SignedFlags(data []byte) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		ctx.XRefTable.Form["SigFlags"] = types.Integer(3)
		return nil
	})
}

// SignedField returns the form with an added signature field whose value is a
// signature dictionary (/ByteRange, /Contents). The cryptographic content is
// a placeholder: the MCP only needs to recognize the signature.
func SignedField(data []byte) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		sig, err := xt.IndRefForNewObject(types.Dict{
			"Type": types.Name("Sig"), "Filter": types.Name("Adobe.PPKLite"), "SubFilter": types.Name("adbe.pkcs7.detached"),
			"ByteRange": types.Array{types.Integer(0), types.Integer(10), types.Integer(20), types.Integer(30)},
			"Contents":  types.HexLiteral("00"),
		})
		if err != nil {
			return err
		}
		pageRef, err := xt.PageDictIndRef(context.Background(), 1)
		if err != nil {
			return err
		}
		field, err := xt.IndRefForNewObject(types.Dict{
			"Type": types.Name("Annot"), "Subtype": types.Name("Widget"), "FT": types.Name("Sig"),
			"T": types.StringLiteral("approval"), "V": *sig, "F": types.Integer(4), "P": *pageRef,
			"Rect": types.Array{types.Integer(300), types.Integer(100), types.Integer(400), types.Integer(130)},
		})
		if err != nil {
			return err
		}
		fields, err := xt.DereferenceArray(xt.Form["Fields"])
		if err != nil {
			return err
		}
		xt.Form["Fields"] = append(fields, *field)
		pd, _, _, err := xt.PageDict(context.Background(), 1, false)
		if err != nil {
			return err
		}
		annots, err := xt.DereferenceArray(pd["Annots"])
		if err != nil {
			return err
		}
		pd["Annots"] = append(annots, *field)
		return nil
	})
}

// Encrypted returns the form encrypted with a user password.
func Encrypted(data []byte, userPW string) ([]byte, error) {
	conf := model.NewStatelessConfiguration()
	conf.UserPW, conf.OwnerPW = userPW, userPW+"-owner"
	var out bytes.Buffer
	if err := api.Encrypt(context.Background(), bytes.NewReader(data), &out, conf); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// EncryptedEmptyUserPassword returns the form encrypted with an empty user
// password (opens without a prompt, restricted by an owner password).
func EncryptedEmptyUserPassword(data []byte) ([]byte, error) {
	conf := model.NewStatelessConfiguration()
	conf.UserPW, conf.OwnerPW = "", "owner-secret"
	var out bytes.Buffer
	if err := api.Encrypt(context.Background(), bytes.NewReader(data), &out, conf); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// NoForm returns a one-page PDF without any form field.
func NoForm() ([]byte, error) {
	conf := model.NewStatelessConfiguration()
	conf.Offline = true
	src := `{"paper": "A4P", "pages": {"1": {"content": {"text": [{"value": "No field here", "pos": [50, 60], "font": {"name": "Helvetica", "size": 12}}]}}}}`
	var out bytes.Buffer
	if err := api.Create(context.Background(), nil, bytes.NewReader([]byte(src)), &out, conf); err != nil {
		return nil, fmt.Errorf("create the test PDF: %w", err)
	}
	return out.Bytes(), nil
}

// WithMarkerText returns the form with the literal strings "/Encrypt" and
// "/ByteRange" in an uncompressed document information string: an ordinary,
// unencrypted and unsigned file that a substring search would misjudge.
func WithMarkerText(data []byte) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		d, err := xt.DereferenceDict(*xt.Info)
		if err != nil {
			return err
		}
		d["Subject"] = types.StringLiteral("mentions /Encrypt and /ByteRange as plain text")
		return nil
	})
}

func pageOne(ctx *model.Context) (types.Dict, error) {
	pd, _, _, err := ctx.XRefTable.PageDict(context.Background(), 1, false)
	return pd, err
}

// WithExistingXObject returns the form whose page 1 already draws its own
// XObject named /FmFlat1 (the name a flatten step would like to take).
func WithExistingXObject(data []byte) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		pd, err := pageOne(ctx)
		if err != nil {
			return err
		}
		sd, err := xt.NewStreamDictForBuf([]byte("0 0 5 5 re f"))
		if err != nil {
			return err
		}
		sd.Dict["Type"] = types.Name("XObject")
		sd.Dict["Subtype"] = types.Name("Form")
		sd.Dict["BBox"] = types.Array{types.Integer(0), types.Integer(0), types.Integer(5), types.Integer(5)}
		if err := sd.Encode(); err != nil {
			return err
		}
		ref, err := xt.IndRefForNewObject(*sd)
		if err != nil {
			return err
		}
		res, err := xt.DereferenceDict(pd["Resources"])
		if err != nil {
			return err
		}
		xo := types.Dict{"FmFlat1": *ref}
		res["XObject"] = xo
		return xt.AppendContent(pd, []byte("q 1 0 0 1 300 700 cm /FmFlat1 Do Q"))
	})
}

// WithUnrestoredScale returns the form whose page 1 content scales the
// coordinate system by 2 and never restores it.
func WithUnrestoredScale(data []byte) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		pd, err := pageOne(ctx)
		if err != nil {
			return err
		}
		return ctx.XRefTable.AppendContent(pd, []byte("2 0 0 2 0 0 cm"))
	})
}

// WithoutAppearance removes the /AP of the named top-level field.
func WithoutAppearance(data []byte, name string) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		fields, err := xt.DereferenceArray(xt.Form["Fields"])
		if err != nil {
			return err
		}
		for _, f := range fields {
			d, err := xt.DereferenceDict(f)
			if err != nil {
				return err
			}
			if s, _, _ := xt.DereferenceStringEntry(d, "T"); s != nil && *s == name {
				delete(d, "AP")
			}
		}
		return nil
	})
}

// WithImage returns the form whose page 1 draws an image XObject /Logo whose
// single pixel is px (two files with different px have the same /Contents and
// differ only in the referenced image).
func WithImage(data []byte, px byte) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		pd, err := pageOne(ctx)
		if err != nil {
			return err
		}
		sd, err := xt.NewStreamDictForBuf([]byte{px})
		if err != nil {
			return err
		}
		sd.Dict["Type"] = types.Name("XObject")
		sd.Dict["Subtype"] = types.Name("Image")
		sd.Dict["Width"] = types.Integer(1)
		sd.Dict["Height"] = types.Integer(1)
		sd.Dict["ColorSpace"] = types.Name("DeviceGray")
		sd.Dict["BitsPerComponent"] = types.Integer(8)
		if err := sd.Encode(); err != nil {
			return err
		}
		ref, err := xt.IndRefForNewObject(*sd)
		if err != nil {
			return err
		}
		res, err := xt.DereferenceDict(pd["Resources"])
		if err != nil {
			return err
		}
		xo := types.Dict{}
		if o, ok := res["XObject"]; ok {
			if xo, err = xt.DereferenceDict(o); err != nil {
				return err
			}
		}
		xo["Logo"] = *ref
		res["XObject"] = xo
		return xt.AppendContent(pd, []byte("q 10 0 0 10 400 700 cm /Logo Do Q"))
	})
}

// WithReplacedAppearance replaces the normal appearance stream of the named
// top-level field by one that draws other text, keeping its value.
func WithReplacedAppearance(data []byte, name, text string) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		fields, err := xt.DereferenceArray(xt.Form["Fields"])
		if err != nil {
			return err
		}
		for _, f := range fields {
			d, err := xt.DereferenceDict(f)
			if err != nil {
				return err
			}
			if s, _, _ := xt.DereferenceStringEntry(d, "T"); s == nil || *s != name {
				continue
			}
			ap, err := xt.DereferenceDict(d["AP"])
			if err != nil {
				return err
			}
			old, _, err := xt.DereferenceStreamDict(ap["N"])
			if err != nil || old == nil {
				return fmt.Errorf("appearance of %s: %v", name, err)
			}
			sd, err := xt.NewStreamDictForBuf([]byte("0.5 g 0 0 40 12 re f 0 g " + text))
			if err != nil {
				return err
			}
			for _, k := range []string{"Type", "Subtype", "BBox", "Matrix", "Resources"} {
				if v, ok := old.Dict[k]; ok {
					sd.Dict[k] = v
				}
			}
			if err := sd.Encode(); err != nil {
				return err
			}
			ref, err := xt.IndRefForNewObject(*sd)
			if err != nil {
				return err
			}
			ap["N"] = *ref
		}
		return nil
	})
}

// WithOptionalContent marks the widget of the named top-level field with an /OC
// entry that points to an optional content group switched off in /OCProperties.
func WithOptionalContent(data []byte, name string) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		ocg, err := xt.IndRefForNewObject(types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("Hidden layer")})
		if err != nil {
			return err
		}
		xt.RootDict["OCProperties"] = types.Dict{
			"OCGs": types.Array{*ocg},
			"D":    types.Dict{"OFF": types.Array{*ocg}},
		}
		fields, err := xt.DereferenceArray(xt.Form["Fields"])
		if err != nil {
			return err
		}
		for _, f := range fields {
			d, err := xt.DereferenceDict(f)
			if err != nil {
				return err
			}
			if s, _, _ := xt.DereferenceStringEntry(d, "T"); s != nil && *s == name {
				d["OC"] = *ocg
			}
		}
		return nil
	})
}

func withField(data []byte, name string, fn func(xt *model.XRefTable, d types.Dict) error) ([]byte, error) {
	return mutate(data, func(ctx *model.Context) error {
		xt := ctx.XRefTable
		fields, err := xt.DereferenceArray(xt.Form["Fields"])
		if err != nil {
			return err
		}
		for _, f := range fields {
			d, err := xt.DereferenceDict(f)
			if err != nil {
				return err
			}
			if s, _, _ := xt.DereferenceStringEntry(d, "T"); s != nil && *s == name {
				return fn(xt, d)
			}
		}
		return fmt.Errorf("no field %q", name)
	})
}

// WithValueNoAppearance sets /V of the named top-level field to v and removes
// its /AP.
func WithValueNoAppearance(data []byte, name string, v types.Object) ([]byte, error) {
	return withField(data, name, func(_ *model.XRefTable, d types.Dict) error {
		d["V"] = v
		delete(d, "AP")
		return nil
	})
}

// WithHugeAppearanceBox gives the normal appearance of the named field a
// /BBox of 1e9 x 1e9, so that fitting it to the widget needs a scale below 1e-6.
func WithHugeAppearanceBox(data []byte, name string) ([]byte, error) {
	return withField(data, name, func(xt *model.XRefTable, d types.Dict) error {
		ap, err := xt.DereferenceDict(d["AP"])
		if err != nil {
			return err
		}
		sd, _, err := xt.DereferenceStreamDict(ap["N"])
		if err != nil || sd == nil {
			return fmt.Errorf("appearance: %v", err)
		}
		sd.Dict["BBox"] = types.Array{types.Integer(0), types.Integer(0), types.Integer(1000000000), types.Integer(1000000000)}
		delete(sd.Dict, "Matrix")
		return nil
	})
}

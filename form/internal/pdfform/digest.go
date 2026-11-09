package pdfform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

const (
	maxDigestDepth = 24
	maxDigestNodes = 400000
)

// hasher computes a deep, decoded digest of PDF objects: dictionaries and
// arrays recursively, streams on their decoded content, indirect references
// followed once (memoized, cycle-safe). It is bounded in depth and in size: an
// object that cannot be digested is an error, never "unchanged".
type hasher struct {
	xt       *model.XRefTable
	memo     map[int]string
	visiting map[int]bool
	nodes    int
}

func newHasher(xt *model.XRefTable) *hasher {
	return &hasher{xt: xt, memo: map[int]string{}, visiting: map[int]bool{}}
}

func sum(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (h *hasher) hash(o types.Object, depth int) (string, error) {
	if depth > maxDigestDepth {
		return "", errors.New("resource nesting too deep to verify")
	}
	if h.nodes++; h.nodes > maxDigestNodes {
		return "", errors.New("too many objects to verify")
	}
	switch v := o.(type) {
	case nil:
		return "null", nil
	case types.IndirectRef:
		n := v.ObjectNumber.Value()
		if s, ok := h.memo[n]; ok {
			return s, nil
		}
		if h.visiting[n] {
			return "cycle", nil
		}
		h.visiting[n] = true
		defer delete(h.visiting, n)
		target, err := h.xt.Dereference(v)
		if err != nil {
			return "", fmt.Errorf("object %d: %w", n, err)
		}
		s, err := h.hash(target, depth+1)
		if err != nil {
			return "", err
		}
		h.memo[n] = s
		return s, nil
	case types.Dict:
		return h.hashDict(v, nil, depth)
	case types.StreamDict:
		return h.hashStream(v, depth)
	case types.Array:
		parts := []string{"array"}
		for _, e := range v {
			s, err := h.hash(e, depth+1)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return sum(parts...), nil
	default:
		return sum("atom", fmt.Sprintf("%T", o), o.PDFString()), nil
	}
}

func (h *hasher) hashDict(d types.Dict, skip map[string]bool, depth int) (string, error) {
	keys := make([]string, 0, len(d))
	for k := range d {
		if !skip[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := []string{"dict"}
	for _, k := range keys {
		s, err := h.hash(d[k], depth+1)
		if err != nil {
			return "", err
		}
		parts = append(parts, k, s)
	}
	return sum(parts...), nil
}

func (h *hasher) hashStream(sd types.StreamDict, depth int) (string, error) {
	cp := sd
	if err := h.xt.DecodeStream(&cp); err != nil {
		return "", fmt.Errorf("stream cannot be decoded: %w", err)
	}
	// The encoding (Length, Filter, DecodeParms) may change when a file is
	// rewritten; the decoded content may not.
	d, err := h.hashDict(sd.Dict, map[string]bool{"Length": true, "Filter": true, "DecodeParms": true}, depth)
	if err != nil {
		return "", err
	}
	c := sha256.Sum256(cp.Content)
	return sum("stream", d, hex.EncodeToString(c[:])), nil
}

// pageDigest is what a page displays: its decoded content and, entry by entry,
// every resource it can use (fonts, XObjects including images and Form
// XObjects, graphic states, color spaces, patterns, shadings), in depth.
type pageDigest struct {
	content   []byte
	resources map[string]string
	err       string // not empty: the page could not be digested, which never counts as unchanged
}

func pageDigests(ctx context.Context, xt *model.XRefTable) []pageDigest {
	out := make([]pageDigest, 0, xt.PageCount)
	h := newHasher(xt)
	for i := 1; i <= xt.PageCount; i++ {
		out = append(out, digestPage(ctx, xt, h, i))
	}
	return out
}

func digestPage(ctx context.Context, xt *model.XRefTable, h *hasher, pageNr int) (pd pageDigest) {
	fail := func(format string, args ...any) pageDigest {
		return pageDigest{err: fmt.Sprintf("page %d: "+format, append([]any{pageNr}, args...)...)}
	}
	d, _, inh, err := xt.PageDict(ctx, pageNr, false)
	if err != nil || d == nil {
		return fail("the page cannot be read")
	}
	c, err := xt.PageContent(d, pageNr)
	if err != nil && !errors.Is(err, model.ErrNoContent) {
		return fail("the page content cannot be decoded: %v", firstLine(err))
	}
	res := inh.Resources
	if res == nil {
		if o, ok := d.Find("Resources"); ok {
			if res, err = xt.DereferenceDict(o); err != nil {
				return fail("the page resources cannot be read")
			}
		}
	}
	pd = pageDigest{content: c, resources: map[string]string{}}
	for cat, o := range res {
		obj, err := xt.Dereference(o)
		if err != nil {
			return fail("resource %s cannot be read", cat)
		}
		dict, isDict := obj.(types.Dict)
		if !isDict {
			s, err := h.hash(obj, 0)
			if err != nil {
				return fail("resource %s: %v", cat, firstLine(err))
			}
			pd.resources[cat] = s
			continue
		}
		for name, entry := range dict {
			s, err := h.hash(entry, 0)
			if err != nil {
				return fail("resource %s/%s: %v", cat, name, firstLine(err))
			}
			pd.resources[cat+"/"+name] = s
		}
	}
	return pd
}

// samePages is the strict check of a fill without flatten.
func samePages(before, after []pageDigest) error {
	if len(before) != len(after) {
		return errf(CodeIntegrityFailed, "", "page count changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		if err := usable(before[i], after[i]); err != nil {
			return err
		}
		if !bytes.Equal(before[i].content, after[i].content) {
			return errf(CodeIntegrityFailed, "", "the content of page %d changed", i+1)
		}
		if len(before[i].resources) != len(after[i].resources) {
			return errf(CodeIntegrityFailed, "", "the resources of page %d changed", i+1)
		}
		for k, v := range before[i].resources {
			if after[i].resources[k] != v {
				return errf(CodeIntegrityFailed, "", "resource %s of page %d changed", k, i+1)
			}
		}
	}
	return nil
}

// flattenedPages is the check of a fill with flatten: the original content must
// survive wrapped in q ... Q (or be unchanged), and every original resource
// must still be present and identical.
func flattenedPages(before, after []pageDigest) error {
	if len(before) != len(after) {
		return errf(CodeIntegrityFailed, "", "page count changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		if err := usable(before[i], after[i]); err != nil {
			return err
		}
		for k, v := range before[i].resources {
			if after[i].resources[k] != v {
				return errf(CodeIntegrityFailed, "", "original resource %s of page %d was altered or removed by the flatten", k, i+1)
			}
		}
		if bytes.Equal(before[i].content, after[i].content) {
			continue
		}
		a := bytes.TrimSpace(after[i].content)
		if !bytes.HasPrefix(a, []byte("q")) || !bytes.Contains(a, before[i].content) {
			return errf(CodeIntegrityFailed, "", "the original content of page %d is not preserved by the flatten", i+1)
		}
	}
	return nil
}

func usable(b, a pageDigest) error {
	if b.err != "" || a.err != "" {
		return errf(CodeIntegrityFailed, "", "the page content cannot be verified (%s %s)", strings.TrimSpace(b.err), strings.TrimSpace(a.err))
	}
	return nil
}

// appearanceDigests digests, per field, the appearance streams of its widgets:
// all of AP /N, /D and /R (apSHA), and the one a flatten would draw (apDraw).
func appearanceDigests(xt *model.XRefTable, fields []walked) (all, draw map[string]string, err error) {
	h := newHasher(xt)
	all, draw = map[string]string{}, map[string]string{}
	for _, f := range fields {
		if f.name == "" {
			continue
		}
		var a, dr []string
		for _, w := range f.widgets {
			ra, rd, err := widgetDigests(xt, h, w)
			if err != nil {
				return nil, nil, fmt.Errorf("field %q: %w", f.name, err)
			}
			a, dr = append(a, ra), append(dr, rd)
		}
		sort.Strings(a)
		sort.Strings(dr)
		all[f.name], draw[f.name] = sum(a...), sum(dr...)
	}
	return all, draw, nil
}

// widgetDigests returns the digest of the whole /AP of a widget and the digest
// of the appearance stream selected for drawing ("-" when nothing is drawn).
func widgetDigests(xt *model.XRefTable, h *hasher, w types.Dict) (all, drawn string, err error) {
	all = "no-ap"
	if ap, ok := w.Find("AP"); ok {
		if all, err = h.hash(ap, 0); err != nil {
			return "", "", err
		}
	}
	drawn = "-"
	if flags := intEntry(xt, w, "F"); flags&(annotHidden|annotNoView) != 0 {
		return all, "hidden", nil
	}
	ref, _, err := appearanceRef(xt, w)
	if err != nil {
		return "", "", err
	}
	if ref != nil {
		if drawn, err = h.hash(*ref, 0); err != nil {
			return "", "", err
		}
	}
	return all, drawn, nil
}

func intEntry(xt *model.XRefTable, d types.Dict, key string) int {
	if i, _, err := xt.DereferenceIntegerEntry(d, key); err == nil && i != nil {
		return i.Value()
	}
	return 0
}

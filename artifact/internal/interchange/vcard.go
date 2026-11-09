package interchange

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// VCFInput is the typed data of the vcf output.
type VCFInput struct {
	Cards []Card `json:"cards"`
}

// Card is one vCard 4.0 (the keys are the lower-cased property names).
type Card struct {
	UID   string     `json:"uid,omitempty"`
	FN    string     `json:"fn"`
	N     *NameParts `json:"n,omitempty"`
	Org   []string   `json:"org,omitempty"` // organization, then units
	Title string     `json:"title,omitempty"`
	Email []Typed    `json:"email,omitempty"`
	Tel   []Typed    `json:"tel,omitempty"`
	Adr   []Address  `json:"adr,omitempty"`
	URL   []string   `json:"url,omitempty"`
	Note  string     `json:"note,omitempty"`
}

// NameParts is the structured name (N).
type NameParts struct {
	Family     string `json:"family,omitempty"`
	Given      string `json:"given,omitempty"`
	Additional string `json:"additional,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	Suffix     string `json:"suffix,omitempty"`
}

// Typed is an e-mail address or a phone number with an optional TYPE.
type Typed struct {
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

// Address is one ADR.
type Address struct {
	Type       string `json:"type,omitempty"`
	POBox      string `json:"po_box,omitempty"`
	Extended   string `json:"extended,omitempty"`
	Street     string `json:"street,omitempty"`
	Locality   string `json:"locality,omitempty"`
	Region     string `json:"region,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
}

var (
	phoneChars  = regexp.MustCompile(`[ ().\-]`)
	phoneNumber = regexp.MustCompile(`^\+?[0-9]{3,20}$`)
)

var uriScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:[^\s\\"<>]+$`)

// isURI reports whether s is an absolute URI (scheme, then characters that need
// no escaping in a content line value).
func isURI(s string) bool { return uriScheme.MatchString(s) }

func typeParam(label, t string, allowed ...string) ([]string, error) {
	if t == "" {
		return nil, nil
	}
	for _, a := range allowed {
		if a == t {
			return []string{"TYPE=" + t}, nil
		}
	}
	return nil, dataErr("%s.type %q is not one of %s", label, t, strings.Join(allowed, ", "))
}

// RenderVCF renders data (already decoded JSON) to a vCard 4.0 file.
func RenderVCF(data any) ([]byte, error) {
	var in VCFInput
	if err := validateAndDecode(OutputVCF, data, &in); err != nil {
		return nil, err
	}
	if len(in.Cards) == 0 {
		return nil, dataErr("cards must hold at least one card")
	}
	if len(in.Cards) > MaxItems {
		return nil, limitErr("%d cards, above the limit of %d", len(in.Cards), MaxItems)
	}
	var d document
	for i := range in.Cards {
		if err := renderCard(&d, &in.Cards[i], fmt.Sprintf("cards[%d]", i)); err != nil {
			return nil, err
		}
	}
	return d.bytes()
}

func renderCard(d *document, c *Card, label string) error {
	if c.FN == "" {
		return dataErr("%s.fn is required", label)
	}
	for _, n := range []int{len(c.Email), len(c.Tel), len(c.Adr), len(c.URL), len(c.Org)} {
		if n > MaxListItems {
			return limitErr("%s holds a list of %d items, above the limit of %d", label, n, MaxListItems)
		}
	}
	single := func(name, v string) error { return checkLine(label+"."+name, v) }
	if err := single("fn", c.FN); err != nil {
		return err
	}
	if err := single("title", c.Title); err != nil {
		return err
	}
	if err := checkText(label+".note", c.Note, true, MaxTextBytes); err != nil {
		return err
	}
	uid := c.UID
	if uid == "" {
		cc := *c
		cc.UID = ""
		canonical, _ := json.Marshal(cc)
		sum := sha256.Sum256(append([]byte("agentmaurice.artifact.vcf.uid\x00"), canonical...))
		sum[6] = sum[6]&0x0f | 0x50 // UUID layout (version 5 bits), derived from the data
		sum[8] = sum[8]&0x3f | 0x80
		uid = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
	} else if err := single("uid", uid); err != nil {
		return err
	}
	d.line("BEGIN:VCARD")
	d.line("VERSION:4.0")
	if c.UID == "" || isURI(uid) {
		// UID is of type URI in vCard 4.0: written as is, no text escaping.
		d.prop("UID", nil, uid)
	} else {
		// Not a URI: declared as text and escaped.
		d.prop("UID", []string{"VALUE=text"}, EscapeText(uid))
	}
	d.prop("FN", nil, EscapeText(c.FN))
	if c.N != nil {
		parts := []string{c.N.Family, c.N.Given, c.N.Additional, c.N.Prefix, c.N.Suffix}
		esc := make([]string, len(parts))
		for i, p := range parts {
			if err := single("n", p); err != nil {
				return err
			}
			esc[i] = EscapeText(p)
		}
		d.prop("N", nil, strings.Join(esc, ";"))
	}
	if len(c.Org) > 0 {
		esc := make([]string, len(c.Org))
		for i, p := range c.Org {
			if err := single("org", p); err != nil {
				return err
			}
			esc[i] = EscapeText(p)
		}
		d.prop("ORG", nil, strings.Join(esc, ";"))
	}
	if c.Title != "" {
		d.prop("TITLE", nil, EscapeText(c.Title))
	}
	for i, e := range c.Email {
		l := fmt.Sprintf("%s.email[%d]", label, i)
		if err := checkEmail(l+".value", e.Value); err != nil {
			return err
		}
		params, err := typeParam(l, e.Type, "work", "home")
		if err != nil {
			return err
		}
		d.prop("EMAIL", params, EscapeText(e.Value))
	}
	for i, t := range c.Tel {
		l := fmt.Sprintf("%s.tel[%d]", label, i)
		if err := checkLine(l+".value", t.Value); err != nil {
			return err
		}
		number := phoneChars.ReplaceAllString(t.Value, "")
		if !phoneNumber.MatchString(number) {
			return dataErr("%s.value %q is not a phone number (digits, optional leading +, spaces, dots, dashes and parentheses)", l, t.Value)
		}
		params, err := typeParam(l, t.Type, "work", "home", "cell", "voice", "fax", "text")
		if err != nil {
			return err
		}
		if strings.HasPrefix(number, "+") {
			// A global number is a valid tel: URI (RFC 3966).
			d.prop("TEL", append(params, "VALUE=uri"), "tel:"+number)
		} else {
			// A local number has no valid tel: URI without a phone-context: kept as text, form preserved.
			d.prop("TEL", append(params, "VALUE=text"), EscapeText(t.Value))
		}
	}
	for i, a := range c.Adr {
		l := fmt.Sprintf("%s.adr[%d]", label, i)
		params, err := typeParam(l, a.Type, "work", "home")
		if err != nil {
			return err
		}
		comps := []string{a.POBox, a.Extended, a.Street, a.Locality, a.Region, a.PostalCode, a.Country}
		esc := make([]string, len(comps))
		for j, p := range comps {
			if err := checkLine(l, p); err != nil {
				return err
			}
			esc[j] = EscapeText(p)
		}
		d.prop("ADR", params, strings.Join(esc, ";"))
	}
	for i, u := range c.URL {
		if err := checkURI(fmt.Sprintf("%s.url[%d]", label, i), u); err != nil {
			return err
		}
		d.prop("URL", nil, u)
	}
	if c.Note != "" {
		d.prop("NOTE", nil, EscapeText(c.Note))
	}
	d.line("END:VCARD")
	return nil
}

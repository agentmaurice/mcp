package mcpserver

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"path"
	"strings"
)

const (
	nsRelationships = "http://schemas.openxmlformats.org/package/2006/relationships"
	// maxRelsTotalBytes bounds what is read from all relationship parts and
	// [Content_Types].xml together, on top of the per-part limit.
	maxRelsTotalBytes = 32 << 20
)

// readBounded decodes a part with a per-part limit, charging it to a budget.
func readBounded(f *zip.File, budget *int64, visit func(xml.StartElement)) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	n := int64(maxXMLPartBytes + 1)
	if *budget < n {
		n = *budget
	}
	lr := &io.LimitedReader{R: rc, N: n}
	dec := xml.NewDecoder(lr)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if lr.N <= 0 {
			return errors.New("part too large")
		}
		if se, ok := tok.(xml.StartElement); ok {
			visit(se)
		}
	}
	*budget -= n - lr.N
	return nil
}

// checkRelationships verifies that every internal relationship target of
// _rels/.rels and */_rels/*.rels exists in the package and does not climb out
// of the archive. External targets (TargetMode="External") are not resolved.
func checkRelationships(files map[string]*zip.File, budget *int64) error {
	for name, f := range files {
		dir, base := path.Split(name)
		if !strings.HasSuffix(base, ".rels") || (dir != "_rels/" && !strings.HasSuffix(dir, "/_rels/")) {
			continue
		}
		// the source part directory: "word/_rels/document.xml.rels" -> "word"
		srcDir := strings.TrimSuffix(strings.TrimSuffix(dir, "/"), "_rels")
		srcDir = strings.TrimSuffix(srcDir, "/")
		// the source part must exist: _rels/.rels is the package itself,
		// "x/_rels/y.rels" belongs to "x/y"
		if name != "_rels/.rels" {
			if _, ok := files[path.Join(srcDir, strings.TrimSuffix(base, ".rels"))]; !ok {
				return errors.New("a relationships part has no source part")
			}
		}
		var bad error
		err := readBounded(f, budget, func(se xml.StartElement) {
			if bad != nil || se.Name.Local != "Relationship" || se.Name.Space != nsRelationships {
				return
			}
			var target, id string
			external := false
			for _, a := range se.Attr {
				if a.Name.Space != "" {
					continue // a namespace-qualified attribute is not the OPC one
				}
				switch a.Name.Local {
				case "Id":
					id = a.Value
				case "Target":
					target = a.Value
				case "TargetMode":
					switch a.Value {
					case "External":
						external = true
					case "Internal":
					default:
						bad = errors.New("invalid TargetMode")
						return
					}
				}
			}
			if strings.TrimSpace(id) == "" {
				bad = errors.New("relationship without Id")
				return
			}
			if external {
				return
			}
			if target == "" {
				bad = errors.New("empty relationship target")
				return
			}
			bad = resolveTarget(files, srcDir, target)
		})
		if err != nil {
			return err
		}
		if bad != nil {
			return bad
		}
	}
	return nil
}

// resolveTarget resolves an internal target against the source part directory
// and requires an existing part; a path that leaves the archive is refused.
func resolveTarget(files map[string]*zip.File, srcDir, target string) error {
	// the target must be a relative reference (RFC 3986, ECMA-376 OPC part 2):
	// no scheme, authority, query or backslash
	if strings.ContainsRune(target, '\\') || strings.HasPrefix(target, "//") {
		return errors.New("relationship target is not a relative reference")
	}
	u, err := url.Parse(target)
	if err != nil {
		return errors.New("relationship target is not a valid URI")
	}
	if u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery {
		return errors.New("relationship target is not a relative reference")
	}
	t := u.Path // decoded
	if t == "" {
		return nil // same-part fragment
	}
	if strings.ContainsRune(t, '\\') || strings.Contains(strings.SplitN(strings.TrimPrefix(t, "/"), "/", 2)[0], ":") {
		return errors.New("relationship target is not a relative reference")
	}
	full := t
	if strings.HasPrefix(t, "/") {
		full = t[1:]
	} else if srcDir != "" {
		full = srcDir + "/" + t
	}
	var parts []string
	for _, seg := range strings.Split(full, "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(parts) == 0 {
				return errors.New("relationship target leaves the package")
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, seg)
		}
	}
	if _, ok := files[strings.Join(parts, "/")]; ok {
		return nil
	}
	return errors.New("relationship target does not exist")
}

// checkContentTypes verifies that every part (but [Content_Types].xml itself
// and directory entries) has a content type, by Override or by Default extension.
func checkContentTypes(files map[string]*zip.File, budget *int64) error {
	defaults, overrides := map[string]bool{}, map[string]bool{}
	err := readBounded(files["[Content_Types].xml"], budget, func(se xml.StartElement) {
		if se.Name.Space != nsContentTypes {
			return
		}
		var ext, partName, ctype string
		for _, a := range se.Attr {
			if a.Name.Space != "" {
				continue
			}
			switch a.Name.Local {
			case "Extension":
				ext = strings.TrimSpace(a.Value)
			case "PartName":
				partName = strings.TrimSpace(a.Value)
			case "ContentType":
				ctype = strings.TrimSpace(a.Value)
			}
		}
		if ctype == "" {
			return // a declaration without a content type types nothing
		}
		// Default needs an Extension, Override a PartName: never one for the other
		switch se.Name.Local {
		case "Default":
			if ext != "" {
				defaults[strings.ToLower(ext)] = true
			}
		case "Override":
			if strings.TrimPrefix(partName, "/") != "" {
				overrides[strings.ToLower(strings.TrimPrefix(partName, "/"))] = true
			}
		}
	})
	if err != nil {
		return err
	}
	for name := range files {
		if name == "[Content_Types].xml" || strings.HasSuffix(name, "/") {
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
		if !overrides[strings.ToLower(name)] && (ext == "" || !defaults[ext]) {
			return errors.New("a part has no content type")
		}
	}
	return nil
}

package template

import (
	"strings"
)

// MaxSchemaCost bounds the evaluation cost of an input_schema. The JSON Schema
// validator cannot be interrupted, so the schema is bounded before it is compiled.
const MaxSchemaCost = 10000

// MaxValidationWork bounds schemaCost x number of data nodes: the validator
// cannot be interrupted, and its work grows with both.
const MaxValidationWork = 20_000_000

// literalKeywords hold literal values (data or annotations), not subschemas: the
// generic walks do not look into them, so {"const": {"$ref": "..."}} is just data.
var literalKeywords = map[string]bool{"const": true, "enum": true, "default": true, "examples": true, "title": true, "description": true, "$comment": true}

// nameMapKeywords hold objects whose keys are names (properties, definitions),
// not keywords: a property called "const" is a property, not a literal.
var nameMapKeywords = map[string]bool{"properties": true, "patternProperties": true, "dependentSchemas": true, "$defs": true, "definitions": true}

// schemaDialect is the only explicitly handled dialect.
const schemaDialect = "https://json-schema.org/draft/2020-12/schema"

// checkDialect accepts an absent $schema or draft 2020-12; any other dialect
// (draft-07 included) is refused: keywords such as `definitions` or an array
// `items` have other meanings there.
func checkDialect(root map[string]any) error {
	v, present := root["$schema"]
	if !present {
		return nil
	}
	s, _ := v.(string)
	if strings.TrimSuffix(s, "#") != schemaDialect {
		return errf(CodeInputSchemaInvalid, "$schema %v is not supported: use draft 2020-12 (%s) or omit $schema", v, schemaDialect)
	}
	return nil
}

// schemaCost returns the evaluation cost of a schema, independently of any
// keyword: 1 plus the cost of every child that is an object or an array, at any
// depth. A "$ref" or "$dynamicRef" adds the (memoized) cost of its target,
// resolved as a JSON pointer, each time it is met, so shared definitions
// referenced twice count twice and chains explode (2^n); the count saturates at
// MaxSchemaCost+1. A non-local reference and a cyclic reference are errors
// (input_schema_invalid).
func schemaCost(root map[string]any) (int, error) {
	memo := map[string]int{}
	inProgress := map[string]bool{"#": true}
	var cost func(node any, names bool) (int, error)
	cost = func(node any, names bool) (int, error) {
		total := 1
		add := func(n int) {
			total += n
			if total > MaxSchemaCost {
				total = MaxSchemaCost + 1
			}
		}
		child := func(v any, names bool) error {
			switch v.(type) {
			case map[string]any, []any:
				c, err := cost(v, names)
				if err != nil {
					return err
				}
				add(c)
			}
			return nil
		}
		switch n := node.(type) {
		case map[string]any:
			for _, k := range sortedAny(n) {
				v := n[k]
				if !names && literalKeywords[k] {
					continue // literal values are data, not subschemas
				}
				if !names && (k == "$ref" || k == "$dynamicRef") {
					if ref, ok := v.(string); ok {
						c, err := refCost(root, ref, memo, inProgress, cost)
						if err != nil {
							return 0, err
						}
						add(c)
						continue
					}
				}
				if err := child(v, !names && nameMapKeywords[k]); err != nil {
					return 0, err
				}
				if total > MaxSchemaCost {
					return total, nil
				}
			}
		case []any:
			for _, v := range n {
				if err := child(v, false); err != nil {
					return 0, err
				}
				if total > MaxSchemaCost {
					return total, nil
				}
			}
		}
		return total, nil
	}
	return cost(root, false)
}

func refCost(root map[string]any, ref string, memo map[string]int, inProgress map[string]bool, cost func(any, bool) (int, error)) (int, error) {
	if !strings.HasPrefix(ref, "#") {
		return 0, errf(CodeInputSchemaInvalid, "reference %q is not local: only #/... pointers are allowed in input_schema", ref)
	}
	if ref == "#" || inProgress[ref] {
		return 0, errf(CodeInputSchemaInvalid, "cyclic reference %q is not allowed in input_schema", ref)
	}
	if c, ok := memo[ref]; ok {
		return c, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return 0, errf(CodeInputSchemaInvalid, "reference %q is not a JSON pointer", ref)
	}
	target, ok := resolvePointer(root, ref)
	if !ok {
		return 1, nil // an unresolvable pointer is reported by the compilation
	}
	inProgress[ref] = true
	c, err := cost(target, false)
	delete(inProgress, ref)
	if err != nil {
		return 0, err
	}
	memo[ref] = c
	return c, nil
}

// resolvePointer follows a generic JSON pointer (#/a/0/b) in a decoded document.
func resolvePointer(root any, ref string) (any, bool) {
	cur := root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch c := cur.(type) {
		case map[string]any:
			v, ok := c[part]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i := 0
			for _, r := range part {
				if r < '0' || r > '9' {
					return nil, false
				}
				i = i*10 + int(r-'0')
				if i > len(c) {
					return nil, false
				}
			}
			if i >= len(c) {
				return nil, false
			}
			cur = c[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// checkRefs is the dedicated structure pass, without any cap: every "$ref" and
// "$dynamicRef" must be a local, resolvable JSON pointer and references must not
// be cyclic. Keys are visited in sorted order and each reference target is
// walked once, so the result (code and message) is deterministic and the pass is
// linear in the size of the schema. All of these are input_schema_invalid, and
// are reported before any limits_exceeded.
func checkRefs(root map[string]any) error {
	state := map[string]int{} // 1 in progress, 2 done
	var walk func(node any, names bool) error
	var visit func(ref string) error
	visit = func(ref string) error {
		if !strings.HasPrefix(ref, "#") {
			return errf(CodeInputSchemaInvalid, "reference %q is not local: only #/... pointers are allowed in input_schema", ref)
		}
		if ref == "#" {
			return errf(CodeInputSchemaInvalid, "cyclic reference %q is not allowed in input_schema", ref)
		}
		if !strings.HasPrefix(ref, "#/") {
			return errf(CodeInputSchemaInvalid, "reference %q is not a JSON pointer", ref)
		}
		switch state[ref] {
		case 1:
			return errf(CodeInputSchemaInvalid, "cyclic reference %q is not allowed in input_schema", ref)
		case 2:
			return nil
		}
		target, ok := resolvePointer(root, ref)
		if !ok {
			return errf(CodeInputSchemaInvalid, "reference %q does not resolve", ref)
		}
		state[ref] = 1
		if err := walk(target, false); err != nil {
			return err
		}
		state[ref] = 2
		return nil
	}
	walk = func(node any, names bool) error {
		switch n := node.(type) {
		case map[string]any:
			for _, k := range sortedAny(n) {
				if !names && literalKeywords[k] {
					continue
				}
				if !names && (k == "$ref" || k == "$dynamicRef") {
					if ref, ok := n[k].(string); ok {
						if err := visit(ref); err != nil {
							return err
						}
						continue
					}
				}
				if err := walk(n[k], !names && nameMapKeywords[k]); err != nil {
					return err
				}
			}
		case []any:
			for _, v := range n {
				if err := walk(v, false); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(root, false)
}

// dataWork measures the data for the validation budget: one per node plus the
// byte length of every string and object key, in one bounded walk (it stops as
// soon as the total passes limit and then returns limit+1).
func dataWork(v any, limit int) int {
	total := 0
	var walk func(v any)
	walk = func(v any) {
		if total > limit {
			return
		}
		total++
		switch x := v.(type) {
		case string:
			total += len(x)
		case map[string]any:
			for k, c := range x {
				total += len(k)
				if total > limit {
					return
				}
				walk(c)
			}
		case []any:
			for _, c := range x {
				if total > limit {
					return
				}
				walk(c)
			}
		}
	}
	walk(v)
	if total > limit {
		return limit + 1
	}
	return total
}

// MaxDataDepth bounds the nesting of the data and of the context. Deeper values
// are refused with limits_exceeded at validation, so every walk over them
// (validation, valueSize, countStats, json) has a bounded recursion depth.
const MaxDataDepth = 256

// dataDepth returns the nesting depth of a JSON-like value, stopping as soon as
// it passes limit (then limit+1): the recursion is at most limit+1 deep.
func dataDepth(v any, limit int) int {
	var walk func(v any, d int) int
	walk = func(v any, d int) int {
		if d > limit {
			return d
		}
		best := d
		switch x := v.(type) {
		case map[string]any:
			for _, c := range x {
				if m := walk(c, d+1); m > best {
					best = m
				}
				if best > limit {
					return best
				}
			}
		case []any:
			for _, c := range x {
				if m := walk(c, d+1); m > best {
					best = m
				}
				if best > limit {
					return best
				}
			}
		}
		return best
	}
	if d := walk(v, 1); d <= limit {
		return d
	}
	return limit + 1
}

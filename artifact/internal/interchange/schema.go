package interchange

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const str = `{"type":"string"}`

// Schemas are closed (additionalProperties false at every level) JSON Schemas
// draft 2020-12 of the data accepted by each output; they are published by
// artifact_capabilities_v1. Semantic rules (dates, zones, e-mail, bounds) are
// enforced by the renderers, which give a precise message.
var schemaSources = map[string]string{
	OutputICS: `{"type":"object","additionalProperties":false,"required":["events"],"properties":{
"name":` + str + `,"method":{"enum":["PUBLISH","REQUEST","CANCEL"]},
"events":{"type":"array","minItems":1,"maxItems":500,"items":{"type":"object","additionalProperties":false,"required":["start","summary"],"properties":{
"uid":` + str + `,"dtstamp":` + str + `,"start":` + str + `,"end":` + str + `,"duration_minutes":{"type":"integer","minimum":1},"duration_days":{"type":"integer","minimum":1,"maximum":366},
"all_day":{"type":"boolean"},"timezone":` + str + `,"summary":` + str + `,"description":` + str + `,"location":` + str + `,"url":` + str + `,
"status":{"enum":["TENTATIVE","CONFIRMED","CANCELLED"]},
"organizer":{"type":"object","additionalProperties":false,"required":["email"],"properties":{"name":` + str + `,"email":` + str + `}},
"attendees":{"type":"array","maxItems":100,"items":{"type":"object","additionalProperties":false,"required":["email"],"properties":{"name":` + str + `,"email":` + str + `,"role":{"enum":["REQ-PARTICIPANT","OPT-PARTICIPANT","CHAIR"]},"rsvp":{"type":"boolean"}}}},
"rrule":{"type":"object","additionalProperties":false,"required":["freq"],"properties":{"freq":{"enum":["DAILY","WEEKLY","MONTHLY","YEARLY"]},"interval":{"type":"integer","minimum":1,"maximum":999},"count":{"type":"integer","minimum":1,"maximum":10000},"until":` + str + `,"by_day":{"type":"array","maxItems":7,"items":{"enum":["MO","TU","WE","TH","FR","SA","SU"]}}}}
}}}}}`,
	OutputVCF: `{"type":"object","additionalProperties":false,"required":["cards"],"properties":{
"cards":{"type":"array","minItems":1,"maxItems":500,"items":{"type":"object","additionalProperties":false,"required":["fn"],"properties":{
"uid":` + str + `,"fn":` + str + `,
"n":{"type":"object","additionalProperties":false,"properties":{"family":` + str + `,"given":` + str + `,"additional":` + str + `,"prefix":` + str + `,"suffix":` + str + `}},
"org":{"type":"array","maxItems":50,"items":` + str + `},"title":` + str + `,
"email":{"type":"array","maxItems":50,"items":{"type":"object","additionalProperties":false,"required":["value"],"properties":{"value":` + str + `,"type":{"enum":["work","home"]}}}},
"tel":{"type":"array","maxItems":50,"items":{"type":"object","additionalProperties":false,"required":["value"],"properties":{"value":` + str + `,"type":{"enum":["work","home","cell","voice","fax","text"]}}}},
"adr":{"type":"array","maxItems":50,"items":{"type":"object","additionalProperties":false,"properties":{"type":{"enum":["work","home"]},"po_box":` + str + `,"extended":` + str + `,"street":` + str + `,"locality":` + str + `,"region":` + str + `,"postal_code":` + str + `,"country":` + str + `}}},
"url":{"type":"array","maxItems":50,"items":` + str + `},"note":` + str + `
}}}}}`,
	OutputCSV: `{"type":"object","additionalProperties":false,"required":["rows"],"properties":{
"headers":{"type":"array","maxItems":256,"items":` + str + `},
"rows":{"type":"array","maxItems":50000,"items":{"anyOf":[
{"type":"array","maxItems":256,"items":{"type":["string","number","boolean","null"]}},
{"type":"object","maxProperties":256,"additionalProperties":{"type":["string","number","boolean","null"]}}]}}}}`,
	"csv.options": `{"type":"object","additionalProperties":false,"properties":{
"separator":{"enum":[",",";","\t","|"]},"bom":{"type":"boolean"},"line_ending":{"enum":["lf","crlf"]},"quote":{"enum":["minimal","all"]},
"expected_columns":{"type":"integer","minimum":1,"maximum":256},"expected_headers":{"type":"array","maxItems":256,"items":` + str + `},"neutralize_formulas":{"type":"boolean"}}}`,
}

var (
	compileOnce sync.Once
	compiled    map[string]*jsonschema.Schema
	compileErr  error
)

func compiledSchemas() (map[string]*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		compiled = map[string]*jsonschema.Schema{}
		c := jsonschema.NewCompiler()
		for name, src := range schemaSources {
			doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
			if err != nil {
				compileErr = fmt.Errorf("schema %s: %w", name, err)
				return
			}
			url := "mem:///" + name + ".json"
			if err := c.AddResource(url, doc); err != nil {
				compileErr = err
				return
			}
			if compiled[name], err = c.Compile(url); err != nil {
				compileErr = fmt.Errorf("schema %s: %w", name, err)
				return
			}
		}
	})
	return compiled, compileErr
}

// Schema returns the closed JSON Schema of an output ("ics", "vcf", "csv" or
// "csv.options") as a decoded value.
func Schema(name string) any {
	var v any
	if src, ok := schemaSources[name]; ok {
		_ = json.Unmarshal([]byte(src), &v)
	}
	return v
}

// validateAndDecode validates a decoded JSON value against the closed schema
// of the kind, then decodes it strictly (unknown fields refused) into target.
func validateAndDecode(kind string, data any, target any) error {
	schemas, err := compiledSchemas()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return dataErr("data is not JSON: %v", err)
	}
	// The validator needs UnmarshalJSON values (numbers as json.Number-compatible).
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return dataErr("data is not JSON: %v", err)
	}
	if err := schemas[kind].Validate(inst); err != nil {
		return dataErr("%s data does not match its schema: %s", kind, firstSchemaError(err))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(target); err != nil {
		return dataErr("%s data: %v", kind, err)
	}
	return nil
}

func firstSchemaError(err error) string {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err.Error()
	}
	for len(ve.Causes) > 0 {
		ve = ve.Causes[0]
	}
	return fmt.Sprintf("at /%s: %v", strings.Join(ve.InstanceLocation, "/"), ve.ErrorKind)
}

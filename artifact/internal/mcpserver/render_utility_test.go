package mcpserver

import (
	"encoding/base64"
	"strings"
	"testing"
)

func utilityContent(t *testing.T, res toolResult) (string, map[string]any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	file := res.Content["file"].(map[string]any)
	raw, err := base64.StdEncoding.DecodeString(file["content_base64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), file
}

func TestRenderUtilityOutputs(t *testing.T) {
	cases := []struct {
		args       map[string]any
		mime, want string
	}{
		{map[string]any{"output": "ics", "data": map[string]any{"events": []any{map[string]any{"summary": "Réunion, équipe", "start": "2026-11-10T09:00:00", "timezone": "Europe/Paris", "duration_minutes": 60}}}},
			"text/calendar", "SUMMARY:Réunion\\, équipe\r\n"},
		{map[string]any{"output": "vcf", "data": map[string]any{"cards": []any{map[string]any{"fn": "Ada"}}}},
			"text/vcard", "BEGIN:VCARD\r\nVERSION:4.0\r\n"},
		{map[string]any{"output": "csv", "data": map[string]any{"headers": []any{"a", "b"}, "rows": []any{[]any{1, "x;y"}}}, "options": map[string]any{"separator": ";", "bom": true}},
			"text/csv", "\xEF\xBB\xBFa;b\r\n1;\"x;y\"\r\n"},
	}
	for _, c := range cases {
		var first string
		for i := 0; i < 5; i++ {
			res := call(t, Deps{}, "artifact_render_v1", c.args)
			body, file := utilityContent(t, res)
			if file["mime_type"] != c.mime || !strings.Contains(body, c.want) {
				t.Fatalf("%v: mime %v body %q", c.args["output"], file["mime_type"], body)
			}
			if got := hexSHA([]byte(body)); got != file["sha256"] {
				t.Fatalf("sha256 %v != %s", file["sha256"], got)
			}
			if i == 0 {
				first = file["sha256"].(string)
			} else if file["sha256"] != first {
				t.Fatalf("render %d of %v differs", i, c.args["output"])
			}
		}
	}
}

func TestRenderUtilityErrors(t *testing.T) {
	good := map[string]any{"events": []any{map[string]any{"summary": "x", "start": "2026-01-01", "all_day": true}}}
	cases := map[string]struct {
		args map[string]any
		code string
	}{
		"invalid data":      {map[string]any{"output": "ics", "data": map[string]any{"events": []any{map[string]any{"summary": "x"}}}}, "data_invalid"},
		"unknown key":       {map[string]any{"output": "ics", "data": map[string]any{"events": []any{}, "bogus": 1}}, "data_invalid"},
		"missing data":      {map[string]any{"output": "ics"}, "invalid_arguments"},
		"foreign argument":  {map[string]any{"output": "ics", "data": good, "context": map[string]any{}}, "invalid_arguments"},
		"options on ics":    {map[string]any{"output": "ics", "data": good, "options": map[string]any{}}, "invalid_arguments"},
		"ragged csv":        {map[string]any{"output": "csv", "data": map[string]any{"headers": []any{"a"}, "rows": []any{[]any{"1", "2"}}}}, "data_invalid"},
		"too many csv rows": {map[string]any{"output": "csv", "data": map[string]any{"rows": make([]any, 50001)}}, "data_invalid"},
	}
	for name, c := range cases {
		res := call(t, Deps{}, "artifact_render_v1", c.args)
		if !res.IsError || res.Content["error"] != c.code {
			t.Errorf("%s: %v", name, res.Content)
		}
	}
}

func TestCapabilitiesListUtilityOutputs(t *testing.T) {
	caps := call(t, Deps{}, "artifact_capabilities_v1", nil)
	list, _ := caps.Content["utility_outputs"].([]any)
	if len(list) != 3 {
		t.Fatalf("utility_outputs: %v", caps.Content["utility_outputs"])
	}
	for _, e := range list {
		m := e.(map[string]any)
		if schema, _ := m["data_schema"].(map[string]any); schema["additionalProperties"] != false {
			t.Errorf("%v: data_schema must be closed", m["output"])
		}
	}
}

// A render without template and with a template output keeps its v0.1/v0.2 behaviour.
func TestRenderWithoutTemplateStillV01(t *testing.T) {
	res := call(t, Deps{}, "artifact_render_v1", map[string]any{"format": "markdown", "content": "# t", "output_format": "pdf"})
	if res.IsError {
		t.Fatalf("%v", res.Content)
	}
	res = call(t, Deps{}, "artifact_render_v1", map[string]any{"output": "pdf"})
	if !res.IsError {
		t.Fatal("pdf without template and content must still fail")
	}
}

package mcpserver

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/mjml/mjmltest"
)

func TestRenderMessageThroughTheTool(t *testing.T) {
	deps := Deps{MJML: mjmltest.Runner(t)}
	for output, mime := range map[string]string{"html": "text/html; charset=utf-8", "txt": "text/plain; charset=utf-8", "eml": "message/rfc822"} {
		r := call(t, deps, "artifact_render_v1", goldenArgs(t, "support-reply", "default", output, "final"))
		if r.IsError {
			t.Fatalf("%s: %v", output, r.Content)
		}
		file := r.Content["file"].(map[string]any)
		if file["mime_type"] != mime || file["filename"] != "main."+output {
			t.Fatalf("%s: file %v", output, file)
		}
		body, err := base64.StdEncoding.DecodeString(file["content_base64"].(string))
		if err != nil || len(body) == 0 {
			t.Fatalf("%s: content: %v", output, err)
		}
		if output == "eml" && !strings.Contains(string(body), "multipart/alternative") {
			t.Fatalf("eml: %s", body[:200])
		}
		if r.Content["artifact"].(map[string]any)["manifest"].(map[string]any)["mime_type"] == "application/octet-stream" {
			t.Fatalf("%s: legacy manifest has no mime type", output)
		}
	}
	// no engine: stable code
	r := call(t, Deps{}, "artifact_render_v1", goldenArgs(t, "support-reply", "default", "html", "final"))
	if !r.IsError || r.Content["error"] != "engine_unavailable" {
		t.Fatalf("no engine: %v", r.Content)
	}
}

func TestCapabilitiesListsMJML(t *testing.T) {
	for _, tc := range []struct {
		deps Deps
		want bool
	}{{Deps{MJML: mjmltest.Runner(t)}, true}, {Deps{}, false}} {
		r := call(t, tc.deps, "artifact_capabilities_v1", nil)
		found := false
		for _, e := range r.Content["engines"].([]any) {
			m := e.(map[string]any)
			if m["name"] == "mjml" {
				found = true
				if m["available"] != tc.want || m["version"] != "5.4.1" {
					t.Fatalf("mjml engine: %v", m)
				}
			}
		}
		if !found {
			t.Fatal("mjml engine not listed")
		}
	}
}

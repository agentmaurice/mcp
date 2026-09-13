package browser

import "testing"

func TestParseMetaRefreshTarget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		html string
		want string
	}{
		{
			name: "standard",
			html: `<!DOCTYPE html><html><head><meta http-equiv="refresh" content="0;url=/en/"></head><body></body></html>`,
			want: "/en/",
		},
		{
			name: "spaced url",
			html: `<meta http-equiv="refresh" content="0; url=https://example.com/path">`,
			want: "https://example.com/path",
		},
		{
			name: "case insensitive",
			html: `<META HTTP-EQUIV="Refresh" CONTENT="1;URL='/fr/'">`,
			want: "/fr/",
		},
		{
			name: "absent",
			html: `<html><body>hello</body></html>`,
			want: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseMetaRefreshTarget(tc.html)
			if got != tc.want {
				t.Fatalf("parseMetaRefreshTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMaybeResolveHTMLRedirectAgentMaurice(t *testing.T) {
	t.Parallel()

	resolved, ok := maybeResolveHTMLRedirect(t.Context(), "https://agentmaurice.ai")
	if !ok {
		t.Fatal("expected meta-refresh resolution for agentmaurice.ai")
	}
	if resolved != "https://agentmaurice.ai/en/" && resolved != "https://agentmaurice.ai/fr/" {
		t.Fatalf("unexpected resolved URL: %s", resolved)
	}
}

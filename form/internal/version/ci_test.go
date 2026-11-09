package version

import (
	"os"
	"strings"
	"testing"
)

// Keep the pipeline keys intact: a blind rename of the MCP name once turned
// `artifacts:` into an invalid GitLab CI key in a sibling MCP.
func TestGitlabCIKeysAreValid(t *testing.T) {
	b, err := os.ReadFile("../../.gitlab-ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	ci := string(b)
	for _, want := range []string{"  artifacts:\n", "      artifacts: true\n"} {
		if !strings.Contains(ci, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

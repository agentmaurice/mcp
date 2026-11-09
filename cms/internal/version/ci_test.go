package version

import (
	"os"
	"strings"
	"testing"
)

// A blind artifact -> cms rename once turned `artifacts:` into `cmss:`,
// an invalid GitLab CI key. Keep the pipeline keys intact.
func TestGitlabCIKeysAreValid(t *testing.T) {
	b, err := os.ReadFile("../../.gitlab-ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	ci := string(b)
	if strings.Contains(ci, "cmss") {
		t.Fatal("invalid key `cmss` in .gitlab-ci.yml")
	}
	for _, want := range []string{"  artifacts:\n", "      artifacts: true\n"} {
		if !strings.Contains(ci, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

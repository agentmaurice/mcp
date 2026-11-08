package version

import (
	"os"
	"strings"
	"testing"
)

// A blind artifact -> social rename once turned `artifacts:` into `socials:`,
// an invalid GitLab CI key. Keep the pipeline keys intact.
func TestGitlabCIKeysAreValid(t *testing.T) {
	b, err := os.ReadFile("../../.gitlab-ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	ci := string(b)
	if strings.Contains(ci, "socials") {
		t.Fatal("invalid key `socials` in .gitlab-ci.yml")
	}
	for _, want := range []string{"  artifacts:\n", "      artifacts: true\n"} {
		if !strings.Contains(ci, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

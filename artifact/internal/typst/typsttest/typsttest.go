// Package typsttest finds a Typst engine for tests: TYPST_BIN, then `typst` in
// PATH (version 0.15.1 only), then the public image ghcr.io/typst/typst:0.15.1
// through Docker. When none is usable the test is skipped locally, but FAILS
// when Typst is required (ARTIFACT_REQUIRE_TYPST=1 or CI set), so a green CI
// always means a real render ran.
package typsttest

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/typst"
)

// Image is the pinned public image used when no local binary is available.
const Image = "ghcr.io/typst/typst:" + template.TypstVersion

// Required reports whether a missing Typst engine must fail the tests.
func Required() bool {
	return os.Getenv("ARTIFACT_REQUIRE_TYPST") == "1" || os.Getenv("CI") != ""
}

// Runner returns a usable runner or skips the test.
func Runner(t testing.TB) typst.Runner {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	r := typst.FromEnv()
	r.DockerImage = ""
	if r.Bin != "" {
		if v, err := r.Version(ctx); err == nil && v == template.TypstVersion {
			return r
		}
		r.Bin = ""
	}
	if _, err := exec.LookPath("docker"); err == nil {
		d := typst.Runner{DockerImage: Image}
		if v, err := d.Version(ctx); err == nil && v == template.TypstVersion {
			return d
		}
	}
	if Required() {
		t.Fatalf("Typst %s is required here (CI or ARTIFACT_REQUIRE_TYPST=1) but no engine is usable: set TYPST_BIN or let Docker run %s", template.TypstVersion, Image)
	}
	t.Skipf("no Typst %s engine (set TYPST_BIN, or make Docker able to run %s)", template.TypstVersion, Image)
	return typst.Runner{}
}

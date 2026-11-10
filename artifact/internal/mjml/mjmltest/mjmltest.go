// Package mjmltest finds an MJML engine for tests: MJML_NODE_BIN with MJML_DIR,
// else a Node in PATH with MJML installed by `npm ci` from the committed
// lockfile (docker/mjml) into a temporary folder (network needed once). When
// none is usable the test is skipped locally, but FAILS when MJML is required
// (ARTIFACT_REQUIRE_MJML=1 or CI set), so a green CI always means a real render ran.
package mjmltest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/mjml"
	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
)

// Required reports whether a missing MJML engine must fail the tests.
func Required() bool {
	return os.Getenv("ARTIFACT_REQUIRE_MJML") == "1" || os.Getenv("CI") != ""
}

func engineSource() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "docker", "mjml")
}

var (
	once    sync.Once
	cached  mjml.Runner
	cacheOK bool
	cacheEr string
)

func install() {
	r := mjml.FromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if r.Available() {
		if v, err := r.Version(ctx); err == nil && v == template.MJMLVersion {
			cached, cacheOK = r, true
			return
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		cacheEr = "no Node binary (set MJML_NODE_BIN and MJML_DIR)"
		return
	}
	// a version-manager shim (Volta, asdf) needs its own HOME: use the real binary
	if out, err := exec.CommandContext(ctx, node, "-p", "process.execPath").Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		node = strings.TrimSpace(string(out))
	}
	if _, err := exec.LookPath("npm"); err != nil {
		cacheEr = "no npm to install MJML (set MJML_NODE_BIN and MJML_DIR)"
		return
	}
	dir, err := os.MkdirTemp("", "artifact-mjml-engine-")
	if err != nil {
		cacheEr = err.Error()
		return
	}
	src := engineSource()
	for _, f := range []string{"package.json", "package-lock.json", "render.mjs"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			cacheEr = err.Error()
			return
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			cacheEr = err.Error()
			return
		}
	}
	cmd := exec.CommandContext(ctx, "npm", "ci", "--omit=dev", "--ignore-scripts", "--no-audit", "--no-fund")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		cacheEr = "npm ci failed: " + err.Error() + ": " + string(out)
		return
	}
	r = mjml.Runner{NodeBin: node, Dir: dir, MaxHeapMB: 512}
	if v, err := r.Version(ctx); err != nil || v != template.MJMLVersion {
		cacheEr = "installed MJML is not " + template.MJMLVersion
		return
	}
	cached, cacheOK = r, true
}

// Runner returns a usable runner or skips the test.
func Runner(t testing.TB) mjml.Runner {
	t.Helper()
	once.Do(install)
	if cacheOK {
		return cached
	}
	if Required() {
		t.Fatalf("MJML %s is required here (CI or ARTIFACT_REQUIRE_MJML=1) but no engine is usable: %s", template.MJMLVersion, cacheEr)
	}
	t.Skipf("no MJML %s engine: %s", template.MJMLVersion, cacheEr)
	return mjml.Runner{}
}

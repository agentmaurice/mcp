// Package mjml runs the MJML compiler (a pinned version, see
// template.MJMLVersion) through Node, in a sub-process: no inherited
// environment, no proxy, a working directory it may read and nothing else
// (Node permission model: no other file, no child process), a time limit, a
// heap limit and a bounded output.
//
// The Node binary and the MJML folder (render.mjs and node_modules, installed
// from a committed lockfile) come from the environment, like TYPST_BIN:
// MJML_NODE_BIN and MJML_DIR. MJML itself makes no network call; the process is
// not isolated from the network at the kernel level, and the environment carries
// only dead proxies.
package mjml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
)

// Runner drives `node <dir>/render.mjs`.
type Runner struct {
	NodeBin   string // Node binary (MJML_NODE_BIN, else `node` in PATH)
	Dir       string // folder holding render.mjs and node_modules (MJML_DIR)
	MaxHeapMB int    // V8 heap limit (ARTIFACT_RENDER_MAX_MEMORY_MB, default 512)
}

// FromEnv builds a Runner from MJML_NODE_BIN, MJML_DIR and ARTIFACT_RENDER_MAX_MEMORY_MB.
func FromEnv() Runner {
	r := Runner{Dir: os.Getenv("MJML_DIR"), MaxHeapMB: 512}
	if bin := os.Getenv("MJML_NODE_BIN"); bin != "" {
		r.NodeBin = bin
	} else if p, err := exec.LookPath("node"); err == nil {
		r.NodeBin = p
	}
	if v, err := strconv.Atoi(os.Getenv("ARTIFACT_RENDER_MAX_MEMORY_MB")); err == nil && v > 0 {
		r.MaxHeapMB = v
	}
	return r
}

// Available reports whether an engine is configured.
func (r Runner) Available() bool { return r.NodeBin != "" && r.Dir != "" }

func unavailable(format string, args ...any) error {
	return &template.Error{Code: template.CodeEngineUnavailable, Message: fmt.Sprintf(format, args...)}
}

// Version returns the MJML version installed in Dir, read from its package.json.
func (r Runner) Version(context.Context) (string, error) {
	if !r.Available() {
		return "", unavailable("no MJML engine found (set MJML_NODE_BIN and MJML_DIR)")
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "render.mjs")); err != nil {
		return "", unavailable("MJML_DIR %q has no render.mjs", r.Dir)
	}
	b, err := os.ReadFile(filepath.Join(r.Dir, "node_modules", "mjml", "package.json"))
	if err != nil {
		return "", unavailable("MJML is not installed in MJML_DIR %q (npm ci)", r.Dir)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil || pkg.Version == "" {
		return "", unavailable("unreadable MJML package.json in %q", r.Dir)
	}
	return pkg.Version, nil
}

// capWriter keeps at most max bytes and cancels the sub-process beyond it.
type capWriter struct {
	buf      bytes.Buffer
	max      int
	cancel   context.CancelFunc
	overflow bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		w.overflow = true
		w.cancel()
		return 0, errors.New("output limit exceeded")
	}
	return w.buf.Write(p)
}

// Compile runs MJML on req.Entry (relative to req.Dir) and returns the HTML.
func (r Runner) Compile(ctx context.Context, req template.MJMLRequest) ([]byte, error) {
	if _, err := r.Version(ctx); err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(req.Dir)
	if err != nil {
		return nil, &template.Error{Code: template.CodeMJMLFailed, Message: err.Error()}
	}
	engine, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return nil, unavailable("MJML_DIR: %v", err)
	}
	home, err := os.MkdirTemp("", "artifact-mjml-home-")
	if err != nil {
		return nil, &template.Error{Code: template.CodeMJMLFailed, Message: err.Error()}
	}
	defer os.RemoveAll(home)
	heap := r.MaxHeapMB
	if heap <= 0 {
		heap = 512
	}
	max := req.MaxOutputBytes
	if max <= 0 {
		max = template.DefaultMaxOutputBytes
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, r.NodeBin,
		"--permission", "--allow-fs-read="+engine, "--allow-fs-read="+dir,
		"--max-old-space-size="+strconv.Itoa(heap),
		filepath.Join(engine, "render.mjs"), filepath.Join(dir, req.Entry), dir)
	cmd.Dir = dir
	const dead = "http://127.0.0.1:9"
	cmd.Env = []string{
		"HOME=" + home, "TMPDIR=" + home, "LANG=C.UTF-8", "NODE_ENV=production",
		"HTTPS_PROXY=" + dead, "HTTP_PROXY=" + dead, "ALL_PROXY=" + dead, "NO_PROXY=",
		"https_proxy=" + dead, "http_proxy=" + dead, "all_proxy=" + dead, "no_proxy=",
	}
	out := &capWriter{max: max, cancel: cancel}
	errw := &capWriter{max: 64 << 10, cancel: func() {}}
	cmd.Stdout, cmd.Stderr = out, errw
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, &template.Error{Code: template.CodeRenderTimeout, Message: "MJML exceeded the time limit"}
	}
	if out.overflow {
		return nil, &template.Error{Code: template.CodeLimitsExceeded, Message: fmt.Sprintf("MJML output exceeds %d bytes", max)}
	}
	if runErr == nil {
		return out.buf.Bytes(), nil
	}
	var notFound *exec.Error
	if errors.As(runErr, &notFound) || errors.Is(runErr, os.ErrNotExist) {
		return nil, unavailable("%v", runErr)
	}
	return nil, &template.Error{Code: template.CodeMJMLFailed, Message: describe(errw.buf.String(), runErr, dir)}
}

// describe turns the diagnostics of render.mjs into a stable message: the
// MJML validation errors, with the working directory replaced by ".".
func describe(stderr string, err error, dir string) string {
	stderr = strings.ReplaceAll(strings.TrimSpace(stderr), dir, ".")
	lines := strings.Split(stderr, "\n")
	var payload struct {
		Errors []string `json:"errors"`
	}
	if json.Unmarshal([]byte(lines[len(lines)-1]), &payload) == nil && len(payload.Errors) > 0 {
		return "MJML refused the document: " + strings.Join(payload.Errors, "; ")
	}
	if len(stderr) > 1000 {
		stderr = stderr[:1000] + "..."
	}
	return fmt.Sprintf("MJML failed: %v: %s", err, stderr)
}

// Package typst runs the Typst command line (a pinned version, see
// template.TypstVersion) with the flags that make a render reproducible and
// confined: fixed creation timestamp, project root, system fonts ignored and
// no network (Typst packages are refused upstream).
package typst

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/template"
)

// Runner drives a Typst binary, or, for development and tests only, a pinned
// public Typst image through Docker.
type Runner struct {
	Bin         string // path or name of the typst binary
	DockerImage string // used only when Bin is empty (development and tests)
	FontPath    string // extra font directory; system fonts are always ignored
	MaxMemoryMB int    // address space limit per compilation, Linux only (0 = none)
}

// FromEnv builds a Runner from the environment: TYPST_BIN, else `typst` in
// PATH; ARTIFACT_FONT_PATH; ARTIFACT_RENDER_MAX_MEMORY_MB.
func FromEnv() Runner {
	r := Runner{FontPath: os.Getenv("ARTIFACT_FONT_PATH")}
	if bin := os.Getenv("TYPST_BIN"); bin != "" {
		r.Bin = bin
	} else if p, err := exec.LookPath("typst"); err == nil {
		r.Bin = p
	}
	if image := os.Getenv("ARTIFACT_TYPST_DOCKER_IMAGE"); image != "" && r.Bin == "" {
		r.DockerImage = image
	}
	if v, err := strconv.Atoi(os.Getenv("ARTIFACT_RENDER_MAX_MEMORY_MB")); err == nil && v > 0 {
		r.MaxMemoryMB = v
	}
	return r
}

// Available reports whether an engine is configured.
func (r Runner) Available() bool { return r.Bin != "" || r.DockerImage != "" }

// isolatedEnv is the whole environment of a Typst process: no inherited
// variable (so no TYPST_PACKAGE_PATH, TYPST_PACKAGE_CACHE_PATH, XDG_CACHE_HOME or
// HOME pointing at an existing cache; Typst 0.15.1 reads TYPST_ROOT,
// TYPST_FONT_PATHS, TYPST_IGNORE_*_FONTS, TYPST_PACKAGE_PATH,
// TYPST_PACKAGE_CACHE_PATH, TYPST_FEATURES and SOURCE_DATE_EPOCH, plus the
// system directories derived from HOME and XDG_*), every directory points into
// the per-call project, and every proxy is a dead local port with NO_PROXY
// empty, so any download fails at once.
func isolatedEnv(home, pkg, cache string) []string {
	const dead = "http://127.0.0.1:9"
	env := []string{
		"HOME=" + home, "XDG_CACHE_HOME=" + home + "/.cache", "XDG_DATA_HOME=" + home + "/.data", "XDG_CONFIG_HOME=" + home + "/.config",
		"TYPST_PACKAGE_PATH=" + pkg, "TYPST_PACKAGE_CACHE_PATH=" + cache,
		"HTTPS_PROXY=" + dead, "HTTP_PROXY=" + dead, "ALL_PROXY=" + dead, "NO_PROXY=",
		"https_proxy=" + dead, "http_proxy=" + dead, "all_proxy=" + dead, "no_proxy=",
		"LANG=C.UTF-8",
	}
	return env
}

func (r Runner) command(ctx context.Context, dir, outDir string, env []string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	switch {
	case r.Bin != "":
		if r.MaxMemoryMB > 0 && runtime.GOOS == "linux" {
			all := append([]string{"-c", fmt.Sprintf(`ulimit -v %d; exec "$@"`, r.MaxMemoryMB*1024), "sh", r.Bin}, args...)
			cmd = exec.CommandContext(ctx, "sh", all...)
		} else {
			cmd = exec.CommandContext(ctx, r.Bin, args...)
		}
		cmd.Dir = dir
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	default:
		all := []string{"run", "--rm", "--network", "none"}
		for _, kv := range env {
			all = append(all, "-e", kv)
		}
		if dir != "" {
			all = append(all, "-v", dir+":/w:ro") // the project root is read only
		}
		if outDir != "" {
			all = append(all, "-v", outDir+":/out")
		}
		all = append(all, r.DockerImage)
		all = append(all, args...)
		cmd = exec.CommandContext(ctx, "docker", all...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir()}
	}
	return cmd
}

// Version returns the version number printed by `typst --version`.
func (r Runner) Version(ctx context.Context) (string, error) {
	if !r.Available() {
		return "", &template.Error{Code: template.CodeEngineUnavailable, Message: "no typst binary found (set TYPST_BIN or install typst in PATH)"}
	}
	var out, errb bytes.Buffer
	cmd := r.command(ctx, "", "", isolatedEnv(os.TempDir(), os.TempDir()+"/.typst-pkg", os.TempDir()+"/.typst-cache"), "--version")
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", wrap(ctx, err, errb.String())
	}
	fields := strings.Fields(out.String())
	if len(fields) < 2 || fields[0] != "typst" {
		return "", &template.Error{Code: template.CodeEngineUnavailable, Message: "unexpected typst --version output: " + strings.TrimSpace(out.String())}
	}
	return fields[1], nil
}

// Compile runs one compilation. The project root is req.Dir; paths are
// relative to it. Inside Docker the directory is mounted at /w.
func (r Runner) Compile(ctx context.Context, req template.CompileRequest) error {
	root, outRoot := req.Dir, req.OutDir
	entry := req.Dir + "/" + req.Entry
	if r.Bin == "" {
		root, outRoot, entry = "/w", "/out", "/w/"+req.Entry
	}
	out := outRoot + "/" + req.Out
	// Package directories and HOME live in the output directory (outside the project
	// root) and stay empty: a package import must fail by compilation, never reach a
	// cache or the network. The root is only read.
	pkg, cache, home := outRoot+"/.typst-pkg", outRoot+"/.typst-cache", outRoot+"/.typst-home"
	for _, d := range []string{".typst-pkg", ".typst-cache", ".typst-home"} {
		if err := os.MkdirAll(req.OutDir+"/"+d, 0o755); err != nil {
			return &template.Error{Code: template.CodeCompileFailed, Message: "preparing the output directory: " + err.Error()}
		}
	}
	args := []string{"compile", "--root", root, "--package-path", pkg, "--package-cache-path", cache,
		"--creation-timestamp", strconv.FormatInt(req.Timestamp, 10),
		"--ignore-system-fonts", "--format", req.Format, "--diagnostic-format", "human"}
	if req.Format == "png" && req.PPI > 0 {
		args = append(args, "--ppi", strconv.Itoa(req.PPI))
	}
	if st, err := os.Stat(r.FontPath); r.FontPath != "" && err == nil && st.IsDir() {
		args = append(args, "--font-path", r.FontPath)
	}
	args = append(args, entry, out)
	var errb bytes.Buffer
	cmd := r.command(ctx, req.Dir, req.OutDir, isolatedEnv(home, pkg, cache), args...)
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return wrap(ctx, err, errb.String())
	}
	return nil
}

func wrap(ctx context.Context, err error, stderr string) error {
	if ctx.Err() == context.DeadlineExceeded {
		return &template.Error{Code: template.CodeRenderTimeout, Message: "compilation exceeded the time limit"}
	}
	var notFound *exec.Error
	if errors.As(err, &notFound) {
		return &template.Error{Code: template.CodeEngineUnavailable, Message: err.Error()}
	}
	stderr = strings.TrimSpace(stderr)
	if len(stderr) > 2000 {
		stderr = stderr[:2000] + "..."
	}
	return &template.Error{Code: template.CodeCompileFailed, Message: fmt.Sprintf("typst failed: %v: %s", err, stderr)}
}

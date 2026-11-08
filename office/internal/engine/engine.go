// Package engine runs the document engine CLI as a separate process.
//
// Every call gets its own temporary directory (the working directory, the
// engine's home and the only path root the engine may read or write), a bounded
// deadline and a minimal environment: no key, no token, no proxy. The CLI's
// JSON output is parsed and its failures are translated into stable codes.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Stable error codes returned to callers.
const (
	CodeUnavailable     = "engine_unavailable"
	CodeTimeout         = "engine_timeout"
	CodeFailed          = "engine_failed"
	CodeInvalidDocument = "invalid_document"
	CodeOpRejected      = "op_rejected"
	CodeUnsupported     = "unsupported"
	CodeBusy            = "engine_busy"
	CodeInvalidOutput   = "invalid_output"
)

// Error is a failure with a stable code.
type Error struct {
	Code    string
	Message string
	// Details carries structured, scrubbed context (for example rejected ops).
	Details any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Runner starts the CLI.
type Runner struct {
	Path        string        // executable (OFFICE_CLI_PATH)
	XlsxSidecar string        // spreadsheet helper binary, passed through to the CLI
	PdfiumWasm  string        // optional, passed through
	Timeout     time.Duration // per-call budget (all CLI invocations of one tool call)
	TempRoot    string        // parent of the per-call directories ("" = os temp)
	MaxOutput   int           // bytes of stdout kept
	slots       chan struct{}
}

// New builds a runner allowing at most `concurrency` simultaneous CLI processes.
func New(path, xlsxSidecar string, timeout time.Duration, concurrency int) *Runner {
	if concurrency < 1 {
		concurrency = 2
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Runner{Path: path, XlsxSidecar: xlsxSidecar, Timeout: timeout, MaxOutput: 8 << 20, slots: make(chan struct{}, concurrency)}
}

// Available reports whether an executable is configured.
func (r *Runner) Configured() bool { return r != nil && r.Path != "" }

// Call is one tool call: a private directory and a shared deadline.
type Call struct {
	r      *Runner
	Dir    string
	ctx    context.Context
	cancel context.CancelFunc
}

// Begin acquires a slot, creates the private directory and starts the deadline.
func (r *Runner) Begin(ctx context.Context) (*Call, error) {
	if !r.Configured() {
		return nil, &Error{Code: CodeUnavailable, Message: "the document engine is not configured (OFFICE_CLI_PATH)"}
	}
	// One deadline for the whole call: waiting for a slot plus running.
	cctx, cancel := context.WithTimeout(ctx, r.Timeout)
	select {
	case r.slots <- struct{}{}:
	default:
		select {
		case r.slots <- struct{}{}:
		case <-cctx.Done():
			cancel()
			return nil, &Error{Code: CodeBusy, Message: "the engine is busy"}
		}
	}
	dir, err := os.MkdirTemp(r.TempRoot, "office-call-*")
	if err != nil {
		<-r.slots
		cancel()
		return nil, &Error{Code: CodeFailed, Message: "cannot create the working directory"}
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for _, sub := range []string{"tmp", "home"} {
		_ = os.MkdirAll(filepath.Join(dir, ".engine", sub), 0o700)
	}
	return &Call{r: r, Dir: dir, ctx: cctx, cancel: cancel}, nil
}

// End removes the directory and releases the slot.
func (c *Call) End() {
	c.cancel()
	_ = os.RemoveAll(c.Dir)
	<-c.r.slots
}

// Path joins a file name to the call directory.
func (c *Call) Path(name string) string { return filepath.Join(c.Dir, name) }

// Result is the parsed JSON output of a CLI invocation.
type Result struct {
	Status     string          `json:"status"`
	Command    string          `json:"command"`
	Summary    string          `json:"summary"`
	OutputPath string          `json:"output_path"`
	Code       int             `json:"code"`
	Reason     string          `json:"error"`
	Message    string          `json:"message"`
	Detail     json.RawMessage `json:"detail"`
}

// Env returns the environment given to the CLI.
func (c *Call) env() []string {
	e := []string{
		"PATH=" + safePath(),
		"HOME=" + filepath.Join(c.Dir, ".engine", "home"),
		"TMPDIR=" + filepath.Join(c.Dir, ".engine", "tmp"),
		"LANG=C.UTF-8",
		"GENOFFICE_ALLOWED_ROOTS=" + c.Dir,
		"GENOFFICE_USER_DATA=" + filepath.Join(c.Dir, ".engine", "home"),
		// no browser or Electron download, no usage statistics
		"ELECTRON_SKIP_BINARY_DOWNLOAD=1",
		"DO_NOT_TRACK=1",
		"NODE_OPTIONS=--max-old-space-size=1024",
	}
	if c.r.XlsxSidecar != "" {
		e = append(e, "XLSX_SIDECAR_PATH="+c.r.XlsxSidecar)
	}
	if c.r.PdfiumWasm != "" {
		e = append(e, "GENOFFICE_PDFIUM_WASM="+c.r.PdfiumWasm)
	}
	return e
}

func safePath() string {
	if p := os.Getenv("PATH"); p != "" {
		return p
	}
	return "/usr/local/bin:/usr/bin:/bin"
}

type limitedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room < len(p) {
		l.overflow = true
		if room > 0 {
			l.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return l.buf.Write(p)
}

// Run executes the CLI with --json appended and returns the parsed output of a
// successful run. A failed run is translated by Translate.
func (c *Call) Run(args ...string) (*Result, error) {
	args = append(append([]string{}, args...), "--json")
	cmd := exec.CommandContext(c.ctx, c.r.Path, args...)
	cmd.Dir = c.Dir
	cmd.Env = c.env()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	out := &limitedBuffer{max: c.r.MaxOutput}
	cmd.Stdout = out
	cmd.Stderr = nil
	runErr := cmd.Run()
	if c.ctx.Err() != nil {
		return nil, &Error{Code: CodeTimeout, Message: fmt.Sprintf("the engine did not finish within %d seconds", int(c.r.Timeout/time.Second))}
	}
	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return nil, &Error{Code: CodeUnavailable, Message: "the engine could not be started"}
		}
	}
	if out.overflow {
		return nil, &Error{Code: CodeFailed, Message: "the engine output exceeds the size limit"}
	}
	var res Result
	if err := json.Unmarshal(bytes.TrimSpace(out.buf.Bytes()), &res); err != nil {
		if exitCode != 0 {
			return nil, &Error{Code: CodeFailed, Message: fmt.Sprintf("the engine failed with exit code %d without a readable result", exitCode)}
		}
		return nil, &Error{Code: CodeFailed, Message: "the engine returned an unreadable result"}
	}
	if exitCode != 0 || res.Status == "error" {
		return nil, c.translate(&res, exitCode)
	}
	if res.Status != "ok" {
		return nil, &Error{Code: CodeInvalidOutput, Message: "the engine answer does not report success"}
	}
	return &res, nil
}

// RunText runs the CLI without --json (for the version flag) and returns stdout.
func (c *Call) RunText(args ...string) (string, error) {
	cmd := exec.CommandContext(c.ctx, c.r.Path, args...)
	cmd.Dir = c.Dir
	cmd.Env = c.env()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	out := &limitedBuffer{max: 1 << 16}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		if c.ctx.Err() != nil {
			return "", &Error{Code: CodeTimeout, Message: "the engine did not answer in time"}
		}
		return "", &Error{Code: CodeUnavailable, Message: "the engine could not be started"}
	}
	return strings.TrimSpace(out.buf.String()), nil
}

type failure struct {
	Index  int    `json:"index"`
	Op     string `json:"op"`
	Error  string `json:"error"`
	Reason string `json:"reason"`
}

type detailFailures struct {
	Failures []failure `json:"failures"`
}

// translate maps a failed CLI result onto a stable code. The exit codes of the
// CLI are 1 usage, 2 file, 3 conversion failed, 4 application not available.
func (c *Call) translate(res *Result, exit int) error {
	msg := c.scrub(res.Message)
	var det detailFailures
	if len(res.Detail) > 0 {
		_ = json.Unmarshal(res.Detail, &det)
	}
	switch {
	case len(det.Failures) > 0:
		rejected := make([]map[string]any, 0, len(det.Failures))
		for _, f := range det.Failures {
			rejected = append(rejected, map[string]any{"index": f.Index, "op": c.scrub(f.Op), "reason": c.scrub(f.Reason), "message": firstLine(c.scrub(f.Error))})
		}
		return &Error{Code: CodeOpRejected, Message: firstLine(msg), Details: map[string]any{"rejected_ops": rejected}}
	case res.Reason == "outside_allowed_roots":
		return &Error{Code: CodeFailed, Message: "the engine refused a path outside its working directory"}
	case exit == 4 || res.Reason == "app_unavailable":
		return &Error{Code: CodeUnavailable, Message: "the requested operation needs a rendering application that is not available"}
	case exit == 2 || exit == 3 || res.Reason == "conversion_failed":
		return &Error{Code: CodeInvalidDocument, Message: firstLine(msg)}
	case exit == 1:
		return &Error{Code: CodeUnsupported, Message: firstLine(msg)}
	}
	if msg == "" {
		msg = fmt.Sprintf("the engine failed with exit code %d", exit)
	}
	return &Error{Code: CodeFailed, Message: firstLine(msg)}
}

var engineWord = regexp.MustCompile(`(?i)gen\s?office`)

// scrub removes the working directory and the engine name from a message.
func (c *Call) scrub(s string) string {
	s = strings.ReplaceAll(s, c.Dir, "<work>")
	s = Scrub(s)
	if len(s) > 600 {
		s = s[:600] + "..."
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

var versionLine = regexp.MustCompile(`\d+\.\d+\.\d+(?:[-+.\w]*)?`)

// Version returns the version reported by the CLI.
func (c *Call) Version() (string, error) {
	out, err := c.RunText("--version")
	if err != nil {
		return "", err
	}
	if v := versionLine.FindString(out); v != "" {
		return v, nil
	}
	return "", &Error{Code: CodeFailed, Message: "the engine did not report a version"}
}

// Slots exposes the concurrency semaphore (capacity = maximum parallel processes).
func (r *Runner) Slots() chan struct{} { return r.slots }

// A path starts at the beginning or after any character that is not part of a
// word or a path; it ends at whitespace or at a quote, bracket or backtick.
var absPath = regexp.MustCompile("(^|[^A-Za-z0-9._~/\\\\-])(/[^\\s\"'`)\\]>}]*|[A-Za-z]:\\\\[^\\s\"'`)\\]>}]*)")

// Scrub masks the engine name (case-insensitive) and every absolute path
// (`/...` or `C:\...`) in a message.
func Scrub(s string) string {
	s = engineWord.ReplaceAllString(s, "engine")
	return absPath.ReplaceAllString(s, "${1}<path>")
}

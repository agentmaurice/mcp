package bootstrap

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"go.uber.org/zap"
)

type Runner struct {
	cfg    *config.SidecarConfig
	logger *zap.Logger
}

type Result struct {
	RuntimeKind       string
	WorkspaceDir      string
	ResolvedCommitSHA string
	Duration          time.Duration
}

func NewRunner(cfg *config.SidecarConfig, logger *zap.Logger) *Runner {
	return &Runner{
		cfg:    cfg,
		logger: logger.Named("bootstrap"),
	}
}

func (r *Runner) Run(ctx context.Context, spec *RuntimeSpec) (*Result, error) {
	if spec == nil {
		return nil, nil
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	startedAt := time.Now()
	if r.cfg.Metadata == nil {
		r.cfg.Metadata = make(map[string]string)
	}
	r.cfg.Metadata["bootstrap_status"] = "running"

	timeout := r.cfg.BootstrapTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	repoURL, logRepoURL := withGitHubToken(spec.Source.RepoURL, resolveGitHubToken(spec, r.cfg))
	workspaceRoot := strings.TrimSpace(r.cfg.RuntimeWorkDir)
	if workspaceRoot == "" {
		workspaceRoot = "/tmp/mcp-sidecar-workspace"
	}
	repoDir := filepath.Join(workspaceRoot, workspaceFolderName(spec.Source.RepoURL))

	r.logger.Info("Preparing source workspace",
		zap.String("runtime_kind", spec.RuntimeKind),
		zap.String("repo_url", logRepoURL),
		zap.String("workspace", repoDir),
	)

	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		return nil, r.failMetadata(err, startedAt)
	}

	if err := r.ensureRepository(bootstrapCtx, repoURL, repoDir); err != nil {
		return nil, r.failMetadata(err, startedAt)
	}

	resolvedSHA, err := r.resolveAndCheckoutRef(bootstrapCtx, repoDir, spec.Source.Ref)
	if err != nil {
		return nil, r.failMetadata(err, startedAt)
	}

	runDir := repoDir
	if strings.TrimSpace(spec.Source.Subdir) != "" {
		runDir = filepath.Join(repoDir, spec.Source.Subdir)
	}
	if stat, err := os.Stat(runDir); err != nil || !stat.IsDir() {
		return nil, r.failMetadata(fmt.Errorf("runtime_spec.source.subdir does not exist in repository"), startedAt)
	}

	if err := r.installDependencies(bootstrapCtx, spec.NormalizedRuntimeKind(), runDir); err != nil {
		return nil, r.failMetadata(err, startedAt)
	}

	if err := os.Chdir(runDir); err != nil {
		return nil, r.failMetadata(fmt.Errorf("change runtime working directory: %w", err), startedAt)
	}

	r.cfg.LocalMCPCommand = "/bin/sh"
	r.cfg.LocalMCPArgs = []string{"-lc", spec.StartCommand}
	r.cfg.LocalMCPEnv = mergeEnvEntries(
		r.cfg.LocalMCPEnv,
		spec.Env,
		map[string]string{
			"MCP_SOURCE_DIR":          runDir,
			"MCP_SOURCE_REPO_URL":     spec.Source.RepoURL,
			"MCP_SOURCE_REF":          spec.Source.Ref,
			"MCP_RESOLVED_COMMIT_SHA": resolvedSHA,
		},
	)

	duration := time.Since(startedAt)
	r.cfg.Metadata["runtime_kind"] = spec.NormalizedRuntimeKind()
	r.cfg.Metadata["repo_url"] = spec.Source.RepoURL
	r.cfg.Metadata["ref_requested"] = spec.Source.Ref
	r.cfg.Metadata["resolved_commit_sha"] = resolvedSHA
	r.cfg.Metadata["subdir"] = strings.TrimSpace(spec.Source.Subdir)
	r.cfg.Metadata["bootstrap_status"] = "ready"
	r.cfg.Metadata["bootstrap_duration_ms"] = fmt.Sprintf("%d", duration.Milliseconds())

	r.logger.Info("Runtime source bootstrap completed",
		zap.String("runtime_kind", spec.NormalizedRuntimeKind()),
		zap.String("repo_url", logRepoURL),
		zap.String("resolved_commit_sha", resolvedSHA),
		zap.Duration("duration", duration),
		zap.String("run_dir", runDir),
	)

	return &Result{
		RuntimeKind:       spec.NormalizedRuntimeKind(),
		WorkspaceDir:      runDir,
		ResolvedCommitSHA: resolvedSHA,
		Duration:          duration,
	}, nil
}

func (r *Runner) failMetadata(err error, startedAt time.Time) error {
	duration := time.Since(startedAt)
	if r.cfg.Metadata == nil {
		r.cfg.Metadata = make(map[string]string)
	}
	r.cfg.Metadata["bootstrap_status"] = "failed"
	r.cfg.Metadata["bootstrap_duration_ms"] = fmt.Sprintf("%d", duration.Milliseconds())
	r.cfg.Metadata["bootstrap_error"] = sanitizeMetadataError(err)
	return err
}

func (r *Runner) ensureRepository(ctx context.Context, repoURL string, repoDir string) error {
	gitDir := filepath.Join(repoDir, ".git")
	if _, err := os.Stat(gitDir); errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(repoDir)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create workspace root: %w", err)
		}
		if entries, readErr := os.ReadDir(repoDir); readErr == nil && len(entries) == 0 {
			_ = os.Remove(repoDir)
		}
		if err := runCommand(ctx, "", "git", []string{"clone", "--no-checkout", repoURL, repoDir}); err != nil {
			return fmt.Errorf("git clone failed: %w", err)
		}
		return nil
	}
	if err := runCommand(ctx, "", "git", []string{"-C", repoDir, "remote", "set-url", "origin", repoURL}); err != nil {
		return fmt.Errorf("git remote set-url failed: %w", err)
	}
	if err := runCommand(ctx, "", "git", []string{"-C", repoDir, "fetch", "--tags", "--prune", "origin"}); err != nil {
		return fmt.Errorf("git fetch failed: %w", err)
	}
	return nil
}

func (r *Runner) resolveAndCheckoutRef(ctx context.Context, repoDir, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if err := runCommand(ctx, "", "git", []string{"-C", repoDir, "fetch", "--tags", "--prune", "origin"}); err != nil {
		return "", fmt.Errorf("git fetch failed: %w", err)
	}

	var resolved string
	if refSHARegexp.MatchString(ref) {
		out, err := runCommandOutput(ctx, "", "git", []string{"-C", repoDir, "rev-parse", "--verify", ref + "^{commit}"})
		if err != nil {
			return "", fmt.Errorf("commit ref %q not found: %w", ref, err)
		}
		resolved = strings.TrimSpace(out)
	} else {
		out, err := runCommandOutput(ctx, "", "git", []string{"-C", repoDir, "rev-parse", "--verify", "refs/tags/" + ref + "^{commit}"})
		if err != nil {
			return "", fmt.Errorf("tag ref %q not found: %w", ref, err)
		}
		resolved = strings.TrimSpace(out)
	}
	if resolved == "" {
		return "", fmt.Errorf("unable to resolve runtime_spec.source.ref")
	}

	if err := runCommand(ctx, "", "git", []string{"-C", repoDir, "checkout", "--detach", resolved}); err != nil {
		return "", fmt.Errorf("git checkout failed: %w", err)
	}
	if err := runCommand(ctx, "", "git", []string{"-C", repoDir, "clean", "-fdx"}); err != nil {
		return "", fmt.Errorf("git clean failed: %w", err)
	}
	return resolved, nil
}

func (r *Runner) installDependencies(ctx context.Context, runtimeKind, runDir string) error {
	switch runtimeKind {
	case "python":
		return installPythonDependencies(ctx, runDir)
	case "node":
		return installNodeDependencies(ctx, runDir)
	case "go":
		return installGoDependencies(ctx, runDir)
	case "docker":
		return nil
	default:
		return fmt.Errorf("unsupported runtime kind %q", runtimeKind)
	}
}

func installPythonDependencies(ctx context.Context, runDir string) error {
	_, hasUV := lookupBinary("uv")
	_, hasPython3 := lookupBinary("python3")
	_, hasPip3 := lookupBinary("pip3")

	if fileExists(filepath.Join(runDir, "uv.lock")) && hasUV {
		if err := runCommand(ctx, runDir, "uv", []string{"sync", "--frozen", "--no-dev"}); err == nil {
			return nil
		}
	}

	if fileExists(filepath.Join(runDir, "requirements.txt")) {
		if hasPip3 {
			return runCommand(ctx, runDir, "pip3", []string{"install", "--no-cache-dir", "-r", "requirements.txt"})
		}
		if hasPython3 {
			return runCommand(ctx, runDir, "python3", []string{"-m", "pip", "install", "--no-cache-dir", "-r", "requirements.txt"})
		}
		return fmt.Errorf("python runtime requires pip3 or python3")
	}

	if fileExists(filepath.Join(runDir, "pyproject.toml")) {
		if hasPip3 {
			return runCommand(ctx, runDir, "pip3", []string{"install", "--no-cache-dir", "-e", "."})
		}
		if hasPython3 {
			return runCommand(ctx, runDir, "python3", []string{"-m", "pip", "install", "--no-cache-dir", "-e", "."})
		}
		return fmt.Errorf("python runtime requires pip3 or python3")
	}

	return nil
}

func installNodeDependencies(ctx context.Context, runDir string) error {
	if !fileExists(filepath.Join(runDir, "package.json")) {
		return nil
	}

	if fileExists(filepath.Join(runDir, "pnpm-lock.yaml")) {
		if _, ok := lookupBinary("pnpm"); ok {
			return runCommand(ctx, runDir, "pnpm", []string{"install", "--frozen-lockfile"})
		}
	}
	if fileExists(filepath.Join(runDir, "package-lock.json")) {
		return runCommand(ctx, runDir, "npm", []string{"ci", "--omit=dev"})
	}
	return runCommand(ctx, runDir, "npm", []string{"install", "--omit=dev"})
}

func installGoDependencies(ctx context.Context, runDir string) error {
	if !fileExists(filepath.Join(runDir, "go.mod")) {
		return nil
	}
	return runCommand(ctx, runDir, "go", []string{"mod", "download"})
}

func lookupBinary(name string) (string, bool) {
	path, err := exec.LookPath(name)
	return path, err == nil
}

func runCommand(ctx context.Context, workDir, bin string, args []string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s command failed: %w (%s)", bin, err, sanitizeCommandOutput(string(out)))
	}
	return nil
}

func runCommandOutput(ctx context.Context, workDir, bin string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s command failed: %w (%s)", bin, err, sanitizeCommandOutput(string(out)))
	}
	return string(out), nil
}

func workspaceFolderName(repoURL string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(repoURL)))
	return hex.EncodeToString(sum[:16])
}

func withGitHubToken(repoURL, token string) (string, string) {
	repoURL = strings.TrimSpace(repoURL)
	if strings.TrimSpace(token) == "" {
		return repoURL, repoURL
	}
	parsed, err := url.Parse(repoURL)
	if err != nil {
		return repoURL, repoURL
	}
	parsed.User = url.UserPassword("x-access-token", token)
	return parsed.String(), sanitizeRepoURL(parsed.String())
}

func resolveGitHubToken(spec *RuntimeSpec, cfg *config.SidecarConfig) string {
	if spec != nil && strings.TrimSpace(spec.Auth.GitHubToken) != "" {
		return strings.TrimSpace(spec.Auth.GitHubToken)
	}
	return strings.TrimSpace(os.Getenv("MCP_SIDECAR_GITHUB_TOKEN"))
}

func mergeEnvEntries(base []string, maps ...map[string]string) []string {
	out := make(map[string]string, len(base))
	for _, entry := range base {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		out[key] = parts[1]
	}
	for _, m := range maps {
		for k, v := range m {
			key := strings.TrimSpace(k)
			if key == "" {
				continue
			}
			out[key] = strings.TrimSpace(v)
		}
	}

	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	merged := make([]string, 0, len(keys))
	for _, k := range keys {
		merged = append(merged, fmt.Sprintf("%s=%s", k, out[k]))
	}
	return merged
}

func fileExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && !stat.IsDir()
}

func sanitizeMetadataError(err error) string {
	const maxLen = 500
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > maxLen {
		msg = msg[:maxLen]
	}
	return strings.ReplaceAll(msg, "\n", " ")
}

func sanitizeCommandOutput(raw string) string {
	msg := strings.TrimSpace(raw)
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	for strings.Contains(msg, "  ") {
		msg = strings.ReplaceAll(msg, "  ", " ")
	}
	msg = redactGitHubTokenInURL(msg)
	const maxLen = 700
	if len(msg) > maxLen {
		msg = msg[:maxLen]
	}
	return msg
}

func redactGitHubTokenInURL(raw string) string {
	start := strings.Index(raw, "https://")
	for start >= 0 {
		end := strings.Index(raw[start:], " ")
		if end < 0 {
			end = len(raw)
		} else {
			end = start + end
		}
		chunk := raw[start:end]
		if strings.Contains(chunk, "@github.com") && strings.Contains(chunk, ":") {
			if at := strings.Index(chunk, "@github.com"); at > 0 {
				prefix := chunk[:at]
				if colon := strings.LastIndex(prefix, ":"); colon > strings.Index(prefix, "//")+1 {
					raw = raw[:start+colon+1] + "REDACTED" + raw[start+at:]
				}
			}
		}
		next := strings.Index(raw[start+1:], "https://")
		if next < 0 {
			break
		}
		start = start + 1 + next
	}
	return raw
}

func sanitizeRepoURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	if parsed.User != nil {
		username := parsed.User.Username()
		if username == "" {
			username = "token"
		}
		parsed.User = url.UserPassword(username, "REDACTED")
	}
	return parsed.String()
}

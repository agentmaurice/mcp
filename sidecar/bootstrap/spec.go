package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	refSHARegexp = regexp.MustCompile(`^[a-fA-F0-9]{7,40}$`)
	tagRefRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	envKeyRegexp = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

type RuntimeSpec struct {
	RuntimeKind  string            `json:"runtime_kind"`
	Source       RuntimeSource     `json:"source"`
	Auth         RuntimeAuth       `json:"auth,omitempty"`
	StartCommand string            `json:"start_command"`
	Env          map[string]string `json:"env,omitempty"`
}

type RuntimeSource struct {
	RepoURL string `json:"repo_url"`
	Ref     string `json:"ref"`
	Subdir  string `json:"subdir,omitempty"`
}

type RuntimeAuth struct {
	GitHubToken string `json:"github_token,omitempty"`
}

func ParseRuntimeSpec(raw string) (*RuntimeSpec, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	var spec RuntimeSpec
	if err := json.Unmarshal([]byte(trimmed), &spec); err != nil {
		return nil, fmt.Errorf("invalid runtime_spec JSON: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return &spec, nil
}

func (s *RuntimeSpec) Validate() error {
	if s == nil {
		return nil
	}

	kind := s.NormalizedRuntimeKind()
	switch kind {
	case "python", "node", "go", "docker":
	default:
		return fmt.Errorf("runtime_spec.runtime_kind must be one of: python, node, go, docker")
	}

	if strings.TrimSpace(s.StartCommand) == "" {
		return fmt.Errorf("runtime_spec.start_command is required")
	}
	if strings.TrimSpace(s.Source.RepoURL) == "" {
		return fmt.Errorf("runtime_spec.source.repo_url is required")
	}
	if strings.TrimSpace(s.Source.Ref) == "" {
		return fmt.Errorf("runtime_spec.source.ref is required")
	}

	if err := validateGitHubRepoURL(s.Source.RepoURL); err != nil {
		return fmt.Errorf("runtime_spec.source.repo_url: %w", err)
	}

	ref := strings.TrimSpace(s.Source.Ref)
	if !refSHARegexp.MatchString(ref) {
		if !tagRefRegexp.MatchString(ref) || strings.HasPrefix(strings.ToLower(ref), "refs/heads/") {
			return fmt.Errorf("runtime_spec.source.ref must be a tag or commit SHA")
		}
	}

	if strings.TrimSpace(s.Source.Subdir) != "" {
		clean := filepath.Clean(strings.TrimSpace(s.Source.Subdir))
		if clean == "." {
			s.Source.Subdir = ""
		} else {
			if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
				return fmt.Errorf("runtime_spec.source.subdir must stay inside repository")
			}
			s.Source.Subdir = clean
		}
	}

	for k := range s.Env {
		if !envKeyRegexp.MatchString(strings.TrimSpace(k)) {
			return fmt.Errorf("runtime_spec.env contains invalid key %q (must match %s)", k, envKeyRegexp.String())
		}
	}

	s.RuntimeKind = kind
	return nil
}

func (s *RuntimeSpec) NormalizedRuntimeKind() string {
	return strings.ToLower(strings.TrimSpace(s.RuntimeKind))
}

func (s *RuntimeSpec) IsSHARef() bool {
	if s == nil {
		return false
	}
	return refSHARegexp.MatchString(strings.TrimSpace(s.Source.Ref))
}

func validateGitHubRepoURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("only https:// GitHub URLs are allowed")
	}
	if !strings.EqualFold(parsed.Host, "github.com") {
		return fmt.Errorf("only github.com repositories are supported")
	}

	path := strings.Trim(parsed.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("expected format https://github.com/<owner>/<repo>")
	}
	return nil
}

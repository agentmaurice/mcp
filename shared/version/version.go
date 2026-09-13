// Package version provides build-time version information for AgentMaurice MCP binaries.
// These variables are populated at build time using ldflags.
//
// Build example:
//
//	go build -ldflags "-X github.com/agentmaurice/mcpchatui/mcp/shared/version.Version=v1.0.0 \
//	                   -X github.com/agentmaurice/mcpchatui/mcp/shared/version.GitCommit=$(git rev-parse HEAD) \
//	                   -X github.com/agentmaurice/mcpchatui/mcp/shared/version.GitTag=$(git describe --tags --always) \
//	                   -X github.com/agentmaurice/mcpchatui/mcp/shared/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
package version

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
)

// Build-time variables (injected via ldflags).
var (
	Version      = "dev"
	GitCommit    = "unknown"
	GitTag       = "unknown"
	BuildTime    = "unknown"
	GitBranch    = ""
	GitTreeState = ""
	Obfuscated   = "false"
	BuildProfile = "dev"
)

// Info holds all version metadata.
type Info struct {
	Version      string `json:"version"`
	GitCommit    string `json:"git_commit"`
	GitTag       string `json:"git_tag"`
	GitBranch    string `json:"git_branch,omitempty"`
	GitTreeState string `json:"git_tree_state,omitempty"`
	BuildTime    string `json:"build_time"`
	GoVersion    string `json:"go_version"`
	Platform     string `json:"platform"`
	TargetOS     string `json:"target_os"`
	TargetArch   string `json:"target_arch"`
	Obfuscated   string `json:"obfuscated"`
	BuildProfile string `json:"build_profile"`
}

// Get returns the version information.
func Get() Info {
	return Info{
		Version:      Version,
		GitCommit:    GitCommit,
		GitTag:       GitTag,
		GitBranch:    GitBranch,
		GitTreeState: GitTreeState,
		BuildTime:    BuildTime,
		GoVersion:    runtime.Version(),
		Platform:     fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		TargetOS:     runtime.GOOS,
		TargetArch:   runtime.GOARCH,
		Obfuscated:   Obfuscated,
		BuildProfile: BuildProfile,
	}
}

// String returns a human-readable version string.
func (i Info) String() string {
	return fmt.Sprintf("AgentMaurice %s (commit: %s, built: %s, go: %s, platform: %s, profile: %s)",
		i.Version, shortCommit(i.GitCommit), i.BuildTime, i.GoVersion, i.Platform, i.BuildProfile)
}

// JSON returns the version info as indented JSON.
func (i Info) JSON() string {
	data, _ := json.MarshalIndent(i, "", "  ")
	return string(data)
}

// Short returns the version string only.
func Short() string {
	return Version
}

// Full returns the full human-readable version string.
func Full() string {
	return Get().String()
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// HandleVersionFlag checks os.Args for --version and prints version info.
// Call this at the very start of main(), before flag.Parse().
// Returns true if --version was handled (caller should return).
func HandleVersionFlag() bool {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(Get().String())
		return true
	}
	return false
}

package protocol

import "time"

const Version = "v1"

type Health struct {
	Status       string `json:"status"`
	Version      string `json:"version"`
	MutationMode string `json:"mutation_mode"`
}

type Capabilities struct {
	Version             string   `json:"version"`
	Transport           string   `json:"transport"`
	Diagnostics         []string `json:"diagnostics"`
	Actions             []string `json:"actions"`
	AllowedServices     []string `json:"allowed_services"`
	MutationsEnabled    bool     `json:"mutations_enabled"`
	MaxLogLines         int      `json:"max_log_lines"`
	ArbitraryShell      bool     `json:"arbitrary_shell"`
	ArbitraryFilesystem bool     `json:"arbitrary_filesystem"`
}

type Inspection struct {
	Hostname      string  `json:"hostname"`
	OS            string  `json:"os"`
	Architecture  string  `json:"architecture"`
	UptimeSeconds float64 `json:"uptime_seconds,omitempty"`
	Load1         float64 `json:"load_1,omitempty"`
	MemoryTotal   uint64  `json:"memory_total_bytes,omitempty"`
	MemoryFree    uint64  `json:"memory_available_bytes,omitempty"`
	RootTotal     uint64  `json:"root_total_bytes,omitempty"`
	RootFree      uint64  `json:"root_free_bytes,omitempty"`
}

type Service struct {
	Name   string `json:"name"`
	Active string `json:"active"`
	Sub    string `json:"sub,omitempty"`
}

type Services struct {
	Services []Service `json:"services"`
}

type LogsRequest struct {
	Service string `json:"service"`
	Lines   int    `json:"lines,omitempty"`
}

type Logs struct {
	Service   string   `json:"service"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
	Redacted  bool     `json:"redacted"`
}

type PlanRequest struct {
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
}

type Plan struct {
	ID                string    `json:"plan_id"`
	Hash              string    `json:"plan_hash"`
	Action            string    `json:"action"`
	Target            string    `json:"target"`
	Summary           string    `json:"summary"`
	ExpiresAt         time.Time `json:"expires_at"`
	MutationMode      string    `json:"mutation_mode"`
	ConfigurationHash string    `json:"configuration_hash"`
}

type ApplyRequest struct {
	PlanID         string `json:"plan_id"`
	PlanHash       string `json:"plan_hash"`
	Actor          string `json:"actor"`
	OrganizationID string `json:"organization_id,omitempty"`
	DeploymentID   string `json:"deployment_id,omitempty"`
}

type ApplyResult struct {
	Status  string `json:"status"`
	PlanID  string `json:"plan_id"`
	AuditID string `json:"audit_id"`
	Action  string `json:"action"`
	Target  string `json:"target"`
	Output  string `json:"output,omitempty"`
}

type Error struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

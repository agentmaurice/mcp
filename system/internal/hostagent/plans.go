package hostagent

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/system/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/protocol"
)

type storedPlan struct {
	plan     protocol.Plan
	consumed bool
}

type planStore struct {
	mu    sync.Mutex
	now   func() time.Time
	ttl   time.Duration
	plans map[string]*storedPlan
}

func newPlanStore(ttl time.Duration) *planStore {
	return &planStore{now: time.Now, ttl: ttl, plans: map[string]*storedPlan{}}
}

func (s *planStore) create(cfg config.Host, request protocol.PlanRequest) (protocol.Plan, error) {
	target := strings.TrimSpace(request.Target)
	switch request.Action {
	case "service_restart":
		if !contains(cfg.AllowedServices, target) {
			return protocol.Plan{}, fmt.Errorf("target service is not allowlisted")
		}
	case "runtime_restart":
		if cfg.RuntimeRestartScript == "" {
			return protocol.Plan{}, fmt.Errorf("runtime restart is not configured")
		}
		target = "runtime"
	default:
		return protocol.Plan{}, fmt.Errorf("unsupported action")
	}
	id, err := randomID()
	if err != nil {
		return protocol.Plan{}, err
	}
	plan := protocol.Plan{
		ID:                id,
		Action:            request.Action,
		Target:            target,
		Summary:           summary(request.Action, target),
		ExpiresAt:         s.now().UTC().Add(s.ttl),
		MutationMode:      cfg.MutationMode,
		ConfigurationHash: configurationHash(cfg),
	}
	plan.Hash = planHash(plan)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.plans[id] = &storedPlan{plan: plan}
	return plan, nil
}

func (s *planStore) consume(cfg config.Host, id, suppliedHash string) (protocol.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.plans[id]
	if !ok {
		return protocol.Plan{}, fmt.Errorf("unknown plan")
	}
	if stored.consumed {
		return protocol.Plan{}, fmt.Errorf("plan already applied")
	}
	if !s.now().Before(stored.plan.ExpiresAt) {
		return protocol.Plan{}, fmt.Errorf("plan expired")
	}
	if stored.plan.ConfigurationHash != configurationHash(cfg) {
		return protocol.Plan{}, fmt.Errorf("plan is stale")
	}
	if subtle.ConstantTimeCompare([]byte(stored.plan.Hash), []byte(suppliedHash)) != 1 {
		return protocol.Plan{}, fmt.Errorf("plan hash mismatch")
	}
	stored.consumed = true
	return stored.plan, nil
}

func (s *planStore) pruneLocked() {
	now := s.now()
	for id, plan := range s.plans {
		if now.After(plan.plan.ExpiresAt.Add(s.ttl)) {
			delete(s.plans, id)
		}
	}
}

func configurationHash(cfg config.Host) string {
	services := append([]string(nil), cfg.AllowedServices...)
	sort.Strings(services)
	payload, _ := json.Marshal(struct {
		Mode     string   `json:"mode"`
		Services []string `json:"services"`
		Script   string   `json:"script"`
	}{cfg.MutationMode, services, cfg.RuntimeRestartScript})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func planHash(plan protocol.Plan) string {
	payload, _ := json.Marshal(struct {
		ID                string    `json:"id"`
		Action            string    `json:"action"`
		Target            string    `json:"target"`
		ExpiresAt         time.Time `json:"expires_at"`
		ConfigurationHash string    `json:"configuration_hash"`
	}{plan.ID, plan.Action, plan.Target, plan.ExpiresAt, plan.ConfigurationHash})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func randomID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func summary(action, target string) string {
	if action == "runtime_restart" {
		return "Restart the AgentMaurice runtime with the administrator-provided canonical script"
	}
	return fmt.Sprintf("Restart allowlisted system service %s", target)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

package poolmanager

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeProvider struct {
	kind      string
	instances []ProviderInstance
	mu        sync.Mutex
}

func (p *fakeProvider) Kind() string {
	if p.kind == "" {
		return "docker"
	}
	return p.kind
}

func (p *fakeProvider) Reconcile(_ context.Context, _ string, desired int, _ PoolSpec) error {
	if desired < 0 {
		desired = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.instances) < desired {
		idx := len(p.instances) + 1
		p.instances = append(p.instances, ProviderInstance{
			ID:        fmt.Sprintf("i%d", idx),
			Endpoint:  fmt.Sprintf("ws://i%d:9222", idx),
			Ready:     true,
			CreatedAt: time.Now(),
			LastSeen:  time.Now(),
		})
	}
	if len(p.instances) > desired {
		p.instances = append([]ProviderInstance{}, p.instances[:desired]...)
	}
	return nil
}

func (p *fakeProvider) ListInstances(_ context.Context, _ string, _ PoolSpec) ([]ProviderInstance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ProviderInstance, len(p.instances))
	copy(out, p.instances)
	if len(out) == 0 {
		out = []ProviderInstance{}
	}
	return out, nil
}

func (p *fakeProvider) DeletePool(_ context.Context, _ string, _ PoolSpec) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.instances = nil
	return nil
}

func TestManagerStickySession(t *testing.T) {
	m := &Manager{
		providers: map[string]Provider{
			"static": &fakeProvider{kind: "static", instances: []ProviderInstance{{ID: "i1", Endpoint: "ws://i1:9222", Ready: true, CreatedAt: time.Now(), LastSeen: time.Now()}}},
		},
		pools: make(map[string]*pool),
	}

	spec := PoolSpec{
		PoolID:                 "pool-a",
		Provider:               "static",
		Mode:                   "static",
		MaxSessionsPerInstance: 2,
		ProviderConfig:         map[string]any{"endpoints": []string{"ws://i1:9222"}},
	}
	if _, err := m.ReconcilePool(context.Background(), spec); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	leaseA, err := m.AllocateSession(context.Background(), "pool-a", "tenant-a", 2*time.Second)
	if err != nil {
		t.Fatalf("allocate A failed: %v", err)
	}
	leaseB, err := m.AllocateSession(context.Background(), "pool-a", "tenant-a", 2*time.Second)
	if err != nil {
		t.Fatalf("allocate B failed: %v", err)
	}

	if leaseA.InstanceID != leaseB.InstanceID {
		t.Fatalf("expected sticky session on same instance, got %s and %s", leaseA.InstanceID, leaseB.InstanceID)
	}
}

func TestManagerDynamicScaleUpOnSaturation(t *testing.T) {
	provider := &fakeProvider{kind: "docker", instances: []ProviderInstance{{ID: "i1", Endpoint: "ws://i1:9222", Ready: true, CreatedAt: time.Now(), LastSeen: time.Now()}}}
	m := &Manager{
		providers: map[string]Provider{"docker": provider},
		pools:     make(map[string]*pool),
	}

	spec := PoolSpec{
		PoolID:                 "pool-dyn",
		Provider:               "docker",
		Mode:                   "dynamic",
		MinWarmInstances:       1,
		MaxInstances:           3,
		MaxSessionsPerInstance: 1,
		ScaleUpCooldown:        time.Millisecond,
		ScaleDownCooldown:      time.Second,
		IdleTTL:                time.Minute,
		QueueTimeout:           time.Second,
	}
	if _, err := m.ReconcilePool(context.Background(), spec); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if _, err := m.AllocateSession(context.Background(), "pool-dyn", "tenant-a", time.Second); err != nil {
		t.Fatalf("first allocate failed: %v", err)
	}
	if _, err := m.AllocateSession(context.Background(), "pool-dyn", "tenant-b", 2*time.Second); err != nil {
		t.Fatalf("second allocate should trigger scale up, got: %v", err)
	}

	status, err := m.PoolStatus("pool-dyn")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if status.TotalInstances < 2 {
		t.Fatalf("expected at least 2 instances after scale up, got %d", status.TotalInstances)
	}
}

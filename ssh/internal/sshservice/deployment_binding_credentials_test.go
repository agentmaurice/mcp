package sshservice

import (
	"context"
	"strings"
	"testing"
)

type countingResolver struct {
	secret string
	err    error
	calls  int
	last   string
}

func (r *countingResolver) ResolveForTerminal(_ context.Context, canonicalRef string, _ Purpose) (string, error) {
	r.calls++
	r.last = canonicalRef
	if r.err != nil {
		return "", r.err
	}
	return r.secret, nil
}

func TestDeploymentBindingProviderDelegatesPerCall(t *testing.T) {
	resolver := &countingResolver{secret: "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-key-material\n"}
	provider := &DeploymentBindingCredentialProvider{
		Resolver: resolver,
		Bindings: map[string]DeploymentBinding{
			"prod": {TargetID: "prod", LogicalRef: "ssh-prod", Canonical: "secret://deployment/SSH_PROD_KEY"},
		},
	}
	if !provider.Available() {
		t.Fatal("configured provider should be available")
	}
	if resolver.calls != 0 {
		t.Fatal("provider decrypted during setup")
	}
	first, err := provider.Resolve(context.Background(), "ssh-prod", Purpose{TargetID: "prod", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Resolve(context.Background(), "ssh-prod", Purpose{TargetID: "prod", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 2 || resolver.last != "secret://deployment/SSH_PROD_KEY" {
		t.Fatalf("calls=%d ref=%s", resolver.calls, resolver.last)
	}
	if first.PrivateKey != resolver.secret || second.PrivateKey != resolver.secret {
		t.Fatal("terminal value was not returned to the SSH transport")
	}
}

func TestDeploymentBindingProviderRefusesWithoutAdapter(t *testing.T) {
	provider := &DeploymentBindingCredentialProvider{
		Bindings: map[string]DeploymentBinding{
			"prod": {TargetID: "prod", LogicalRef: "ssh-prod", Canonical: "secret://deployment/SSH_PROD_KEY"},
		},
	}
	if provider.Available() {
		t.Fatal("missing resolver must stay unavailable")
	}
	_, err := provider.Resolve(context.Background(), "ssh-prod", Purpose{TargetID: "prod"})
	assertCode(t, err, "credentials_provider_unavailable")

	service, err := New(DefaultConfig(), &MemoryTargetStore{SnapshotValue: TargetSnapshot{}}, provider, &fakeConnector{})
	if err != nil {
		t.Fatal(err)
	}
	health, err := service.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "degraded" || health.CredentialsProvider {
		t.Fatalf("health = %#v", health)
	}
}

func TestDeploymentBindingProviderDoesNotDecryptOnScopeRefusal(t *testing.T) {
	resolver := &countingResolver{err: NewError("binding_scope_denied", "workflow is outside recipe_ids")}
	provider := &DeploymentBindingCredentialProvider{
		Resolver: resolver,
		Bindings: map[string]DeploymentBinding{
			"prod": {TargetID: "prod", LogicalRef: "ssh-prod", Canonical: "secret://c411_apikey"},
		},
	}
	_, err := provider.Resolve(context.Background(), "ssh-prod", Purpose{TargetID: "prod"})
	assertCode(t, err, "credentials_provider_unavailable")
	if resolver.calls != 0 {
		t.Fatal("non-canonical binding reached the resolver")
	}
	provider.Bindings["prod"] = DeploymentBinding{TargetID: "prod", LogicalRef: "ssh-prod", Canonical: "secret://deployment/SSH_PROD_KEY"}
	_, err = provider.Resolve(context.Background(), "ssh-prod", Purpose{TargetID: "prod"})
	assertCode(t, err, "credentials_provider_unavailable")
	if resolver.calls != 1 {
		t.Fatalf("calls=%d", resolver.calls)
	}
	if strings.Contains(err.Error(), "secret") && strings.Contains(err.Error(), "KEY") {
		t.Fatal("error carried credential material")
	}
}

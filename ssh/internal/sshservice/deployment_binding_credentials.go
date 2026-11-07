package sshservice

import (
	"context"
	"regexp"
	"strings"
)

// deploymentSecretPattern is secret://deployment/NAME. The SSH process does not
// accept secret://c411_apikey or a raw key from the model.
var deploymentSecretPattern = regexp.MustCompile(`^secret://deployment/[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// TerminalResolver is the runtime binding boundary owned by the server.
// mcp/ssh does not decrypt and does not keep a second secret store.
type TerminalResolver interface {
	ResolveForTerminal(ctx context.Context, canonicalRef string, purpose Purpose) (string, error)
}

// DeploymentBinding is the server-published link from one SSH target to one
// canonical deployment secret. It is not an argument of the model.
type DeploymentBinding struct {
	TargetID   string
	LogicalRef string
	Canonical  string
}

// DeploymentBindingCredentialProvider resolves a published target through the
// same terminal resolver as a Workflow tool slot. Without a resolver, health
// stays unavailable and run fails closed.
type DeploymentBindingCredentialProvider struct {
	Resolver TerminalResolver
	Bindings map[string]DeploymentBinding
}

func (p *DeploymentBindingCredentialProvider) Available() bool {
	return p != nil && p.Resolver != nil && len(p.Bindings) > 0
}

func (p *DeploymentBindingCredentialProvider) Resolve(ctx context.Context, ref string, purpose Purpose) (Credential, error) {
	if p == nil || p.Resolver == nil {
		return Credential{}, NewError("credentials_provider_unavailable", "no credential provider is configured")
	}
	binding, ok := p.Bindings[purpose.TargetID]
	if !ok || binding.TargetID != purpose.TargetID || strings.TrimSpace(binding.LogicalRef) == "" || binding.LogicalRef != strings.TrimSpace(ref) {
		return Credential{}, NewError("credentials_provider_unavailable", "SSH target has no deployment binding")
	}
	if !deploymentSecretPattern.MatchString(binding.Canonical) {
		return Credential{}, NewError("credentials_provider_unavailable", "SSH target binding is not canonical")
	}
	secret, err := p.Resolver.ResolveForTerminal(ctx, binding.Canonical, purpose)
	if err != nil || secret == "" {
		return Credential{}, NewError("credentials_provider_unavailable", "deployment binding was refused")
	}
	credential := Credential{Method: "private_key", PrivateKey: secret}
	if err := validateCredential(credential); err != nil {
		return Credential{}, NewError("ssh_credential_unavailable", "resolved deployment binding is incomplete")
	}
	return credential, nil
}

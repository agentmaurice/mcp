package sshservice

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPTerminalResolverRequiresTheAuthenticatedGrant(t *testing.T) {
	var sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		if sawAuth == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "PRIVATE") {
			t.Fatal("request carried key material")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"private_key":"-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-key-material\n"}`))
	}))
	t.Cleanup(server.Close)

	missing := &HTTPTerminalResolver{BaseURL: server.URL, Grant: "am-grant:abc"}
	if _, err := missing.ResolveForTerminal(context.Background(), "secret://deployment/SSH_PROD_KEY", Purpose{}); err == nil {
		t.Fatal("missing token resolved a secret")
	}
	resolver := &HTTPTerminalResolver{BaseURL: server.URL, Token: "service-token", Grant: "am-grant:abc", HTTPClient: server.Client()}
	got, err := resolver.ResolveForTerminal(context.Background(), "secret://deployment/SSH_PROD_KEY", Purpose{TargetID: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "PRIVATE KEY") {
		t.Fatal("resolver did not return the terminal key")
	}
	if sawAuth != "Bearer service-token" {
		t.Fatalf("auth = %q", sawAuth)
	}
	provider := &DeploymentBindingCredentialProvider{
		Resolver: resolver,
		Bindings: map[string]DeploymentBinding{
			"prod": {TargetID: "prod", LogicalRef: "ssh-prod", Canonical: "secret://deployment/SSH_PROD_KEY"},
		},
	}
	credential, err := provider.Resolve(context.Background(), "ssh-prod", Purpose{TargetID: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if credential.PrivateKey == "" {
		t.Fatal("provider dropped the resolved key")
	}
}

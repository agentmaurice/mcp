package shared

import (
	"context"
	"net/http"
	"os"
	"strings"
)

type identityKey struct{}

// Identity represents caller identity and authorization context.
type Identity struct {
	TenantID string
	UserID   string
	Scopes   []string
}

// ContextWithIdentity attaches identity to context.
func ContextWithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFromContext returns identity or zero value if missing.
func IdentityFromContext(ctx context.Context) Identity {
	if ctx == nil {
		return Identity{}
	}
	if v := ctx.Value(identityKey{}); v != nil {
		if id, ok := v.(Identity); ok {
			return id
		}
	}
	return Identity{}
}

// IdentityFromRequest builds identity from HTTP headers with defaults.
func IdentityFromRequest(r *http.Request, defaultTenant string) Identity {
	if r == nil {
		return Identity{TenantID: defaultTenant}
	}
	tenant := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenant == "" {
		tenant = defaultTenant
	}
	user := strings.TrimSpace(r.Header.Get("X-User-Id"))
	scopes := parseScopes(r.Header.Get("X-Scopes"))
	return Identity{TenantID: tenant, UserID: user, Scopes: scopes}
}

// IdentityFromEnv builds identity from environment variables with defaults.
func IdentityFromEnv(defaultTenant string) Identity {
	tenant := strings.TrimSpace(os.Getenv("MCP_TENANT_ID"))
	if tenant == "" {
		tenant = defaultTenant
	}
	user := strings.TrimSpace(os.Getenv("MCP_USER_ID"))
	scopes := parseScopes(os.Getenv("MCP_SCOPES"))
	return Identity{TenantID: tenant, UserID: user, Scopes: scopes}
}

func parseScopes(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	scopes := make([]string, 0, len(parts))
	for _, part := range parts {
		val := strings.TrimSpace(part)
		if val != "" {
			scopes = append(scopes, val)
		}
	}
	return scopes
}

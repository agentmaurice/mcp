package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/tenant"
	"github.com/rs/xid"
)

// TenantRepository handles tenant data access
type TenantRepository struct {
	client *ent.Client
}

// NewTenantRepository creates a new tenant repository
func NewTenantRepository(client *ent.Client) *TenantRepository {
	return &TenantRepository{client: client}
}

// Create creates a new tenant with API key (no deployment attached).
func (r *TenantRepository) Create(ctx context.Context, name, apiKey string) (*ent.Tenant, error) {
	hash := hashAPIKey(apiKey)

	created, err := r.client.Tenant.Create().
		SetDeploymentID(xid.NilID()).
		SetName(name).
		SetAPIKeyHash(hash).
		Save(ctx)

	if err != nil {
		return nil, shared.ErrInternal("failed to create tenant", err)
	}

	return created, nil
}

// Get retrieves a tenant by ID
func (r *TenantRepository) Get(ctx context.Context, id xid.ID) (*ent.Tenant, error) {
	t, err := r.client.Tenant.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("tenant")
		}
		return nil, shared.ErrInternal("failed to get tenant", err)
	}
	return t, nil
}

// GetByAPIKey retrieves a tenant by API key
func (r *TenantRepository) GetByAPIKey(ctx context.Context, apiKey string) (*ent.Tenant, error) {
	hash := hashAPIKey(apiKey)

	t, err := r.client.Tenant.Query().
		Where(tenant.APIKeyHashEQ(hash)).
		Only(ctx)

	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("tenant")
		}
		return nil, shared.ErrInternal("failed to get tenant by API key", err)
	}

	return t, nil
}

// List retrieves all tenants
func (r *TenantRepository) List(ctx context.Context) ([]*ent.Tenant, error) {
	tenants, err := r.client.Tenant.Query().
		Order(ent.Asc(tenant.FieldName)).
		All(ctx)

	if err != nil {
		return nil, shared.ErrInternal("failed to list tenants", err)
	}

	return tenants, nil
}

// ListByDeployment retrieves tenants for a deployment.
func (r *TenantRepository) ListByDeployment(ctx context.Context, deploymentID xid.ID) ([]*ent.Tenant, error) {
	tenants, err := r.client.Tenant.Query().
		Where(tenant.DeploymentIDEQ(deploymentID)).
		Order(ent.Asc(tenant.FieldName)).
		All(ctx)
	if err != nil {
		return nil, shared.ErrInternal("failed to list tenants", err)
	}
	return tenants, nil
}

// CreateForDeployment creates a tenant under a deployment and optionally marks it default.
func (r *TenantRepository) CreateForDeployment(ctx context.Context, deploymentID xid.ID, name, apiKey string, isDefault bool) (*ent.Tenant, error) {
	hash := hashAPIKey(apiKey)

	if isDefault {
		_, _ = r.client.Tenant.Update().
			Where(tenant.DeploymentIDEQ(deploymentID), tenant.IsDefault(true)).
			SetIsDefault(false).
			Save(ctx)
	}

	created, err := r.client.Tenant.Create().
		SetDeploymentID(deploymentID).
		SetName(name).
		SetAPIKeyHash(hash).
		SetIsDefault(isDefault).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			existing, qerr := r.client.Tenant.Query().
				Where(tenant.DeploymentIDEQ(deploymentID), tenant.NameEQ(name)).
				Only(ctx)
			if qerr == nil {
				return existing, nil
			}
		}
		return nil, shared.ErrInternal("failed to create tenant", err)
	}
	return created, nil
}

// GetDefaultByDeployment returns the default tenant for a deployment.
func (r *TenantRepository) GetDefaultByDeployment(ctx context.Context, deploymentID xid.ID) (*ent.Tenant, error) {
	t, err := r.client.Tenant.Query().
		Where(tenant.DeploymentIDEQ(deploymentID), tenant.IsDefault(true)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("default tenant")
		}
		return nil, shared.ErrInternal("failed to get default tenant", err)
	}
	return t, nil
}

// EnsureTenant ensures the tenant belongs to the deployment, creating it if missing.
func (r *TenantRepository) EnsureTenant(ctx context.Context, deploymentID, tenantID xid.ID, name string) (*ent.Tenant, error) {
	if tenantID != xid.NilID() {
		t, err := r.client.Tenant.Get(ctx, tenantID)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, shared.ErrNotFound("tenant")
			}
			return nil, shared.ErrInternal("failed to get tenant", err)
		}
		if t.DeploymentID != nil && *t.DeploymentID != deploymentID {
			return nil, shared.ErrForbidden("tenant does not belong to deployment")
		}
		return t, nil
	}

	apiKey := xid.New().String()
	created, err := r.CreateForDeployment(ctx, deploymentID, name, apiKey, false)
	if err != nil {
		return nil, err
	}
	return created, nil
}

// SetDefault sets a tenant as default for its deployment.
func (r *TenantRepository) SetDefault(ctx context.Context, deploymentID, tenantID xid.ID) error {
	_, _ = r.client.Tenant.Update().
		Where(tenant.DeploymentIDEQ(deploymentID), tenant.IsDefault(true)).
		SetIsDefault(false).
		Save(ctx)
	_, err := r.client.Tenant.UpdateOneID(tenantID).SetIsDefault(true).Save(ctx)
	if err != nil {
		return shared.ErrInternal("failed to set default tenant", err)
	}
	return nil
}

// UpdateAPIKey updates a tenant's API key
func (r *TenantRepository) UpdateAPIKey(ctx context.Context, id xid.ID, apiKey string) error {
	hash := hashAPIKey(apiKey)

	err := r.client.Tenant.UpdateOneID(id).
		SetAPIKeyHash(hash).
		Exec(ctx)

	if err != nil {
		if ent.IsNotFound(err) {
			return shared.ErrNotFound("tenant")
		}
		return shared.ErrInternal("failed to update tenant API key", err)
	}

	return nil
}

// GetOrCreate retrieves a tenant by ID, creating it if it doesn't exist
func (r *TenantRepository) GetOrCreate(ctx context.Context, id xid.ID, name string) (*ent.Tenant, error) {
	// Try to get existing tenant
	t, err := r.client.Tenant.Get(ctx, id)
	if err == nil {
		return t, nil
	}

	if !ent.IsNotFound(err) {
		return nil, shared.ErrInternal("failed to get tenant", err)
	}

	// Tenant doesn't exist, create it with a generated API key
	apiKey := xid.New().String() // Generate a random API key
	hash := hashAPIKey(apiKey)

	created, err := r.client.Tenant.Create().
		SetID(id).
		SetName(name).
		SetAPIKeyHash(hash).
		Save(ctx)

	if err != nil {
		return nil, shared.ErrInternal("failed to create tenant", err)
	}

	return created, nil
}

// GetByName retrieves a tenant by name within a deployment
func (r *TenantRepository) GetByName(ctx context.Context, deploymentID xid.ID, name string) (*ent.Tenant, error) {
	t, err := r.client.Tenant.Query().
		Where(
			tenant.DeploymentIDEQ(deploymentID),
			tenant.NameEQ(name),
		).
		Only(ctx)

	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("tenant")
		}
		return nil, shared.ErrInternal("failed to get tenant by name", err)
	}

	return t, nil
}

// Delete removes a tenant by ID.
func (r *TenantRepository) Delete(ctx context.Context, id xid.ID) error {
	err := r.client.Tenant.DeleteOneID(id).Exec(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return shared.ErrNotFound("tenant")
		}
		return shared.ErrInternal("failed to delete tenant", err)
	}
	return nil
}

// hashAPIKey hashes an API key using SHA-256
func hashAPIKey(apiKey string) string {
	hash := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(hash[:])
}

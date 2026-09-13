package mcp

import (
	"context"
	"errors"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

func parseDeploymentID(deploymentIDStr string) (xid.ID, error) {
	if deploymentIDStr == "" {
		return xid.NilID(), shared.ErrValidation("deployment_id is required")
	}
	parsed, err := xid.FromString(deploymentIDStr)
	if err != nil || parsed == xid.NilID() {
		return xid.NilID(), shared.ErrValidation("invalid deployment_id format")
	}
	return parsed, nil
}

func isNotFound(err error) bool {
	var appErr *shared.AppError
	return errors.As(err, &appErr) && appErr.Code == 404
}

// resolveTenant resolves a tenant by ID or name.
// If tenantIDOrName is empty, returns (or creates) the default tenant for the deployment.
// If tenantIDOrName is a valid XID, returns that tenant (only if it belongs to the deployment).
// If tenantIDOrName is a name, looks up or creates a tenant with that name.
func resolveTenant(ctx context.Context, tenantRepo *repository.TenantRepository, deploymentID xid.ID, tenantIDOrName string, logger *zap.Logger) (xid.ID, error) {
	if deploymentID == xid.NilID() {
		return xid.NilID(), shared.ErrValidation("deployment_id is required")
	}

	// Empty tenant -> use (or create) default
	if tenantIDOrName == "" {
		defaultTenant, err := tenantRepo.GetDefaultByDeployment(ctx, deploymentID)
		if err == nil {
			logger.Debug("using default tenant", zap.String("tenant_id", defaultTenant.ID.String()))
			return defaultTenant.ID, nil
		}
		if !isNotFound(err) {
			return xid.NilID(), err
		}

		apiKey := xid.New().String()
		created, cerr := tenantRepo.CreateForDeployment(ctx, deploymentID, "default", apiKey, true)
		if cerr != nil {
			return xid.NilID(), cerr
		}
		logger.Info("created default tenant", zap.String("tenant_id", created.ID.String()))
		return created.ID, nil
	}

	// Try to parse as XID first
	if parsed, err := xid.FromString(tenantIDOrName); err == nil {
		// Verify the tenant exists and belongs to this deployment
		tenant, err := tenantRepo.Get(ctx, parsed)
		if err == nil && tenant.DeploymentID != nil && *tenant.DeploymentID == deploymentID {
			return parsed, nil
		}
		logger.Debug("xid format but tenant not found, treating as name", zap.String("original", tenantIDOrName))
	}

	// Try to find tenant by name
	tenant, err := tenantRepo.GetByName(ctx, deploymentID, tenantIDOrName)
	if err == nil {
		logger.Debug("resolved tenant by name", zap.String("name", tenantIDOrName), zap.String("tenant_id", tenant.ID.String()))
		return tenant.ID, nil
	}
	if !isNotFound(err) {
		return xid.NilID(), err
	}

	// Tenant not found by name - create it
	apiKey := xid.New().String()
	newTenant, err := tenantRepo.CreateForDeployment(ctx, deploymentID, tenantIDOrName, apiKey, false)
	if err != nil {
		logger.Error("failed to create tenant", zap.String("name", tenantIDOrName), zap.Error(err))
		return xid.NilID(), err
	}

	logger.Info("created new tenant", zap.String("name", tenantIDOrName), zap.String("tenant_id", newTenant.ID.String()))
	return newTenant.ID, nil
}

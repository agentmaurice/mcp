package repository

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/deployment"
	"github.com/rs/xid"
)

// DeploymentRepository handles deployment data access.
type DeploymentRepository struct {
	client *ent.Client
}

// NewDeploymentRepository creates a new repository.
func NewDeploymentRepository(client *ent.Client) *DeploymentRepository {
	return &DeploymentRepository{client: client}
}

// Create creates a deployment.
func (r *DeploymentRepository) Create(ctx context.Context, name string) (*ent.Deployment, error) {
	created, err := r.client.Deployment.Create().
		SetName(name).
		Save(ctx)
	if err != nil {
		return nil, shared.ErrInternal("failed to create deployment", err)
	}
	return created, nil
}

// Get retrieves a deployment by ID.
func (r *DeploymentRepository) Get(ctx context.Context, id xid.ID) (*ent.Deployment, error) {
	d, err := r.client.Deployment.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, shared.ErrNotFound("deployment")
		}
		return nil, shared.ErrInternal("failed to get deployment", err)
	}
	return d, nil
}

// List lists all deployments ordered by creation.
func (r *DeploymentRepository) List(ctx context.Context) ([]*ent.Deployment, error) {
	res, err := r.client.Deployment.Query().
		Order(ent.Asc(deployment.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, shared.ErrInternal("failed to list deployments", err)
	}
	return res, nil
}

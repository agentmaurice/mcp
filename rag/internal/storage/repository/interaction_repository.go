package repository

import (
	"context"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/interaction"
	"github.com/rs/xid"
)

// InteractionRepository handles persistence of interactions and feedbacks.
type InteractionRepository struct {
	client *ent.Client
}

// NewInteractionRepository creates a new repository.
func NewInteractionRepository(client *ent.Client) *InteractionRepository {
	return &InteractionRepository{client: client}
}

// CreateInteraction stores an interaction event.
func (r *InteractionRepository) CreateInteraction(ctx context.Context, deploymentID, tenantID xid.ID, query, answer string, duration time.Duration, chunks []shared.Chunk) error {
	create := r.client.Interaction.Create().
		SetTenantID(tenantID).
		SetEventType("interaction").
		SetQuery(query).
		SetAnswer(answer).
		SetDurationMs(int(duration.Milliseconds())).
		SetCreatedAt(time.Now())
	if deploymentID != xid.NilID() {
		create = create.SetDeploymentID(deploymentID)
	}

	_, err := create.Save(ctx)

	return err
}

// CreateFeedback stores a feedback event.
func (r *InteractionRepository) CreateFeedback(ctx context.Context, deploymentID, tenantID xid.ID, query string, helpful bool) error {
	create := r.client.Interaction.Create().
		SetTenantID(tenantID).
		SetEventType("feedback").
		SetQuery(query).
		SetHelpful(helpful).
		SetCreatedAt(time.Now())
	if deploymentID != xid.NilID() {
		create = create.SetDeploymentID(deploymentID)
	}
	_, err := create.Save(ctx)
	return err
}

// PurgeOlderThan deletes interaction rows older than cutoff.
func (r *InteractionRepository) PurgeOlderThan(ctx context.Context, cutoff time.Time) error {
	_, err := r.client.Interaction.Delete().
		Where(interaction.CreatedAtLT(cutoff)).
		Exec(ctx)
	return err
}

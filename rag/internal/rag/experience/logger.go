package experience

import (
	"context"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// Interaction represents a user interaction
type Interaction struct {
	Query        string
	Answer       string
	Chunks       []shared.Chunk
	TenantID     xid.ID
	DeploymentID xid.ID
	Timestamp    time.Time
	Duration     time.Duration
}

// Logger implements interaction logging for learning
type Logger struct {
	logger     *zap.Logger
	repo       *repository.InteractionRepository
	lastPurge  time.Time
	retention  time.Duration
	purgeEvery time.Duration
}

// NewLogger creates a new experience logger
func NewLogger(repo *repository.InteractionRepository, logger *zap.Logger) *Logger {
	return &Logger{
		logger:     logger.Named("experience"),
		repo:       repo,
		retention:  30 * 24 * time.Hour,
		purgeEvery: 24 * time.Hour,
	}
}

// LogInteraction logs a user interaction
func (l *Logger) LogInteraction(ctx context.Context, interaction *Interaction) error {
	l.logger.Info("interaction logged",
		zap.String("query", interaction.Query),
		zap.Int("chunks_used", len(interaction.Chunks)),
		zap.Duration("duration", interaction.Duration),
		zap.Time("timestamp", interaction.Timestamp))

	if l.repo != nil {
		tenantID := interaction.TenantID
		if tenantID == xid.NilID() {
			tenantID = xid.New()
		}
		deploymentID := interaction.DeploymentID
		_ = l.repo.CreateInteraction(ctx, deploymentID, tenantID, interaction.Query, interaction.Answer, interaction.Duration, interaction.Chunks)
		l.maybePurge(ctx)
	} else {
		l.logger.Debug("interaction repo not configured; skipping DB persist")
	}

	return nil
}

// LogFeedback logs user feedback on an answer
func (l *Logger) LogFeedback(ctx context.Context, query string, helpful bool) error {
	l.logger.Info("feedback received",
		zap.String("query", query),
		zap.Bool("helpful", helpful))

	if l.repo != nil {
		tenantID := xid.New()
		if ctxTenant, ok := ctx.Value("tenant_id").(xid.ID); ok {
			tenantID = ctxTenant
		}
		deploymentID := xid.NilID()
		if ctxDep, ok := ctx.Value("deployment_id").(xid.ID); ok {
			deploymentID = ctxDep
		}
		_ = l.repo.CreateFeedback(ctx, deploymentID, tenantID, query, helpful)
		l.maybePurge(ctx)
	} else {
		l.logger.Debug("interaction repo not configured; skipping DB persist")
	}

	return nil
}

// maybePurge deletes older rows once per purge interval.
func (l *Logger) maybePurge(ctx context.Context) {
	if l.repo == nil {
		return
	}
	now := time.Now()
	if now.Sub(l.lastPurge) < l.purgeEvery {
		return
	}
	l.lastPurge = now
	cutoff := now.Add(-l.retention)
	if err := l.repo.PurgeOlderThan(ctx, cutoff); err != nil {
		l.logger.Warn("purge interactions failed", zap.Error(err))
	}
}

package storage

import (
	"context"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/shared/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// WithTracing wraps a manager so that opening a tenant and every statement
// run on a tenant store produce spans. The SQL text and the tenant id are
// never recorded.
func WithTracing(m Manager) Manager {
	if m == nil {
		return nil
	}
	return &tracingManager{Manager: m, system: dbSystem(m.Dialect())}
}

type tracingManager struct {
	Manager
	system string
}

func (m *tracingManager) OpenTenant(ctx context.Context, tenantID string) (Querier, error) {
	_, span := telemetry.Tracer().Start(ctx, "memory.tenant.open",
		trace.WithAttributes(attribute.String("db.system", m.system)))
	q, err := m.Manager.OpenTenant(ctx, tenantID)
	if err != nil {
		span.SetStatus(codes.Error, "open tenant failed")
	}
	span.End()
	if err != nil {
		return nil, err
	}
	return &tracingQuerier{Querier: q, system: m.system}, nil
}

func (m *tracingManager) WithWriter(ctx context.Context, tenantID string, fn func(context.Context, Querier) error) error {
	return m.Manager.WithWriter(ctx, tenantID, func(ctx context.Context, q Querier) error {
		return fn(ctx, &tracingQuerier{Querier: q, system: m.system})
	})
}

type tracingQuerier struct {
	Querier
	system string
}

func (q *tracingQuerier) QueryContext(ctx context.Context, query string, args ...any) (*QueryResult, error) {
	ctx, span := telemetry.StartDBQuery(ctx, q.system, query)
	result, err := q.Querier.QueryContext(ctx, query, args...)
	rows := -1
	if result != nil {
		rows = len(result.Rows)
	}
	telemetry.EndDBQuery(span, rows, err)
	return result, err
}

func (q *tracingQuerier) ExecContext(ctx context.Context, query string, args ...any) (int64, error) {
	ctx, span := telemetry.StartDBQuery(ctx, q.system, query)
	affected, err := q.Querier.ExecContext(ctx, query, args...)
	telemetry.EndDBQuery(span, int(affected), err)
	return affected, err
}

func dbSystem(dialect string) string {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "postgres", "postgresql":
		return "postgresql"
	case "":
		return "duckdb"
	default:
		return strings.ToLower(strings.TrimSpace(dialect))
	}
}

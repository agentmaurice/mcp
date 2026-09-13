package jobqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/xid"
	"go.uber.org/zap"

	"github.com/nats-io/nats.go"
)

type ingestJobMessage struct {
	JobID      string    `json:"job_id"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

// NATSQueue publishes and consumes ingest jobs through NATS.
type NATSQueue struct {
	conn       *nats.Conn
	subject    string
	queueGroup string
	logger     *zap.Logger

	mu  sync.Mutex
	sub *nats.Subscription
}

// NATSConfig holds NATS queue settings.
type NATSConfig struct {
	URL              string
	Subject          string
	QueueGroup       string
	Stream           string
	Durable          string
	CreateStream     bool
	AckWaitSeconds   int
	MaxDeliver       int
	DLQEnabled       bool
	DLQSubject       string
	DLQStream        string
	DLQCreateStream  bool
	DLQAdvisoryGroup string
}

// NewNATSQueue creates a new NATS queue client.
func NewNATSQueue(cfg NATSConfig, logger *zap.Logger) (*NATSQueue, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("nats url is required")
	}
	if cfg.Subject == "" {
		return nil, fmt.Errorf("nats subject is required")
	}
	if cfg.QueueGroup == "" {
		return nil, fmt.Errorf("nats queue_group is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	conn, err := nats.Connect(
		cfg.URL,
		nats.Name("rag-ingest-queue"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to nats: %w", err)
	}

	return &NATSQueue{
		conn:       conn,
		subject:    cfg.Subject,
		queueGroup: cfg.QueueGroup,
		logger:     logger.Named("jobqueue-nats"),
	}, nil
}

// PublishIngestJob publishes a job ID to NATS.
func (q *NATSQueue) PublishIngestJob(ctx context.Context, jobID xid.ID) error {
	msg := ingestJobMessage{
		JobID:      jobID.String(),
		EnqueuedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal ingest job message: %w", err)
	}

	if err := q.conn.Publish(q.subject, payload); err != nil {
		return fmt.Errorf("failed to publish ingest job: %w", err)
	}

	// Flush to fail fast on connectivity issues.
	if err := q.conn.FlushWithContext(ctx); err != nil {
		return fmt.Errorf("failed to flush nats publish: %w", err)
	}
	return nil
}

// SubscribeIngestJobs subscribes to ingest jobs and invokes the handler.
func (q *NATSQueue) SubscribeIngestJobs(ctx context.Context, handler func(context.Context, xid.ID) error) error {
	if handler == nil {
		return fmt.Errorf("handler is required")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.sub != nil {
		return nil
	}

	sub, err := q.conn.QueueSubscribe(q.subject, q.queueGroup, func(msg *nats.Msg) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		var payload ingestJobMessage
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			q.logger.Warn("invalid ingest job message", zap.Error(err))
			return
		}

		jobID, err := xid.FromString(payload.JobID)
		if err != nil {
			q.logger.Warn("invalid ingest job id", zap.String("job_id", payload.JobID), zap.Error(err))
			return
		}

		if err := handler(ctx, jobID); err != nil {
			q.logger.Warn("ingest job handler failed", zap.String("job_id", jobID.String()), zap.Error(err))
		}
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe ingest jobs: %w", err)
	}

	q.sub = sub
	q.logger.Info("subscribed to ingest jobs", zap.String("subject", q.subject), zap.String("queue_group", q.queueGroup))

	return nil
}

// Close closes subscription and NATS connection.
func (q *NATSQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.sub != nil {
		_ = q.sub.Unsubscribe()
		q.sub = nil
	}
	if q.conn != nil && !q.conn.IsClosed() {
		q.conn.Close()
	}
	return nil
}

package jobqueue

import (
	"context"
	"time"

	"github.com/rs/xid"
)

// IngestJobPublisher publishes job IDs for asynchronous processing.
type IngestJobPublisher interface {
	PublishIngestJob(ctx context.Context, jobID xid.ID) error
}

// IngestJobSubscriber subscribes to asynchronous ingest job messages.
type IngestJobSubscriber interface {
	SubscribeIngestJobs(ctx context.Context, handler func(context.Context, xid.ID) error) error
}

// IngestJobQueue combines publishing and subscription capabilities.
type IngestJobQueue interface {
	IngestJobPublisher
	IngestJobSubscriber
	Close() error
}

// DLQReplayResult describes the outcome of replaying a DLQ message.
type DLQReplayResult struct {
	JobID             xid.ID `json:"job_id"`
	DLQSequence       uint64 `json:"dlq_sequence"`
	PublishedSequence uint64 `json:"published_sequence"`
}

// DLQReplayer replays messages from dead-letter storage.
type DLQReplayer interface {
	ReplayDLQMessage(ctx context.Context, dlqSequence uint64, deleteAfterReplay bool) (*DLQReplayResult, error)
}

// DLQMessageSummary describes one DLQ message for listing.
type DLQMessageSummary struct {
	Sequence         uint64    `json:"sequence"`
	JobID            xid.ID    `json:"job_id,omitempty"`
	Reason           string    `json:"reason"`
	CapturedAt       time.Time `json:"captured_at"`
	OriginalSubject  string    `json:"original_subject"`
	OriginalSequence uint64    `json:"original_sequence"`
	Deliveries       uint64    `json:"deliveries"`
}

// DLQListResult contains paginated DLQ listing.
type DLQListResult struct {
	Stream        string              `json:"stream"`
	Subject       string              `json:"subject"`
	FirstSequence uint64              `json:"first_sequence"`
	LastSequence  uint64              `json:"last_sequence"`
	NextBeforeSeq uint64              `json:"next_before_seq,omitempty"`
	Messages      []DLQMessageSummary `json:"messages"`
}

// DLQInspector lists messages from dead-letter storage.
type DLQInspector interface {
	ListDLQMessages(ctx context.Context, limit int, beforeSeq uint64) (*DLQListResult, error)
}

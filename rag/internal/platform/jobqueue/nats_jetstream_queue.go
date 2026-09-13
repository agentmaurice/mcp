package jobqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// NATSJetStreamQueue publishes and consumes ingest jobs through NATS JetStream.
type NATSJetStreamQueue struct {
	conn       *nats.Conn
	js         nats.JetStreamContext
	subject    string
	stream     string
	durable    string
	queueGroup string
	ackWait    time.Duration
	maxDeliver int
	logger     *zap.Logger
	dlqEnabled bool
	dlqSubject string
	dlqStream  string
	dlqGroup   string

	mu          sync.Mutex
	sub         *nats.Subscription
	advisorySub *nats.Subscription
}

type maxDeliveriesAdvisory struct {
	Type       string    `json:"type"`
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Time       time.Time `json:"time"`
	Stream     string    `json:"stream"`
	Consumer   string    `json:"consumer"`
	StreamSeq  uint64    `json:"stream_seq"`
	Deliveries uint64    `json:"deliveries"`
	Domain     string    `json:"domain,omitempty"`
}

type ingestJobDLQMessage struct {
	Reason           string                `json:"reason"`
	CapturedAt       time.Time             `json:"captured_at"`
	OriginalSubject  string                `json:"original_subject"`
	OriginalSequence uint64                `json:"original_sequence"`
	OriginalTime     time.Time             `json:"original_time"`
	OriginalHeaders  map[string][]string   `json:"original_headers,omitempty"`
	OriginalData     []byte                `json:"original_data"`
	Advisory         maxDeliveriesAdvisory `json:"advisory"`
}

// NewNATSJetStreamQueue creates a new NATS JetStream queue client.
func NewNATSJetStreamQueue(cfg NATSConfig, logger *zap.Logger) (*NATSJetStreamQueue, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("nats url is required")
	}
	if cfg.Subject == "" {
		return nil, fmt.Errorf("nats subject is required")
	}
	if cfg.QueueGroup == "" {
		return nil, fmt.Errorf("nats queue_group is required")
	}
	if cfg.Stream == "" {
		return nil, fmt.Errorf("nats stream is required")
	}
	if cfg.Durable == "" {
		return nil, fmt.Errorf("nats durable is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	ackWait := 15 * time.Minute
	if cfg.AckWaitSeconds > 0 {
		ackWait = time.Duration(cfg.AckWaitSeconds) * time.Second
	}
	maxDeliver := 10
	if cfg.MaxDeliver > 0 {
		maxDeliver = cfg.MaxDeliver
	}

	conn, err := nats.Connect(
		cfg.URL,
		nats.Name("rag-ingest-jetstream-queue"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to nats: %w", err)
	}

	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to initialize jetstream context: %w", err)
	}

	q := &NATSJetStreamQueue{
		conn:       conn,
		js:         js,
		subject:    cfg.Subject,
		stream:     cfg.Stream,
		durable:    cfg.Durable,
		queueGroup: cfg.QueueGroup,
		ackWait:    ackWait,
		maxDeliver: maxDeliver,
		logger:     logger.Named("jobqueue-nats-jetstream"),
		dlqEnabled: cfg.DLQEnabled,
		dlqSubject: cfg.DLQSubject,
		dlqStream:  cfg.DLQStream,
		dlqGroup:   cfg.DLQAdvisoryGroup,
	}

	if cfg.CreateStream {
		if err := q.ensureStream(); err != nil {
			conn.Close()
			return nil, err
		}
	}
	if q.dlqEnabled {
		if q.dlqSubject == "" {
			conn.Close()
			return nil, fmt.Errorf("nats dlq_subject is required when dlq is enabled")
		}
		if cfg.DLQCreateStream {
			if q.dlqStream == "" {
				conn.Close()
				return nil, fmt.Errorf("nats dlq_stream is required when dlq_create_stream is enabled")
			}
			if err := q.ensureDLQStream(); err != nil {
				conn.Close()
				return nil, err
			}
		}
		if q.dlqGroup == "" {
			q.dlqGroup = cfg.QueueGroup + ".dlq"
		}
	}

	return q, nil
}

// PublishIngestJob publishes a job ID to JetStream.
func (q *NATSJetStreamQueue) PublishIngestJob(ctx context.Context, jobID xid.ID) error {
	msg := ingestJobMessage{
		JobID:      jobID.String(),
		EnqueuedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal ingest job message: %w", err)
	}

	_, err = q.js.Publish(q.subject, payload, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("failed to publish ingest job to jetstream: %w", err)
	}
	return nil
}

// SubscribeIngestJobs subscribes to ingest jobs and invokes the handler.
func (q *NATSJetStreamQueue) SubscribeIngestJobs(ctx context.Context, handler func(context.Context, xid.ID) error) error {
	if handler == nil {
		return fmt.Errorf("handler is required")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.sub != nil {
		return nil
	}
	if err := q.startDLQMonitor(ctx); err != nil {
		return err
	}

	sub, err := q.js.QueueSubscribe(
		q.subject,
		q.queueGroup,
		func(msg *nats.Msg) {
			select {
			case <-ctx.Done():
				return
			default:
			}

			var payload ingestJobMessage
			if err := json.Unmarshal(msg.Data, &payload); err != nil {
				q.logger.Warn("invalid ingest job message", zap.Error(err))
				_ = msg.Ack()
				return
			}

			jobID, err := xid.FromString(payload.JobID)
			if err != nil {
				q.logger.Warn("invalid ingest job id", zap.String("job_id", payload.JobID), zap.Error(err))
				_ = msg.Ack()
				return
			}

			if err := handler(ctx, jobID); err != nil {
				q.logger.Warn("ingest job handler failed; nacking for retry",
					zap.String("job_id", jobID.String()),
					zap.Error(err))
				_ = msg.Nak()
				return
			}

			_ = msg.Ack()
		},
		nats.BindStream(q.stream),
		nats.Durable(q.durable),
		nats.DeliverAll(),
		nats.ManualAck(),
		nats.AckExplicit(),
		nats.AckWait(q.ackWait),
		nats.MaxDeliver(q.maxDeliver),
	)
	if err != nil {
		return fmt.Errorf("failed to subscribe ingest jobs on jetstream: %w", err)
	}

	q.sub = sub
	q.logger.Info("subscribed to ingest jobs on jetstream",
		zap.String("subject", q.subject),
		zap.String("stream", q.stream),
		zap.String("durable", q.durable),
		zap.String("queue_group", q.queueGroup),
		zap.Duration("ack_wait", q.ackWait),
		zap.Int("max_deliver", q.maxDeliver),
	)
	return nil
}

// ReplayDLQMessage republishes one DLQ message back to the ingest subject.
func (q *NATSJetStreamQueue) ReplayDLQMessage(ctx context.Context, dlqSequence uint64, deleteAfterReplay bool) (*DLQReplayResult, error) {
	if !q.dlqEnabled {
		return nil, fmt.Errorf("dlq is not enabled")
	}
	if q.dlqStream == "" {
		return nil, fmt.Errorf("dlq stream is not configured")
	}
	if dlqSequence == 0 {
		return nil, fmt.Errorf("dlq sequence must be > 0")
	}

	raw, err := q.js.GetMsg(q.dlqStream, dlqSequence, nats.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to get dlq message: %w", err)
	}

	var dlqMsg ingestJobDLQMessage
	if err := json.Unmarshal(raw.Data, &dlqMsg); err != nil {
		return nil, fmt.Errorf("failed to decode dlq message: %w", err)
	}
	if len(dlqMsg.OriginalData) == 0 {
		return nil, fmt.Errorf("dlq message has empty original payload")
	}

	ack, err := q.js.Publish(q.subject, dlqMsg.OriginalData, nats.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to republish dlq message: %w", err)
	}

	var jobID xid.ID
	var jobMsg ingestJobMessage
	if err := json.Unmarshal(dlqMsg.OriginalData, &jobMsg); err == nil {
		if parsed, perr := xid.FromString(jobMsg.JobID); perr == nil {
			jobID = parsed
		}
	}

	if deleteAfterReplay {
		if err := q.js.DeleteMsg(q.dlqStream, dlqSequence, nats.Context(ctx)); err != nil {
			q.logger.Warn("failed to delete dlq message after replay",
				zap.Uint64("dlq_sequence", dlqSequence),
				zap.Error(err))
		}
	}

	return &DLQReplayResult{
		JobID:             jobID,
		DLQSequence:       dlqSequence,
		PublishedSequence: ack.Sequence,
	}, nil
}

// ListDLQMessages lists DLQ messages in reverse sequence order.
func (q *NATSJetStreamQueue) ListDLQMessages(ctx context.Context, limit int, beforeSeq uint64) (*DLQListResult, error) {
	if !q.dlqEnabled {
		return nil, fmt.Errorf("dlq is not enabled")
	}
	if q.dlqStream == "" {
		return nil, fmt.Errorf("dlq stream is not configured")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	info, err := q.js.StreamInfo(q.dlqStream, nats.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to get dlq stream info: %w", err)
	}
	if info == nil || info.State.Msgs == 0 {
		return &DLQListResult{
			Stream:        q.dlqStream,
			Subject:       q.dlqSubject,
			FirstSequence: 0,
			LastSequence:  0,
			Messages:      []DLQMessageSummary{},
		}, nil
	}

	start := beforeSeq
	if start == 0 || start > info.State.LastSeq {
		start = info.State.LastSeq
	}
	if start < info.State.FirstSeq {
		return &DLQListResult{
			Stream:        q.dlqStream,
			Subject:       q.dlqSubject,
			FirstSequence: info.State.FirstSeq,
			LastSequence:  info.State.LastSeq,
			Messages:      []DLQMessageSummary{},
		}, nil
	}

	items := make([]DLQMessageSummary, 0, limit)
	for seq := start; seq >= info.State.FirstSeq && len(items) < limit; seq-- {
		raw, err := q.js.GetMsg(q.dlqStream, seq, nats.Context(ctx))
		if err != nil {
			if errors.Is(err, nats.ErrMsgNotFound) {
				if seq == 0 {
					break
				}
				continue
			}
			return nil, fmt.Errorf("failed to load dlq message sequence %d: %w", seq, err)
		}

		var dlqMsg ingestJobDLQMessage
		if err := json.Unmarshal(raw.Data, &dlqMsg); err != nil {
			// Keep malformed message visible for operations.
			items = append(items, DLQMessageSummary{
				Sequence:         raw.Sequence,
				Reason:           "malformed_dlq_payload",
				CapturedAt:       raw.Time,
				OriginalSubject:  raw.Subject,
				OriginalSequence: 0,
				Deliveries:       0,
			})
		} else {
			items = append(items, DLQMessageSummary{
				Sequence:         raw.Sequence,
				JobID:            extractJobIDFromOriginal(dlqMsg.OriginalData),
				Reason:           dlqMsg.Reason,
				CapturedAt:       dlqMsg.CapturedAt,
				OriginalSubject:  dlqMsg.OriginalSubject,
				OriginalSequence: dlqMsg.OriginalSequence,
				Deliveries:       dlqMsg.Advisory.Deliveries,
			})
		}

		if seq == 0 {
			break
		}
	}

	res := &DLQListResult{
		Stream:        q.dlqStream,
		Subject:       q.dlqSubject,
		FirstSequence: info.State.FirstSeq,
		LastSequence:  info.State.LastSeq,
		Messages:      items,
	}
	if len(items) > 0 {
		lastSeq := items[len(items)-1].Sequence
		if lastSeq > info.State.FirstSeq {
			res.NextBeforeSeq = lastSeq - 1
		}
	}
	return res, nil
}

// Close closes subscription and NATS connection.
func (q *NATSJetStreamQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.sub != nil {
		_ = q.sub.Unsubscribe()
		q.sub = nil
	}
	if q.advisorySub != nil {
		_ = q.advisorySub.Unsubscribe()
		q.advisorySub = nil
	}
	if q.conn != nil && !q.conn.IsClosed() {
		q.conn.Close()
	}
	return nil
}

func (q *NATSJetStreamQueue) ensureStream() error {
	info, err := q.js.StreamInfo(q.stream)
	if err == nil && info != nil {
		return nil
	}

	_, err = q.js.AddStream(&nats.StreamConfig{
		Name:      q.stream,
		Subjects:  []string{q.subject},
		Retention: nats.WorkQueuePolicy,
		Storage:   nats.FileStorage,
		MaxAge:    7 * 24 * time.Hour,
		Discard:   nats.DiscardOld,
	})
	if err != nil {
		// Stream may have been created concurrently by another instance.
		if _, checkErr := q.js.StreamInfo(q.stream); checkErr == nil {
			return nil
		}
		return fmt.Errorf("failed to ensure jetstream stream %s: %w", q.stream, err)
	}

	q.logger.Info("created jetstream stream",
		zap.String("stream", q.stream),
		zap.String("subject", q.subject))
	return nil
}

func (q *NATSJetStreamQueue) ensureDLQStream() error {
	info, err := q.js.StreamInfo(q.dlqStream)
	if err == nil && info != nil {
		return nil
	}

	_, err = q.js.AddStream(&nats.StreamConfig{
		Name:      q.dlqStream,
		Subjects:  []string{q.dlqSubject},
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
		MaxAge:    30 * 24 * time.Hour,
		Discard:   nats.DiscardOld,
	})
	if err != nil {
		if _, checkErr := q.js.StreamInfo(q.dlqStream); checkErr == nil {
			return nil
		}
		return fmt.Errorf("failed to ensure jetstream dlq stream %s: %w", q.dlqStream, err)
	}

	q.logger.Info("created jetstream dlq stream",
		zap.String("stream", q.dlqStream),
		zap.String("subject", q.dlqSubject))
	return nil
}

func (q *NATSJetStreamQueue) startDLQMonitor(ctx context.Context) error {
	if !q.dlqEnabled {
		return nil
	}
	if q.advisorySub != nil {
		return nil
	}

	subject := fmt.Sprintf("$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.%s.%s", q.stream, q.durable)
	sub, err := q.conn.QueueSubscribe(subject, q.dlqGroup, func(msg *nats.Msg) {
		q.handleMaxDeliveriesAdvisory(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to max deliveries advisories: %w", err)
	}
	q.advisorySub = sub

	q.logger.Info("subscribed to max deliveries advisories",
		zap.String("subject", subject),
		zap.String("queue_group", q.dlqGroup))
	return nil
}

func (q *NATSJetStreamQueue) handleMaxDeliveriesAdvisory(ctx context.Context, msg *nats.Msg) {
	var advisory maxDeliveriesAdvisory
	if err := json.Unmarshal(msg.Data, &advisory); err != nil {
		q.logger.Warn("invalid max deliveries advisory payload", zap.Error(err))
		return
	}
	if advisory.Stream == "" {
		advisory.Stream = q.stream
	}
	if advisory.Consumer == "" {
		advisory.Consumer = q.durable
	}
	if !strings.EqualFold(advisory.Stream, q.stream) || advisory.Consumer != q.durable {
		return
	}
	if advisory.StreamSeq == 0 {
		q.logger.Warn("max deliveries advisory missing stream sequence")
		return
	}

	raw, err := q.js.GetMsg(q.stream, advisory.StreamSeq, nats.Context(ctx))
	if err != nil {
		q.logger.Warn("failed to fetch max-deliver message from stream",
			zap.Uint64("stream_seq", advisory.StreamSeq),
			zap.Error(err))
		return
	}

	dlqMsg := ingestJobDLQMessage{
		Reason:           "max_deliveries",
		CapturedAt:       time.Now().UTC(),
		OriginalSubject:  raw.Subject,
		OriginalSequence: raw.Sequence,
		OriginalTime:     raw.Time,
		OriginalHeaders:  headerToMap(raw.Header),
		OriginalData:     raw.Data,
		Advisory:         advisory,
	}
	payload, err := json.Marshal(dlqMsg)
	if err != nil {
		q.logger.Warn("failed to encode dlq message",
			zap.Uint64("stream_seq", advisory.StreamSeq),
			zap.Error(err))
		return
	}

	if _, err := q.js.Publish(q.dlqSubject, payload, nats.Context(ctx)); err != nil {
		q.logger.Warn("failed to publish to dlq",
			zap.String("dlq_subject", q.dlqSubject),
			zap.Uint64("stream_seq", advisory.StreamSeq),
			zap.Error(err))
		return
	}

	// Remove poison message from primary stream after successful DLQ publication.
	if err := q.js.DeleteMsg(q.stream, advisory.StreamSeq, nats.Context(ctx)); err != nil {
		q.logger.Warn("failed to delete max-deliver message after dlq publish",
			zap.Uint64("stream_seq", advisory.StreamSeq),
			zap.Error(err))
	}
}

func headerToMap(h nats.Header) map[string][]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string][]string, len(h))
	for k, v := range h {
		vv := make([]string, len(v))
		copy(vv, v)
		out[k] = vv
	}
	return out
}

func extractJobIDFromOriginal(data []byte) xid.ID {
	if len(data) == 0 {
		return xid.NilID()
	}
	var msg ingestJobMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return xid.NilID()
	}
	id, err := xid.FromString(msg.JobID)
	if err != nil {
		return xid.NilID()
	}
	return id
}

package business

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/docint"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/pipeline"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent/ingestjob"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	_ "github.com/lib/pq"
	"github.com/rs/xid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type ingestionLLM struct {
	shared.LLMClient
	calls, failAt int
}

func (l *ingestionLLM) GenerateEmbedding(context.Context, string) ([]float64, error) {
	l.calls++
	if l.calls == l.failAt {
		return nil, errors.New("embedding provider unavailable")
	}
	return []float64{1, 0, 0}, nil
}

type ingestionVectors struct {
	shared.VectorStore
	err           error
	chunks        []shared.Chunk
	docs, deletes int
	cleanupErr    error
}

func (v *ingestionVectors) Upsert(_ context.Context, chunks []shared.Chunk) error {
	v.chunks = append(v.chunks, chunks...)
	return v.err
}

func (v *ingestionVectors) UpsertDocEmbedding(context.Context, shared.Chunk) error {
	v.docs++
	return nil
}

func (v *ingestionVectors) Delete(_ context.Context, ids []string) error {
	v.deletes += len(ids)
	return v.cleanupErr
}

// Uses a real PostgreSQL schema; every test owns and removes only its schema.
// The CI supplies this DSN from a disposable PostgreSQL service.
func TestIngestionCompletion(t *testing.T) {
	dsn := os.Getenv("RAG_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set RAG_TEST_DATABASE_DSN to run PostgreSQL ingestion regression tests")
	}
	for _, tc := range []struct {
		name       string
		failAt     int
		vectorErr  bool
		cleanupErr bool
	}{
		{name: "complete document"},
		{name: "first embedding fails", failAt: 1},
		{name: "second embedding fails", failAt: 2},
		{name: "vector write fails", vectorErr: true},
		{name: "vector cleanup fails visibly", vectorErr: true, cleanupErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("postgres", dsn)
			require.NoError(t, err)
			db.SetMaxOpenConns(1)
			schema := "ingest_test_" + xid.New().String()
			_, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, e := db.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
				require.NoError(t, e)
				require.NoError(t, db.Close())
			})
			_, err = db.ExecContext(ctx, "SET search_path TO "+schema)
			require.NoError(t, err)
			client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			require.NoError(t, client.Schema.Create(ctx))
			tenant, err := client.Tenant.Create().SetName("synthetic").SetAPIKeyHash("test-only").Save(ctx)
			require.NoError(t, err)
			jobs := repository.NewIngestJobRepository(client)
			job, err := jobs.Create(ctx, &ent.IngestJob{ID: xid.New(), TenantID: tenant.ID, Status: ingestjob.StatusRunning, SourceType: "text", SourcePayload: map[string]interface{}{"title": "Synthetic document", "content": strings.Repeat("Synthetic paragraph for complete ingestion. ", 80), "duplicate_strategy": "none"}})
			require.NoError(t, err)
			log := zap.NewNop()
			llm := &ingestionLLM{failAt: tc.failAt}
			vectors := &ingestionVectors{}
			if tc.cleanupErr {
				vectors.cleanupErr = errors.New("cleanup unavailable")
			}
			if tc.vectorErr {
				vectors.err = errors.New("qdrant unavailable")
			}
			worker := &IngestWorker{ctx: ctx, logger: log, jobRepo: jobs, docRepo: repository.NewDocumentRepository(client), chunkRepo: repository.NewChunkRepository(client), llmClient: llm, vectorStore: vectors, pipeline: pipeline.NewPipeline(docint.NewAnalyzer(log), nil, nil, nil, nil, log)}
			err = worker.processClaimedJob(job)
			state, getErr := jobs.Get(ctx, job.ID)
			require.NoError(t, getErr)
			if tc.failAt > 0 || tc.vectorErr {
				require.Error(t, err)
				require.Equal(t, ingestjob.StatusFailed, state.Status)
				require.NotEmpty(t, state.Message)
				if tc.cleanupErr {
					require.Contains(t, state.Message, "vector cleanup failed")
				}
				require.Zero(t, client.Document.Query().CountX(ctx), "failed ingestion must not block retry as a duplicate")
				require.Zero(t, client.Chunk.Query().CountX(ctx), "failed chunks must not leak into lexical search")
				require.Zero(t, vectors.docs, "failed document must not enter semantic deduplication")
				if tc.vectorErr {
					require.Equal(t, len(vectors.chunks), vectors.deletes)
				}
				// Resubmitting the same content after dependencies recover succeeds.
				llm.failAt = 0
				vectors.err, vectors.cleanupErr = nil, nil
				retry, createErr := jobs.Create(ctx, &ent.IngestJob{ID: xid.New(), TenantID: tenant.ID, Status: ingestjob.StatusRunning, SourceType: job.SourceType, SourcePayload: job.SourcePayload})
				require.NoError(t, createErr)
				require.NoError(t, worker.processClaimedJob(retry))
				retryState, getErr := jobs.Get(ctx, retry.ID)
				require.NoError(t, getErr)
				require.Equal(t, ingestjob.StatusCompleted, retryState.Status)
				require.Equal(t, 1, client.Document.Query().CountX(ctx))
			} else {
				require.NoError(t, err)
				require.Equal(t, ingestjob.StatusCompleted, state.Status)
				require.Equal(t, 100, state.Progress)
				require.Greater(t, len(vectors.chunks), 1)
				require.Equal(t, len(vectors.chunks), client.Chunk.Query().CountX(ctx))
				for _, chunk := range client.Chunk.Query().AllX(ctx) {
					require.Equal(t, chunk.ID.String(), chunk.VectorID)
				}
			}
		})
	}
}

//go:build duckdb

package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/connector"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/connector/filesystem"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer/embedder"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	"go.uber.org/zap"
)

type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embedding unavailable")
}
func (failingEmbedder) Dimensions() int { return 1 }
func (failingEmbedder) Model() string   { return "failing" }

type fixedEmbedder struct{}

func (fixedEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		embeddings[i] = []float32{0.25, 0.75}
	}
	return embeddings, nil
}
func (fixedEmbedder) Dimensions() int { return 2 }
func (fixedEmbedder) Model() string   { return "test-embedding" }

type failingConnector struct{}

func (failingConnector) Type() string { return "failing" }
func (failingConnector) ListFiles(context.Context, json.RawMessage) ([]shared.SourceFile, error) {
	return []shared.SourceFile{{Path: "broken.txt", ContentHash: "new", Size: 10, DocType: "markdown"}}, nil
}
func (failingConnector) ReadFile(context.Context, json.RawMessage, string) ([]byte, error) {
	return nil, errors.New("read failed")
}
func (failingConnector) DetectChanges(_ context.Context, current []shared.SourceFile, _ []shared.Document) (added, modified, deleted []shared.SourceFile) {
	return current, nil, nil
}

func newTestPipeline(t *testing.T) (*Pipeline, storage.Manager) {
	return newTestPipelineWithEmbedder(t, nil)
}

func newTestPipelineWithEmbedder(t *testing.T, embProvider embedder.Provider) (*Pipeline, storage.Manager) {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			Backend: "duckdb", BaseDir: t.TempDir(), TenantsDirName: "tenants", DBFilename: "brain.duckdb", WriteQueueSize: 16,
		},
		Indexing: config.IndexingConfig{Workers: 2, ChunkMaxTokens: 200, MaxDocumentBytes: 10 * 1024 * 1024},
	}
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("create storage manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return NewPipeline(manager, map[string]connector.Connector{"filesystem": filesystem.New()}, embProvider, cfg, zap.NewNop()), manager
}

func TestUpsertDocumentStoresEmbeddingsWhenProviderIsAvailable(t *testing.T) {
	pipeline, manager := newTestPipelineWithEmbedder(t, fixedEmbedder{})
	ctx := context.Background()
	req := DocumentUpsertRequest{
		TenantID: "tenant", SourceURI: "storage://brain-sources", DocumentURI: "storage://brain-sources/rdv/vector.json",
		ContentType: "application/json", Content: []byte(`{"verbatim":"indexation vectorielle"}`),
	}

	result, err := pipeline.UpsertDocument(ctx, req)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if status, err := pipeline.WaitForJob(ctx, req.TenantID, result.JobID); err != nil || status != "completed" {
		t.Fatalf("job: status=%s err=%v", status, err)
	}
	db, err := manager.OpenTenant(ctx, req.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT COUNT(*) AS total FROM chunks WHERE tenant_id = ? AND document_id = ? AND embedding IS NOT NULL AND embedding_model = ?`, req.TenantID, result.DocumentID, "test-embedding")
	if err != nil || intValue(rows.Rows[0]["total"]) == 0 {
		t.Fatalf("expected embedded chunks: rows=%#v err=%v", rows, err)
	}
}

func TestUpsertDocumentIsIdempotentAndKeepsStableIDs(t *testing.T) {
	pipeline, manager := newTestPipeline(t)
	ctx := context.Background()
	req := DocumentUpsertRequest{
		TenantID: "tenant", SourceURI: "storage://brain-sources", DocumentURI: "storage://brain-sources/rdv/RDV-123.json",
		ContentType: "application/json", Content: []byte(`{"rdv_id":"RDV-123","verbatim":"bonjour"}`),
	}

	first, err := pipeline.UpsertDocument(ctx, req)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if status, err := pipeline.WaitForJob(ctx, req.TenantID, first.JobID); err != nil || status != "completed" {
		t.Fatalf("first job: status=%s err=%v", status, err)
	}

	second, err := pipeline.UpsertDocument(ctx, req)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.Outcome != "unchanged" || second.Status != "completed" {
		t.Fatalf("expected unchanged completed result, got %#v", second)
	}
	if second.SourceID != first.SourceID || second.DocumentID != first.DocumentID {
		t.Fatalf("ids changed across identical upsert: %#v / %#v", first, second)
	}

	req.Content = []byte(`{"rdv_id":"RDV-123","verbatim":"contenu mis a jour"}`)
	third, err := pipeline.UpsertDocument(ctx, req)
	if err != nil {
		t.Fatalf("updated upsert: %v", err)
	}
	if status, err := pipeline.WaitForJob(ctx, req.TenantID, third.JobID); err != nil || status != "completed" {
		t.Fatalf("updated job: status=%s err=%v", status, err)
	}
	if third.SourceID != first.SourceID || third.DocumentID != first.DocumentID {
		t.Fatalf("ids changed across update: %#v / %#v", first, third)
	}

	db, err := manager.OpenTenant(ctx, req.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT COUNT(*) AS total, MIN(config) AS config FROM sources WHERE tenant_id = ? AND source_type = 'document' AND source_uri = ?`, req.TenantID, req.SourceURI)
	if err != nil || intValue(rows.Rows[0]["total"]) != 1 {
		t.Fatalf("expected one source: rows=%#v err=%v", rows, err)
	}
	configJSON, marshalErr := json.Marshal(rows.Rows[0]["config"])
	if marshalErr != nil || string(configJSON) != "{}" {
		t.Fatalf("document source persisted transient ingestion data: %s (err=%v)", configJSON, marshalErr)
	}
	rows, err = db.QueryContext(ctx, `SELECT COUNT(*) AS total FROM documents WHERE tenant_id = ? AND source_id = ? AND file_path = ?`, req.TenantID, first.SourceID, req.DocumentURI)
	if err != nil || intValue(rows.Rows[0]["total"]) != 1 {
		t.Fatalf("expected one document: rows=%#v err=%v", rows, err)
	}
	rows, err = db.QueryContext(ctx, `SELECT COUNT(*) AS total, COUNT(embedding) AS embedded FROM chunks WHERE tenant_id = ? AND document_id = ?`, req.TenantID, first.DocumentID)
	if err != nil || intValue(rows.Rows[0]["total"]) == 0 || intValue(rows.Rows[0]["embedded"]) != 0 {
		t.Fatalf("expected BM25 chunks without embeddings: rows=%#v err=%v", rows, err)
	}
}

func TestConcurrentDocumentsReuseOneCanonicalSource(t *testing.T) {
	pipeline, manager := newTestPipeline(t)
	ctx := context.Background()
	requests := []DocumentUpsertRequest{
		{
			TenantID: "tenant", SourceURI: "storage://brain-sources", DocumentURI: "storage://brain-sources/rdv/A.json",
			ContentType: "application/json", Content: []byte(`{"rdv_id":"A"}`),
		},
		{
			TenantID: "tenant", SourceURI: "storage://brain-sources", DocumentURI: "storage://brain-sources/rdv/B.json",
			ContentType: "application/json", Content: []byte(`{"rdv_id":"B"}`),
		},
	}

	results := make([]*DocumentUpsertResult, len(requests))
	errs := make([]error, len(requests))
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = pipeline.UpsertDocument(ctx, requests[i])
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("upsert %d: %v", i, errs[i])
		}
		if status, err := pipeline.WaitForJob(ctx, requests[i].TenantID, results[i].JobID); err != nil || status != "completed" {
			t.Fatalf("job %d: status=%s err=%v", i, status, err)
		}
	}
	if results[0].SourceID != results[1].SourceID {
		t.Fatalf("concurrent documents used different sources: %s / %s", results[0].SourceID, results[1].SourceID)
	}

	db, err := manager.OpenTenant(ctx, "tenant")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT COUNT(*) AS total FROM sources WHERE tenant_id = ? AND source_type = 'document' AND source_uri = ?`, "tenant", "storage://brain-sources")
	if err != nil || intValue(rows.Rows[0]["total"]) != 1 {
		t.Fatalf("expected one canonical source: rows=%#v err=%v", rows, err)
	}
}

func TestIndexSourceFailsEmptyUnlessExplicitlyAllowed(t *testing.T) {
	pipeline, manager := newTestPipeline(t)
	ctx := context.Background()
	emptyRoot := t.TempDir()

	configJSON, _ := json.Marshal(map[string]any{"path": filepath.Clean(emptyRoot)})
	jobID, err := pipeline.IndexSource(ctx, "tenant", "filesystem", emptyRoot, "empty", configJSON)
	if err != nil {
		t.Fatalf("index empty source: %v", err)
	}
	if status, err := pipeline.WaitForJob(ctx, "tenant", jobID); err != nil || status != "failed" {
		t.Fatalf("empty job: status=%s err=%v", status, err)
	}

	configJSON, _ = json.Marshal(map[string]any{"path": filepath.Clean(emptyRoot), "allow_empty": true})
	jobID, err = pipeline.IndexSource(ctx, "tenant", "filesystem", emptyRoot, "empty", configJSON)
	if err != nil {
		t.Fatalf("index allowed empty source: %v", err)
	}
	if status, err := pipeline.WaitForJob(ctx, "tenant", jobID); err != nil || status != "completed" {
		t.Fatalf("allowed empty job: status=%s err=%v", status, err)
	}
	db, err := manager.OpenTenant(ctx, "tenant")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT COUNT(*) AS total FROM sources WHERE tenant_id = ? AND source_type = 'filesystem' AND source_uri = ?`, "tenant", emptyRoot)
	if err != nil || intValue(rows.Rows[0]["total"]) != 1 {
		t.Fatalf("repeated filesystem index created duplicate sources: rows=%#v err=%v", rows, err)
	}
}

func TestDocumentUpdateFailureKeepsPreviouslyIndexedVersion(t *testing.T) {
	pipeline, manager := newTestPipeline(t)
	ctx := context.Background()
	req := DocumentUpsertRequest{
		TenantID: "tenant", SourceURI: "storage://brain-sources", DocumentURI: "storage://brain-sources/rdv/RDV-keep.json",
		ContentType: "application/json", Content: []byte(`{"version":1,"text":"stable"}`),
	}
	first, err := pipeline.UpsertDocument(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := pipeline.WaitForJob(ctx, req.TenantID, first.JobID); err != nil || status != "completed" {
		t.Fatalf("initial job: status=%s err=%v", status, err)
	}

	pipeline.embedder = failingEmbedder{}
	req.Content = []byte(`{"version":2,"text":"must not replace stable"}`)
	updated, err := pipeline.UpsertDocument(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := pipeline.WaitForJob(ctx, req.TenantID, updated.JobID); err != nil || status != "failed" {
		t.Fatalf("failed update job: status=%s err=%v", status, err)
	}

	db, err := manager.OpenTenant(ctx, req.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT content_hash FROM documents WHERE tenant_id = ? AND id = ?`, req.TenantID, first.DocumentID)
	if err != nil || len(rows.Rows) != 1 {
		t.Fatalf("read stable document: rows=%#v err=%v", rows, err)
	}
	wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte(`{"version":1,"text":"stable"}`)))
	if got := fmt.Sprintf("%v", rows.Rows[0]["content_hash"]); got != wantHash {
		t.Fatalf("failed update replaced stable hash: got=%s want=%s", got, wantHash)
	}
}

func TestIndexSourceMarksFileProcessingErrorsAsFailed(t *testing.T) {
	pipeline, _ := newTestPipeline(t)
	pipeline.connectors["failing"] = failingConnector{}
	jobID, err := pipeline.IndexSource(context.Background(), "tenant", "failing", "logical-source", "failing", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if status, err := pipeline.WaitForJob(context.Background(), "tenant", jobID); err != nil || status != "failed" {
		t.Fatalf("processing error job: status=%s err=%v", status, err)
	}
}

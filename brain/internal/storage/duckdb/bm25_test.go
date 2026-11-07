//go:build duckdb

package duckdb

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	stypes "github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/types"
)

func newFTSTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(config.StorageConfig{
		BaseDir:        t.TempDir(),
		TenantsDirName: "tenants",
		DBFilename:     "brain.duckdb",
		WriteQueueSize: 16,
	}, zap.NewNop())
	t.Cleanup(func() { _ = m.Close() })
	return m
}

var testFTSOptions = stypes.FullTextOptions{Stemmer: "porter", Stopwords: "none"}

// writeChunk inserts one chunk. When mark is true the write follows the
// production contract: the stale flag is set inside the tenant writer.
func writeChunk(t *testing.T, m *Manager, tenant, id, content string, mark bool) {
	t.Helper()
	err := m.WithWriter(context.Background(), tenant, func(ctx context.Context, q stypes.Querier) error {
		if mark {
			m.MarkFullTextStale(tenant)
		}
		_, err := q.ExecContext(ctx,
			`INSERT INTO chunks (id, tenant_id, document_id, chunk_index, content, chunk_type, bm25_content, created_at)
			 VALUES (?, ?, 'doc', 0, ?, 'paragraph', ?, ?)`,
			id, tenant, content, content, time.Now())
		return err
	})
	if err != nil {
		t.Fatalf("write chunk %s: %v", id, err)
	}
}

// matchIDs runs the same ranked query shape as storage/search.SearchBM25 and
// returns the matching chunk ids, best first.
func matchIDs(t *testing.T, m *Manager, tenant, query string) []string {
	t.Helper()
	q, err := m.OpenTenant(context.Background(), tenant)
	if err != nil {
		t.Fatalf("open tenant: %v", err)
	}
	qr, err := q.QueryContext(context.Background(), `
		SELECT c.id FROM chunks c
		JOIN (SELECT id, fts_main_chunks.match_bm25(id, ?) AS score FROM chunks WHERE tenant_id = ?) fts ON c.id = fts.id
		WHERE fts.score IS NOT NULL ORDER BY fts.score DESC`, query, tenant)
	if err != nil {
		t.Fatalf("match_bm25 %q: %v", query, err)
	}
	ids := make([]string, 0, len(qr.Rows))
	for _, row := range qr.Rows {
		ids = append(ids, toStringForTest(row["id"]))
	}
	return ids
}

func toStringForTest(v any) string {
	s, _ := v.(string)
	return s
}

func TestFullTextIndexIsBuiltLazilyAndOnlyWhenStale(t *testing.T) {
	m := newFTSTestManager(t)
	ctx := context.Background()
	const tenant = "tenant-a"

	writeChunk(t, m, tenant, "c1", "Le ticket AGENT-530 décrit la tarification.", true)
	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if got := m.FullTextRebuilds(tenant); got != 1 {
		t.Fatalf("first ensure should rebuild once, got %d", got)
	}

	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if got := m.FullTextRebuilds(tenant); got != 1 {
		t.Fatalf("current index must not be rebuilt, rebuilds=%d", got)
	}

	writeChunk(t, m, tenant, "c2", "Nouveau paragraphe sur la facturation.", true)
	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("ensure after write: %v", err)
	}
	if got := m.FullTextRebuilds(tenant); got != 2 {
		t.Fatalf("write must trigger exactly one rebuild, rebuilds=%d", got)
	}
}

func TestMarkedWriteIsIndexedBeforeNextSearchAndUnmarkedWriteIsNot(t *testing.T) {
	m := newFTSTestManager(t)
	ctx := context.Background()
	const tenant = "tenant-b"

	writeChunk(t, m, tenant, "c1", "Paragraphe initial sur la sécurité.", true)
	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// A write that does not mark the index stays invisible until the next mark:
	// the flag is what makes the rebuild happen, so writers must set it.
	writeChunk(t, m, tenant, "c2", "Terme sentinelleunmarque présent ici.", false)
	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("ensure after unmarked write: %v", err)
	}
	if ids := matchIDs(t, m, tenant, "sentinelleunmarque"); len(ids) != 0 {
		t.Fatalf("unmarked write should not be rebuilt into the index, got %v", ids)
	}

	writeChunk(t, m, tenant, "c3", "Terme sentinellemarque présent ici.", true)
	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("ensure after marked write: %v", err)
	}
	if ids := matchIDs(t, m, tenant, "sentinellemarque"); len(ids) != 1 || ids[0] != "c3" {
		t.Fatalf("marked write must be indexed before the next search, got %v", ids)
	}
	if ids := matchIDs(t, m, tenant, "sentinelleunmarque"); len(ids) != 1 || ids[0] != "c2" {
		t.Fatalf("the rebuild also covers earlier unmarked writes, got %v", ids)
	}
}

func TestConcurrentCallersCoalesceIntoOneRebuild(t *testing.T) {
	m := newFTSTestManager(t)
	ctx := context.Background()
	const tenant = "tenant-c"

	writeChunk(t, m, tenant, "c1", "Contenu pour la reconstruction concurrente.", true)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- m.EnsureFullTextIndex(ctx, tenant, testFTSOptions)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensure: %v", err)
		}
	}
	if got := m.FullTextRebuilds(tenant); got != 1 {
		t.Fatalf("eight callers on one stale index must rebuild once, got %d", got)
	}
}

func TestFailedRebuildStaysStaleAndRetries(t *testing.T) {
	m := newFTSTestManager(t)
	ctx := context.Background()
	const tenant = "tenant-d"

	writeChunk(t, m, tenant, "c1", "Contenu avec stopwords.", true)
	missing := stypes.FullTextOptions{Stemmer: "porter", Stopwords: "table_absente_xyz"}
	if err := m.EnsureFullTextIndex(ctx, tenant, missing); err == nil {
		t.Fatal("expected an error for a stopword table that does not exist")
	}
	if got := m.FullTextRebuilds(tenant); got != 0 {
		t.Fatalf("a failed rebuild must not count as a rebuild, got %d", got)
	}
	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("retry with valid options should succeed: %v", err)
	}
	if got := m.FullTextRebuilds(tenant); got != 1 {
		t.Fatalf("retry should rebuild once, got %d", got)
	}
}

func TestUnsafeFullTextOptionsAreRejectedBeforeSQL(t *testing.T) {
	m := newFTSTestManager(t)
	ctx := context.Background()
	for _, opts := range []stypes.FullTextOptions{
		{Stemmer: "porter'); DROP TABLE chunks; --", Stopwords: "none"},
		{Stemmer: "porter", Stopwords: "none'; --"},
		{Stemmer: "klingon", Stopwords: "none"},
	} {
		if err := m.EnsureFullTextIndex(ctx, "tenant-e", opts); err == nil {
			t.Fatalf("expected rejection for %+v", opts)
		}
	}
}

func TestEachTenantRebuildsItsOwnIndex(t *testing.T) {
	m := newFTSTestManager(t)
	ctx := context.Background()

	writeChunk(t, m, "tenant-one", "a1", "Premier tenant avec le terme alphaunique.", true)
	writeChunk(t, m, "tenant-two", "b1", "Second tenant avec le terme betaunique.", true)
	for _, tenant := range []string{"tenant-one", "tenant-two"} {
		if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
			t.Fatalf("rebuild for %s after another tenant: %v", tenant, err)
		}
		if got := m.FullTextRebuilds(tenant); got != 1 {
			t.Fatalf("%s rebuilds = %d, want 1", tenant, got)
		}
	}
	if ids := matchIDs(t, m, "tenant-two", "betaunique"); len(ids) != 1 || ids[0] != "b1" {
		t.Fatalf("tenant-two index missing its chunk: %v", ids)
	}
	if ids := matchIDs(t, m, "tenant-one", "betaunique"); len(ids) != 0 {
		t.Fatalf("tenant-one index must not see tenant-two chunks: %v", ids)
	}
}

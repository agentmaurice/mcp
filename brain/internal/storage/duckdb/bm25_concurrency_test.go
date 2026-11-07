//go:build duckdb

package duckdb

import (
	"context"
	"sync"
	"testing"
	"time"

	stypes "github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/types"
)

// queryMatchIDs is matchIDs for goroutines: it reports errors instead of failing the test.
func queryMatchIDs(m *Manager, tenant, query string) ([]string, error) {
	q, err := m.OpenTenant(context.Background(), tenant)
	if err != nil {
		return nil, err
	}
	qr, err := q.QueryContext(context.Background(), `
		SELECT c.id FROM chunks c
		JOIN (SELECT id, fts_main_chunks.match_bm25(id, ?) AS score FROM chunks WHERE tenant_id = ?) fts ON c.id = fts.id
		WHERE fts.score IS NOT NULL ORDER BY fts.score DESC`, query, tenant)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(qr.Rows))
	for _, row := range qr.Rows {
		ids = append(ids, toStringForTest(row["id"]))
	}
	return ids, nil
}

// TestSearchWaitsForRebuildInFlight holds a rebuild open after its claim. A second
// search must neither answer from the old index nor report it as current: it waits
// for the rebuild, then finds the document written before it started.
func TestSearchWaitsForRebuildInFlight(t *testing.T) {
	m := newFTSTestManager(t)
	ctx := context.Background()
	const tenant = "tenant-gate"

	writeChunk(t, m, tenant, "c1", "Premier document de référence.", true)
	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("initial build: %v", err)
	}
	writeChunk(t, m, tenant, "c2", "Ce texte contient sentinelleunique.", true)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	testHookBeforeFullTextBuild = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	defer func() { testHookBeforeFullTextBuild = nil }()

	first := make(chan error, 1)
	go func() { first <- m.EnsureFullTextIndex(ctx, tenant, testFTSOptions) }()
	<-entered // the rebuild is claimed and held open

	type answer struct {
		ids []string
		err error
	}
	second := make(chan answer, 1)
	go func() {
		if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
			second <- answer{err: err}
			return
		}
		ids, err := queryMatchIDs(m, tenant, "sentinelleunique")
		second <- answer{ids: ids, err: err}
	}()

	select {
	case got := <-second:
		t.Fatalf("second search returned while the rebuild was in flight: ids=%v err=%v", got.ids, got.err)
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	got := <-second
	if got.err != nil {
		t.Fatalf("second search: %v", got.err)
	}
	if len(got.ids) != 1 || got.ids[0] != "c2" {
		t.Fatalf("second search after the rebuild = %v, want [c2] (sentinelleunique)", got.ids)
	}
}

// TestFailedInFlightRebuildLeavesIndexStale checks that a waiter does not take a
// failed rebuild for a current index: the next search rebuilds again.
func TestFailedInFlightRebuildLeavesIndexStale(t *testing.T) {
	m := newFTSTestManager(t)
	ctx := context.Background()
	const tenant = "tenant-fail-gate"
	writeChunk(t, m, tenant, "c1", "Contenu.", true)
	bad := stypes.FullTextOptions{Stemmer: "porter", Stopwords: "table_absente_gate"}
	if err := m.EnsureFullTextIndex(ctx, tenant, bad); err == nil {
		t.Fatal("expected the rebuild with a missing stopword table to fail")
	}
	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("valid rebuild after a failed one: %v", err)
	}
	if got := m.FullTextRebuilds(tenant); got != 1 {
		t.Fatalf("rebuilds = %d, want 1 (the failed attempt does not count)", got)
	}
}

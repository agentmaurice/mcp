//go:build duckdb

package search

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
)

// benchQueries mix BM25 queries and identifier queries (the exact-identifier path
// runs list_transform and list_filter over every chunk).
var benchQueries = []string{
	"procédure de révision annuelle",
	"réseau Besançon",
	"sécurité données chiffrement",
	"tarification mensuelle",
	"AGENT-530",
	"MEM-G3",
	"2026.10.19-3015",
	"AGENT-530 AGENT-531 calcul_taux_marge",
}

// BenchmarkKeywordSearch measures brain.keyword on the French corpus copied
// BRAIN_BENCH_COPIES times (default 50), so the same data can be replayed on two
// DuckDB versions. The digest of the ranked results is logged: equal digests mean
// equal results.
//
//	BRAIN_BENCH_COPIES=100 go test -tags duckdb -run '^$' -bench KeywordSearch ./internal/search
func BenchmarkKeywordSearch(b *testing.B) {
	copies := 50
	if v := os.Getenv("BRAIN_BENCH_COPIES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			b.Fatalf("BRAIN_BENCH_COPIES = %q, want a positive integer", v)
		}
		copies = n
	}
	env := newSearchEnv(b)
	entries, err := os.ReadDir(frenchCorpusDir)
	if err != nil {
		b.Fatalf("read corpus: %v", err)
	}
	const tenant = "tenant-bench"
	for i := 0; i < copies; i++ {
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(frenchCorpusDir, entry.Name()))
			if err != nil {
				b.Fatalf("read fixture %s: %v", entry.Name(), err)
			}
			env.upsert(b, tenant, fmt.Sprintf("copy%03d-%s", i, entry.Name()), string(data))
		}
	}
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())
	ctx := context.Background()

	digest := sha256.New()
	for _, q := range benchQueries { // warm-up: builds the full-text index once
		resp, err := o.Search(ctx, tenant, q, shared.SearchOpts{Mode: "bm25", MaxResults: 20})
		if err != nil {
			b.Fatalf("search %q: %v", q, err)
		}
		fmt.Fprintf(digest, "%s|%d|", q, len(resp.Results))
		for _, r := range resp.Results {
			// Ids are generated per run; path, chunk position and content, match
			// type and score identify a result.
			content := sha256.Sum256([]byte(r.Chunk.Content))
			fmt.Fprintf(digest, "%s#%d#%x#%s#%.6f;", r.Document.FilePath, r.Chunk.ChunkIndex, content[:6], r.MatchType, r.Score)
		}
	}
	b.Logf("documents=%d result-digest=%x", copies*len(entries), digest.Sum(nil)[:8])

	// Mean latency is dominated by host load; the median and the minimum of the
	// individual searches are what two runs can be compared on.
	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := benchQueries[i%len(benchQueries)]
		start := time.Now()
		if _, err := o.Search(ctx, tenant, q, shared.SearchOpts{Mode: "bm25", MaxResults: 20}); err != nil {
			b.Fatalf("search %q: %v", q, err)
		}
		latencies = append(latencies, time.Since(start))
	}
	b.StopTimer()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	b.ReportMetric(float64(latencies[0].Microseconds())/1000, "min-ms")
	b.ReportMetric(float64(latencies[len(latencies)/2].Microseconds())/1000, "p50-ms")
}

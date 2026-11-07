//go:build duckdb

package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	storagesearch "github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/search"
)

// frenchCorpusDir holds the French fixture documents with identifiers.
const frenchCorpusDir = "testdata/fr"

type searchEnv struct {
	manager  storage.Manager
	pipeline *indexer.Pipeline
	cfg      *config.Config
}

func newSearchEnv(t testing.TB) searchEnv {
	t.Helper()
	return newSearchEnvAt(t, t.TempDir())
}

// newSearchEnvAt is newSearchEnv with the tenant files under baseDir.
func newSearchEnvAt(t testing.TB, baseDir string) searchEnv {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			Backend: "duckdb", BaseDir: baseDir, TenantsDirName: "tenants", DBFilename: "brain.duckdb", WriteQueueSize: 16,
		},
		Indexing: config.IndexingConfig{Workers: 2, ChunkMaxTokens: 500, MaxDocumentBytes: 10 * 1024 * 1024},
		Search: config.SearchConfig{
			DefaultMaxResults: 20, MaxResultsLimit: 100, HybridDefaultAlpha: 0.6,
			FTSStemmer: "porter", FTSStopwords: "none",
		},
	}
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("create storage manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	pipeline := indexer.NewPipeline(manager, nil, nil, cfg, zap.NewNop())
	return searchEnv{manager: manager, pipeline: pipeline, cfg: cfg}
}

// upsert indexes one document and waits for its job, so the next search sees it.
func (env searchEnv) upsert(t testing.TB, tenant, uri, content string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := env.pipeline.UpsertDocument(ctx, indexer.DocumentUpsertRequest{
		TenantID: tenant, SourceURI: "fixtures", DocumentURI: uri, ContentType: "text/markdown", Content: []byte(content),
	})
	if err != nil {
		t.Fatalf("upsert %s: %v", uri, err)
	}
	status, err := env.pipeline.WaitForJob(ctx, tenant, res.JobID)
	if err != nil {
		t.Fatalf("wait job %s: %v", uri, err)
	}
	if status != "completed" {
		t.Fatalf("job for %s ended %q, want completed", uri, status)
	}
}

func (env searchEnv) ingestFrenchCorpus(t testing.TB, tenant string) {
	t.Helper()
	entries, err := os.ReadDir(frenchCorpusDir)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(frenchCorpusDir, entry.Name()))
		if err != nil {
			t.Fatalf("read fixture %s: %v", entry.Name(), err)
		}
		env.upsert(t, tenant, entry.Name(), string(data))
	}
}

func (env searchEnv) keyword(t testing.TB, o *Orchestrator, tenant, query string, maxResults int) *shared.SearchResponse {
	t.Helper()
	resp, err := o.Search(context.Background(), tenant, query, shared.SearchOpts{Mode: "bm25", MaxResults: maxResults})
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return resp
}

func topPath(resp *shared.SearchResponse) string {
	if len(resp.Results) == 0 {
		return ""
	}
	return resp.Results[0].Document.FilePath
}

// frenchCorpusCases are queries over the French corpus and the document each must rank first.
var frenchCorpusCases = []struct {
	query string
	want  string
}{
	{"procédure de révision annuelle", "procedure-revision.md"},
	{"réseau Besançon", "reseau-instable.md"},
	{"sécurité données chiffrement", "securite-donnees.md"},
	{"farine lait crêpes", "recettes-cuisine.md"},
	{"tarification mensuelle", "ticket-agent-530.md"},
	{"calcul_taux_marge", "finance-taux-marge.md"},
	{"MEM-G3", "release-2026.md"},
	{"2026.10.19-3015", "release-2026.md"},
	{"AGENT-530", "ticket-agent-530.md"},
}

// checkFrenchCorpusCases runs every case of frenchCorpusCases against tenant.
func checkFrenchCorpusCases(t *testing.T, env searchEnv, o *Orchestrator, tenant string) {
	t.Helper()
	for _, tc := range frenchCorpusCases {
		resp := env.keyword(t, o, tenant, tc.query, 5)
		if got := topPath(resp); got != tc.want {
			t.Errorf("query %q: top = %q, want %q (results=%d)", tc.query, got, tc.want, len(resp.Results))
		}
		if resp.SearchMetadata.BM25ModeEffective != "fts" {
			t.Errorf("query %q: bm25_mode_effective = %q, want fts", tc.query, resp.SearchMetadata.BM25ModeEffective)
		}
	}
}

func TestKeywordRanksFrenchCorpusAndIdentifiers(t *testing.T) {
	env := newSearchEnv(t)
	env.ingestFrenchCorpus(t, "tenant-fr")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())
	checkFrenchCorpusCases(t, env, o, "tenant-fr")
}

func TestIdentifierHitsAreExactAndComeFirst(t *testing.T) {
	env := newSearchEnv(t)
	env.ingestFrenchCorpus(t, "tenant-id")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())

	resp := env.keyword(t, o, "tenant-id", "AGENT-530", 10)
	if len(resp.Results) == 0 {
		t.Fatal("expected results for AGENT-530")
	}
	first := resp.Results[0]
	if first.MatchType != "exact_identifier" || first.Document.FilePath != "ticket-agent-530.md" {
		t.Fatalf("first hit = %s/%s, want exact_identifier on ticket-agent-530.md", first.MatchType, first.Document.FilePath)
	}
	if resp.SearchMetadata.IdentifierMatches < 1 {
		t.Fatalf("identifier_matches = %d, want >= 1", resp.SearchMetadata.IdentifierMatches)
	}
	for _, r := range resp.Results {
		if r.MatchType != "exact_identifier" {
			continue
		}
		if strings.Contains(r.Document.FilePath, "531") || !strings.Contains(r.Chunk.Content, "AGENT-530") {
			t.Fatalf("exact hit %s does not contain the identifier AGENT-530 as a whole token", r.Document.FilePath)
		}
	}
	for i, r := range resp.Results {
		if r.MatchType == "exact_identifier" && i > 0 && resp.Results[i-1].MatchType == "bm25" {
			t.Fatalf("exact hit at rank %d follows a ranked hit", i+1)
		}
	}
}

func TestIndexIsRebuiltBeforeTheNextSearch(t *testing.T) {
	env := newSearchEnv(t)
	env.ingestFrenchCorpus(t, "tenant-live")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())

	if resp := env.keyword(t, o, "tenant-live", "nouveautermeunique", 5); len(resp.Results) != 0 {
		t.Fatalf("term absent from corpus returned %d results", len(resp.Results))
	}

	env.upsert(t, "tenant-live", "nouveau.md", "# Nouveau\n\nLe terme nouveautermeunique apparaît dans ce document.\n")
	resp := env.keyword(t, o, "tenant-live", "nouveautermeunique", 5)
	if got := topPath(resp); got != "nouveau.md" {
		t.Fatalf("document written before the search is not found: top=%q", got)
	}

	// Replacing a document must also reach the index: the old wording goes away.
	env.upsert(t, "tenant-live", "procedure-revision.md", "# Procédure mise à jour\n\nLe bilan semestriel remplace le suivi trimestriel.\n")
	if got := topPath(env.keyword(t, o, "tenant-live", "bilan semestriel", 5)); got != "procedure-revision.md" {
		t.Fatalf("replaced document not found by new wording: top=%q", got)
	}
	for _, r := range env.keyword(t, o, "tenant-live", "révision annuelle", 20).Results {
		if r.Document.FilePath == "procedure-revision.md" {
			t.Fatal("replaced document still matches its previous wording")
		}
	}
}

// indexlessManager hides the FullTextIndexer capability, as duckhttp does.
type indexlessManager struct{ storage.Manager }

// brokenIndexManager keeps the capability but reports a failing index.
type brokenIndexManager struct{ storage.Manager }

func (brokenIndexManager) MarkFullTextStale(string) {}
func (brokenIndexManager) EnsureFullTextIndex(context.Context, string, storage.FullTextOptions) error {
	return errors.New("simulated index failure")
}

func TestFallbackIsExplicitWhenIndexIsUnavailable(t *testing.T) {
	env := newSearchEnv(t)
	env.ingestFrenchCorpus(t, "tenant-fb")

	for name, manager := range map[string]storage.Manager{
		"no-capability": indexlessManager{env.manager},
		"index-error":   brokenIndexManager{env.manager},
	} {
		o := NewOrchestrator(manager, nil, env.cfg, zap.NewNop())
		resp := env.keyword(t, o, "tenant-fb", "Besançon", 5)
		meta := resp.SearchMetadata
		if meta.BM25ModeEffective != "substring_fallback" {
			t.Fatalf("%s: bm25_mode_effective = %q, want substring_fallback", name, meta.BM25ModeEffective)
		}
		if len(meta.Warnings) == 0 || !strings.Contains(meta.Warnings[0], "not BM25-ranked") {
			t.Fatalf("%s: fallback not reported in warnings: %v", name, meta.Warnings)
		}
		if got := topPath(resp); got != "reseau-instable.md" {
			t.Fatalf("%s: substring fallback top = %q, want reseau-instable.md", name, got)
		}
		if resp.Results[0].MatchType != "substring" {
			t.Fatalf("%s: match_type = %q, want substring", name, resp.Results[0].MatchType)
		}
	}
}

func TestBrokenIndexReasonIsReported(t *testing.T) {
	env := newSearchEnv(t)
	env.ingestFrenchCorpus(t, "tenant-why")
	o := NewOrchestrator(brokenIndexManager{env.manager}, nil, env.cfg, zap.NewNop())
	resp := env.keyword(t, o, "tenant-why", "Besançon", 5)
	if len(resp.SearchMetadata.Warnings) == 0 || !strings.Contains(resp.SearchMetadata.Warnings[0], "simulated index failure") {
		t.Fatalf("index error reason missing from warnings: %v", resp.SearchMetadata.Warnings)
	}
}

func TestTenantWithoutDocumentsWarnsWithoutLeakingOtherTenants(t *testing.T) {
	env := newSearchEnv(t)
	env.ingestFrenchCorpus(t, "tenant-plein")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())

	empty := env.keyword(t, o, "tenant-vide", "tarification mensuelle", 5)
	if len(empty.Results) != 0 {
		t.Fatalf("empty tenant returned %d results", len(empty.Results))
	}
	if !empty.SearchMetadata.TenantEmpty {
		t.Fatal("tenant_empty must be true for a tenant without documents")
	}
	if len(empty.SearchMetadata.Warnings) == 0 || !strings.Contains(empty.SearchMetadata.Warnings[0], "no indexed documents") {
		t.Fatalf("empty tenant warning missing: %v", empty.SearchMetadata.Warnings)
	}
	for _, w := range empty.SearchMetadata.Warnings {
		if strings.Contains(w, "tenant-plein") {
			t.Fatalf("warning leaks another tenant: %q", w)
		}
	}

	populated := env.keyword(t, o, "tenant-plein", "zzzabsentdescorpus", 5)
	if populated.SearchMetadata.TenantEmpty || len(populated.SearchMetadata.Warnings) != 0 {
		t.Fatalf("populated tenant without match must not warn: empty=%v warnings=%v",
			populated.SearchMetadata.TenantEmpty, populated.SearchMetadata.Warnings)
	}
}

func TestFiltersRestrictKeywordHits(t *testing.T) {
	env := newSearchEnv(t)
	env.ingestFrenchCorpus(t, "tenant-filter")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())

	resp, err := o.Search(context.Background(), "tenant-filter", "contrats", shared.SearchOpts{
		Mode: "bm25", MaxResults: 10, DocTypes: []string{"no-such-type"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("doc_types filter ignored: %d results", len(resp.Results))
	}
}

// Review case: a query with six identifiers where only the sixth is in the corpus.
// The sixth must still be searched verbatim and reported as an exact hit.
func TestSixthIdentifierIsSearchedVerbatim(t *testing.T) {
	env := newSearchEnv(t)
	env.upsert(t, "tenant-six", "dossier.md", "# Dossier\n\nLe dossier AGENT-106 est clos.\n")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())

	query := "AGENT-101 AGENT-102 AGENT-103 AGENT-104 AGENT-105 AGENT-106"
	resp, err := o.Search(context.Background(), "tenant-six", query, shared.SearchOpts{Mode: "bm25", MaxResults: 20})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := false
	for _, r := range resp.Results {
		if r.MatchType == "exact_identifier" && r.Document.FilePath == "dossier.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("AGENT-106 not matched verbatim: results=%d identifier_matches=%d", len(resp.Results), resp.SearchMetadata.IdentifierMatches)
	}
	if len(resp.SearchMetadata.Warnings) != 0 {
		t.Fatalf("six identifiers are within the limit, no warning expected: %v", resp.SearchMetadata.Warnings)
	}
}

// Identifiers beyond the verbatim limit are reported in warnings, not dropped silently.
func TestIdentifiersBeyondLimitAreReported(t *testing.T) {
	env := newSearchEnv(t)
	env.upsert(t, "tenant-many", "dossier.md", "# Dossier\n\nRéférence AGENT-022 seule.\n")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())

	var ids []string
	for i := 1; i <= storagesearch.MaxExactIdentifiers+2; i++ {
		ids = append(ids, fmt.Sprintf("AGENT-%03d", i))
	}
	resp, err := o.Search(context.Background(), "tenant-many", strings.Join(ids, " "), shared.SearchOpts{Mode: "bm25", MaxResults: 20})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := false
	for _, w := range resp.SearchMetadata.Warnings {
		if strings.Contains(w, fmt.Sprintf("2 identifier(s) beyond the first %d were not matched verbatim: AGENT-%03d, AGENT-%03d",
			storagesearch.MaxExactIdentifiers, storagesearch.MaxExactIdentifiers+1, storagesearch.MaxExactIdentifiers+2)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning for the 2 identifiers beyond the limit, got %v", resp.SearchMetadata.Warnings)
	}
}

// Chunks are ordered by how many identifiers they repeat, so a chunk that cites the
// identifier many times outranks one that names it once.
func TestExactHitsOrderByOccurrences(t *testing.T) {
	env := newSearchEnv(t)
	long := "# A\n\n" + strings.Repeat("contexte général sans rapport. ", 60) + "\nLa fiche AGENT-530 est citée une fois.\n"
	env.upsert(t, "tenant-ord", "a.md", long)
	env.upsert(t, "tenant-ord", "z.md", "# Z\n\nAGENT-530 puis AGENT-530 et encore AGENT-530.\n")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())
	resp, err := o.Search(context.Background(), "tenant-ord", "AGENT-530", shared.SearchOpts{Mode: "bm25", MaxResults: 2})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) < 2 || resp.Results[0].Document.FilePath != "z.md" {
		t.Fatalf("chunk repeating the identifier must come first: %+v", resp.Results)
	}
}

// Review case: a token that continues an identifier is not that identifier.
func TestIdentifierDoesNotMatchContinuedToken(t *testing.T) {
	env := newSearchEnv(t)
	env.upsert(t, "tenant-token", "suite.md", "# Suite\n\nRéférence AGENT-530--1 seule.\n")
	env.upsert(t, "tenant-token", "version.md", "# Version\n\nVersion 2026.10.19-3015.2 publiée.\n")
	env.upsert(t, "tenant-token", "phrase.md", "# Phrase\n\nLa fiche AGENT-530. est close.\n")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())
	resp, err := o.Search(context.Background(), "tenant-token", "AGENT-530 2026.10.19-3015", shared.SearchOpts{Mode: "bm25", MaxResults: 20})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	exactDocs := map[string]bool{}
	for _, r := range resp.Results {
		if r.MatchType == "exact_identifier" {
			exactDocs[r.Document.FilePath] = true
		}
	}
	if exactDocs["suite.md"] || exactDocs["version.md"] {
		t.Fatalf("continued tokens matched verbatim: %v", exactDocs)
	}
	if !exactDocs["phrase.md"] {
		t.Fatalf("identifier at sentence end must match verbatim: %v", exactDocs)
	}
}

// Review case: the 201st identifier of a long query is searched verbatim.
func TestLastIdentifierOfLongQueryIsSearchedVerbatim(t *testing.T) {
	env := newSearchEnv(t)
	env.upsert(t, "tenant-long", "dernier.md", "# Dernier\n\nRéférence AGENT-201 seule.\n")
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())
	var ids []string
	for i := 1; i <= 201; i++ {
		ids = append(ids, fmt.Sprintf("AGENT-%03d", i))
	}
	resp, err := o.Search(context.Background(), "tenant-long", strings.Join(ids, " "), shared.SearchOpts{Mode: "bm25", MaxResults: 20})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := false
	for _, r := range resp.Results {
		if r.MatchType == "exact_identifier" && r.Document.FilePath == "dernier.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("AGENT-201 not matched verbatim among 201 identifiers: %d results", len(resp.Results))
	}
}

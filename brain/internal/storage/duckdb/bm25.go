//go:build duckdb

package duckdb

import (
	"context"
	"fmt"
	"sync"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	stypes "github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/types"
)

// ftsIgnorePattern keeps digits and underscores inside tokens, so identifiers
// such as AGENT-530, MEM-G3 or calcul_taux_marge are not split apart. DuckDB
// defaults ([^a-z]) would erase the digits.
const ftsIgnorePattern = `(\\.|[^a-z0-9_])+`

// ftsTenantState tracks whether the BM25 index of one tenant reflects its chunks.
type ftsTenantState struct {
	// stale is set by any writer that touches chunks or documents, and cleared
	// when a rebuild is claimed. A fresh process starts stale because the index may
	// predate writes that were not rebuilt before a restart.
	stale   bool
	built   bool
	options stypes.FullTextOptions
	// rebuilds counts successful rebuilds in this process (observability and tests).
	rebuilds int
	// inflight is open while a claimed rebuild runs. Until it closes, the index is
	// not current, whatever stale says: searches wait on it.
	inflight chan struct{}
}

// ftsTracker holds the per-tenant index state. A single mutex guards it; the
// rebuild itself runs through the tenant writer, which serializes it with writes.
type ftsTracker struct {
	mu      sync.Mutex
	tenants map[string]*ftsTenantState
}

func (f *ftsTracker) stateLocked(tenantID string) *ftsTenantState {
	if f.tenants == nil {
		f.tenants = make(map[string]*ftsTenantState)
	}
	st, ok := f.tenants[tenantID]
	if !ok {
		st = &ftsTenantState{stale: true}
		f.tenants[tenantID] = st
	}
	return st
}

// needsRebuildLocked reports whether the index of st is not current for opts.
func (st *ftsTenantState) needsRebuildLocked(opts stypes.FullTextOptions) bool {
	return st.stale || !st.built || st.options != opts
}

// testHookBeforeFullTextBuild runs inside a claimed rebuild, after the claim and
// before the PRAGMA. Tests set it to hold a rebuild open. It is nil in production.
var testHookBeforeFullTextBuild func()

// MarkFullTextStale implements stypes.FullTextIndexer.
func (m *Manager) MarkFullTextStale(tenantID string) {
	m.fts.mu.Lock()
	defer m.fts.mu.Unlock()
	m.fts.stateLocked(tenantID).stale = true
}

// EnsureFullTextIndex implements stypes.FullTextIndexer. When the index is current
// it returns at once. While a rebuild is running, the caller waits for it to end
// (or for ctx). Otherwise the caller claims a rebuild through the tenant writer,
// so concurrent callers queue behind one another, and the one that follows finds
// the index current.
func (m *Manager) EnsureFullTextIndex(ctx context.Context, tenantID string, opts stypes.FullTextOptions) error {
	if err := config.ValidateFullTextSettings(opts.Stemmer, opts.Stopwords); err != nil {
		return err
	}
	for {
		wait, needs := m.fullTextStatus(tenantID, opts)
		if wait == nil {
			if !needs {
				return nil
			}
			break
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.WithWriter(ctx, tenantID, func(ctx context.Context, q stypes.Querier) error {
		if !m.claimFullTextRebuild(tenantID, opts) {
			return nil
		}
		if testHookBeforeFullTextBuild != nil {
			testHookBeforeFullTextBuild()
		}
		err := loadFTSExtension(ctx, q)
		if err == nil {
			err = rebuildFullTextIndex(ctx, q, opts)
		}
		m.finishFullTextRebuild(tenantID, opts, err)
		return err
	})
}

// fullTextStatus returns the channel of a rebuild in progress (nil if none), and
// whether the index needs a rebuild when none is in progress.
func (m *Manager) fullTextStatus(tenantID string, opts stypes.FullTextOptions) (<-chan struct{}, bool) {
	m.fts.mu.Lock()
	defer m.fts.mu.Unlock()
	st := m.fts.stateLocked(tenantID)
	if st.inflight != nil {
		return st.inflight, false
	}
	return nil, st.needsRebuildLocked(opts)
}

// claimFullTextRebuild re-checks under the writer. On success it clears the stale
// flag and opens the in-flight gate: from here until finishFullTextRebuild, the
// index counts as not current. A write that lands later sets stale again.
func (m *Manager) claimFullTextRebuild(tenantID string, opts stypes.FullTextOptions) bool {
	m.fts.mu.Lock()
	defer m.fts.mu.Unlock()
	st := m.fts.stateLocked(tenantID)
	if st.inflight != nil || !st.needsRebuildLocked(opts) {
		return false
	}
	st.stale = false
	st.inflight = make(chan struct{})
	return true
}

// finishFullTextRebuild records the outcome of a claimed rebuild and releases the
// searches waiting on it. On failure the index stays stale, so the next search retries.
func (m *Manager) finishFullTextRebuild(tenantID string, opts stypes.FullTextOptions, err error) {
	m.fts.mu.Lock()
	defer m.fts.mu.Unlock()
	st := m.fts.stateLocked(tenantID)
	gate := st.inflight
	st.inflight = nil
	if err != nil {
		st.stale = true
		st.built = false
	} else {
		st.built = true
		st.options = opts
		st.rebuilds++
	}
	if gate != nil {
		close(gate)
	}
}

// FullTextRebuilds returns how many times the BM25 index of tenantID was rebuilt
// successfully in this process.
func (m *Manager) FullTextRebuilds(tenantID string) int {
	m.fts.mu.Lock()
	defer m.fts.mu.Unlock()
	return m.fts.stateLocked(tenantID).rebuilds
}

// loadFTSExtension loads the fts extension on the tenant database that is about
// to be rebuilt. Each tenant is a separate DuckDB instance, so the load cannot be
// shared across tenants; LOAD is cheap when the extension is already loaded. It
// returns the error, if any, so the rebuild can report why BM25 is unavailable.
func loadFTSExtension(ctx context.Context, q stypes.Querier) error {
	if _, err := q.ExecContext(ctx, "LOAD fts"); err == nil {
		return nil
	}
	if _, err := q.ExecContext(ctx, "INSTALL fts"); err != nil {
		return fmt.Errorf("fts extension unavailable: %w", err)
	}
	if _, err := q.ExecContext(ctx, "LOAD fts"); err != nil {
		return fmt.Errorf("fts extension unavailable: %w", err)
	}
	return nil
}

// rebuildFullTextIndex (re)creates the FTS index on chunks.bm25_content. The
// options are validated by the caller, so interpolation into the PRAGMA is safe.
func rebuildFullTextIndex(ctx context.Context, q stypes.Querier, opts stypes.FullTextOptions) error {
	pragma := fmt.Sprintf(
		"PRAGMA create_fts_index('chunks', 'id', 'bm25_content', stemmer='%s', stopwords='%s', ignore='%s', strip_accents=1, lower=1, overwrite=1)",
		opts.Stemmer, opts.Stopwords, ftsIgnorePattern)
	if _, err := q.ExecContext(ctx, pragma); err != nil {
		return fmt.Errorf("failed to create FTS index: %w", err)
	}
	return nil
}

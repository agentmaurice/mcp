//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/marcboeker/go-duckdb/v2"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	stypes "github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage/types"
)

type writeRequest struct {
	ctx  context.Context
	fn   func(context.Context, stypes.Querier) error
	done chan error
}

type tenantDB struct {
	db      *sql.DB
	path    string
	writeCh chan writeRequest
}

// Manager handles per-tenant DuckDB connections and migrations.
type Manager struct {
	cfg     config.StorageConfig
	logger  *zap.Logger
	mu      sync.Mutex
	tenants map[string]*tenantDB
}

// NewManager creates a new DuckDB manager.
func NewManager(cfg config.StorageConfig, logger *zap.Logger) *Manager {
	return &Manager{
		cfg:     cfg,
		logger:  logger.Named("duckdb"),
		tenants: make(map[string]*tenantDB),
	}
}

// Close closes all tenant databases.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for tenant, td := range m.tenants {
		if td.db != nil {
			// Checkpoint before closing to flush WAL
			if _, err := td.db.Exec("CHECKPOINT"); err != nil {
				m.logger.Warn("failed to checkpoint before close", zap.String("tenant", tenant), zap.Error(err))
			}
			if err := td.db.Close(); err != nil {
				m.logger.Warn("failed to close tenant db", zap.String("tenant", tenant), zap.Error(err))
			}
		}
		close(td.writeCh)
	}
	m.tenants = make(map[string]*tenantDB)
	return nil
}

// Backend returns the backend name.
func (m *Manager) Backend() string {
	return "duckdb"
}

// Dialect returns the SQL dialect name.
func (m *Manager) Dialect() string {
	return "duckdb"
}

// TenantPath returns the DuckDB file path for a tenant.
func (m *Manager) TenantPath(tenantID string) string {
	base := filepath.Join(m.cfg.BaseDir, m.cfg.TenantsDirName, tenantID)
	return filepath.Join(base, m.cfg.DBFilename)
}

// DocumentsDir returns the documents directory for a tenant.
func (m *Manager) DocumentsDir(tenantID string) string {
	return filepath.Join(m.cfg.BaseDir, m.cfg.TenantsDirName, tenantID, m.cfg.DocumentsDirName)
}

// OpenTenant returns a read-capable Querier for a tenant.
func (m *Manager) OpenTenant(ctx context.Context, tenantID string) (stypes.Querier, error) {
	td, err := m.ensureTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return newSQLQuerier(td.db), nil
}

// WithWriter serializes write operations per tenant using a dedicated connection.
func (m *Manager) WithWriter(ctx context.Context, tenantID string, fn func(context.Context, stypes.Querier) error) error {
	td, err := m.ensureTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req := writeRequest{ctx: ctx, fn: fn, done: make(chan error, 1)}
	select {
	case td.writeCh <- req:
		return <-req.done
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) ensureTenant(ctx context.Context, tenantID string) (*tenantDB, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tenantID == "" {
		return nil, fmt.Errorf("tenantID is required")
	}
	if td, ok := m.tenants[tenantID]; ok {
		return td, nil
	}

	path := m.TenantPath(tenantID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("failed to create tenant directory: %w", err)
	}

	if m.cfg.DocumentsDirName != "" {
		_ = os.MkdirAll(m.DocumentsDir(tenantID), 0755)
	}

	db, err := m.openDB(ctx, path)
	if err != nil {
		// Attempt WAL recovery: remove WAL file and retry.
		walPath := path + ".wal"
		if _, statErr := os.Stat(walPath); statErr == nil {
			m.logger.Warn("database open/migration failed, attempting WAL recovery",
				zap.String("tenant", tenantID),
				zap.Error(err),
			)

			// Back up the corrupted WAL before removing it.
			backupPath := walPath + ".corrupted"
			if renameErr := os.Rename(walPath, backupPath); renameErr != nil {
				m.logger.Error("failed to backup corrupted WAL", zap.Error(renameErr))
				// Try removing it directly as fallback.
				_ = os.Remove(walPath)
			} else {
				m.logger.Info("corrupted WAL backed up", zap.String("backup", backupPath))
			}

			// Retry open.
			db, err = m.openDB(ctx, path)
			if err != nil {
				m.logger.Error("WAL recovery failed", zap.String("tenant", tenantID), zap.Error(err))
				return nil, fmt.Errorf("failed to open duckdb after WAL recovery: %w", err)
			}
			m.logger.Info("WAL recovery successful", zap.String("tenant", tenantID))
		} else {
			return nil, err
		}
	}

	// Checkpoint to flush WAL to main database file - prevents WAL corruption on restart
	if _, err := db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		m.logger.Warn("failed to checkpoint after schema migration", zap.Error(err))
	}

	writeCh := make(chan writeRequest, m.cfg.WriteQueueSize)
	td := &tenantDB{db: db, path: path, writeCh: writeCh}
	m.tenants[tenantID] = td

	go m.writerLoop(tenantID, td)

	return td, nil
}

// openDB opens a DuckDB database, configures connection pools, and runs schema migrations.
func (m *Manager) openDB(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open duckdb: %w", err)
	}
	if m.cfg.ReadMaxOpenConns > 0 {
		db.SetMaxOpenConns(m.cfg.ReadMaxOpenConns)
	}
	if m.cfg.ReadMaxIdleConns > 0 {
		db.SetMaxIdleConns(m.cfg.ReadMaxIdleConns)
	}

	if err := EnsureSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (m *Manager) writerLoop(tenantID string, td *tenantDB) {
	conn, err := td.db.Conn(context.Background())
	if err != nil {
		m.logger.Error("failed to open writer connection", zap.String("tenant", tenantID), zap.Error(err))
		return
	}
	defer conn.Close()

	querier := newSQLConnQuerier(conn)
	for req := range td.writeCh {
		ctx := req.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		err := req.fn(ctx, querier)
		select {
		case req.done <- err:
		default:
		}
	}
}

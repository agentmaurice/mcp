-- Store table: one store per deployment
CREATE TABLE IF NOT EXISTS stores (
    id TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL UNIQUE,
    gemini_store_name TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_stores_deployment_id ON stores(deployment_id);

-- File table: files imported into stores
CREATE TABLE IF NOT EXISTS files (
    id TEXT PRIMARY KEY,
    store_id TEXT NOT NULL,
    file_name TEXT NOT NULL,
    public_url TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    google_file_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    metadata TEXT,
    error_message TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_files_store_id ON files(store_id);
CREATE INDEX IF NOT EXISTS idx_files_status ON files(status);
CREATE INDEX IF NOT EXISTS idx_files_google_file_name ON files(google_file_name);

-- Search log table: track search queries
CREATE TABLE IF NOT EXISTS search_logs (
    id TEXT PRIMARY KEY,
    store_id TEXT NOT NULL,
    query TEXT NOT NULL,
    results_count INTEGER NOT NULL DEFAULT 0,
    execution_time_ms INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (store_id) REFERENCES stores(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_search_logs_store_id ON search_logs(store_id);
CREATE INDEX IF NOT EXISTS idx_search_logs_created_at ON search_logs(created_at);

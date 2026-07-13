package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite connection with migration support.
type DB struct {
	*sql.DB
	dataDir string
}

// Open initializes the SQLite database with migrations.
func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "charon.db")
	dsn := fmt.Sprintf("%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db := &DB{DB: sqlDB, dataDir: dataDir}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func (db *DB) migrate() error {
	ctx := context.Background()

	// Migration tracking table
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return err
	}

	var currentVersion int
	row := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations")
	if err := row.Scan(&currentVersion); err != nil {
		return err
	}

	if currentVersion < 1 {
		if err := db.migrateV1(); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			1, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return err
		}
	}

	if currentVersion < 2 {
		if err := db.migrateV2(); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			2, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return err
		}
	}

	if currentVersion < 3 {
		if err := db.migrateV3(); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			3, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return err
		}
	}

	return nil
}

func (db *DB) migrateV1() error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	statements := []string{
		`CREATE TABLE principals (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			disabled_at TEXT
		)`,
		`CREATE TABLE principal_projects (
			principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL,
			PRIMARY KEY (principal_id, project_id)
		)`,
		`CREATE TABLE principal_scopes (
			principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
			scope TEXT NOT NULL,
			PRIMARY KEY (principal_id, scope)
		)`,
		`CREATE TABLE obols (
			id TEXT PRIMARY KEY,
			principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
			secret_digest BLOB NOT NULL,
			audience TEXT NOT NULL,
			expires_at TEXT,
			revoked_at TEXT,
			created_at TEXT NOT NULL,
			last_used_at TEXT
		)`,
		`CREATE TABLE idempotency_records (
			principal_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			request_hash BLOB NOT NULL,
			status TEXT NOT NULL,
			resulting_resource_id TEXT,
			stored_response TEXT,
			created_at TEXT NOT NULL,
			PRIMARY KEY (principal_id, operation, idempotency_key)
		)`,
		`CREATE TABLE ledger_entries (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at TEXT NOT NULL,
			request_id TEXT NOT NULL,
			principal_id TEXT,
			operation TEXT NOT NULL,
			authorized_projects_json TEXT,
			result TEXT NOT NULL,
			latency_ms INTEGER NOT NULL,
			previous_hash BLOB,
			entry_hash BLOB NOT NULL
		)`,
		`CREATE INDEX idx_obols_principal ON obols(principal_id)`,
		`CREATE UNIQUE INDEX idx_idempotency ON idempotency_records(principal_id, operation, idempotency_key)`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration v1: %w", err)
		}
	}

	return tx.Commit()
}

func (db *DB) migrateV2() error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	statements := []string{
		`CREATE TABLE proposals (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			proposed_event_type TEXT NOT NULL,
			kind TEXT NOT NULL,
			content TEXT NOT NULL,
			reason TEXT,
			confidence REAL,
			tags TEXT,
			originating_principal TEXT NOT NULL,
			originating_client TEXT,
			originating_provider TEXT,
			originating_session TEXT,
			request_id TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at TEXT NOT NULL,
			decided_at TEXT,
			decided_by TEXT,
			resulting_lethe_event_id TEXT
		)`,
		`CREATE INDEX idx_proposals_status ON proposals(status)`,
		`CREATE INDEX idx_proposals_principal ON proposals(originating_principal)`,
		`CREATE INDEX idx_proposals_project ON proposals(project_id)`,
		`CREATE UNIQUE INDEX idx_proposals_idempotency ON proposals(originating_principal, idempotency_key)`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration v2: %w", err)
		}
	}

	return tx.Commit()
}

func (db *DB) migrateV3() error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	statements := []string{
		`ALTER TABLE principals ADD COLUMN agent_id TEXT`,
		`UPDATE principals SET agent_id = id WHERE agent_id IS NULL`,
		`CREATE INDEX idx_principals_agent ON principals(agent_id)`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration v3: %w", err)
		}
	}

	return tx.Commit()
}

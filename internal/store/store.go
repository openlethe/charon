package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite connection with migration support.
type DB struct {
	*sql.DB
	dataDir string
}

// Durability policy (WP8): every client-visible success is fully durable on
// commit — journal_mode=WAL with synchronous=FULL and foreign_keys enforced.
// Acceptable data loss is zero for committed transactions; the RPO is the
// transaction boundary. Storage must honor fsync (local disk or a volume
// that does); network filesystems without fsync guarantees are unsupported.
const (
	defaultSynchronous    = "FULL"
	defaultBusyTimeoutMS  = 5000
	defaultAutoCheckpoint = 1000
)

// Open initializes the SQLite database with migrations.
func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	// #nosec G302 -- directories require execute permission; 0700 is owner-only.
	if err := os.Chmod(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("secure data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "charon.db")
	// #nosec G304 -- CHARON_DATA_DIR is an explicit operator-controlled storage root.
	dbFile, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("create sqlite file: %w", err)
	}
	if err := dbFile.Close(); err != nil {
		return nil, fmt.Errorf("close sqlite file: %w", err)
	}
	if err := os.Chmod(dbPath, 0600); err != nil {
		return nil, fmt.Errorf("secure sqlite file: %w", err)
	}
	synchronous := envOr("CHARON_SQLITE_SYNCHRONOUS", defaultSynchronous)
	dsn := fmt.Sprintf("%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(%s)&_pragma=synchronous(%s)&_pragma=wal_autocheckpoint(%s)",
		dbPath,
		envOr("CHARON_SQLITE_BUSY_TIMEOUT_MS", fmt.Sprintf("%d", defaultBusyTimeoutMS)),
		synchronous,
		envOr("CHARON_SQLITE_AUTOCHECKPOINT", fmt.Sprintf("%d", defaultAutoCheckpoint)),
	)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db := &DB{DB: sqlDB, dataDir: dataDir}
	if err := db.verifyDurabilityPragmas(synchronous); err != nil {
		verifyErr := fmt.Errorf("durability verification: %w", err)
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, errors.Join(verifyErr, fmt.Errorf("close failed store: %w", closeErr))
		}
		return nil, verifyErr
	}
	if err := db.migrate(); err != nil {
		migrationErr := fmt.Errorf("migrate: %w", err)
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, errors.Join(migrationErr, fmt.Errorf("close failed store: %w", closeErr))
		}
		return nil, migrationErr
	}

	return db, nil
}

// verifyDurabilityPragmas fails closed when the storage engine did not apply
// the required durability configuration, so an unsupported volume is detected
// at startup rather than after data loss.
func (db *DB) verifyDurabilityPragmas(wantSynchronous string) error {
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("journal_mode = %s, want WAL", journalMode)
	}
	var synchronous int
	if err := db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("read synchronous: %w", err)
	}
	wantSync := map[string]int{"OFF": 0, "NORMAL": 1, "FULL": 2, "EXTRA": 3}[strings.ToUpper(wantSynchronous)]
	if synchronous != wantSync {
		return fmt.Errorf("synchronous = %d, want %d (%s)", synchronous, wantSync, wantSynchronous)
	}
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys not enforced")
	}
	return nil
}

// Close checkpoints the WAL (TRUNCATE) before closing so a clean shutdown
// leaves a single database file and the next open starts without recovery.
func (db *DB) Close() error {
	// Best-effort checkpoint: an interrupted checkpoint does not lose data
	// (WAL recovery on next open is correct), so only the close error counts.
	_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return db.DB.Close()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
	}

	if currentVersion < 2 {
		if err := db.migrateV2(); err != nil {
			return err
		}
	}

	if currentVersion < 3 {
		if err := db.migrateV3(); err != nil {
			return err
		}
	}

	if currentVersion < 4 {
		if err := db.migrateV4(); err != nil {
			return err
		}
	}

	if currentVersion < 5 {
		if err := db.migrateV5(); err != nil {
			return err
		}
	}

	if currentVersion < 6 {
		if err := db.migrateV6(); err != nil {
			return err
		}
	}

	if currentVersion < 7 {
		if err := db.migrateV7(); err != nil {
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
	if err := recordMigration(ctx, tx, 1); err != nil {
		return err
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
	if err := recordMigration(ctx, tx, 2); err != nil {
		return err
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
	if err := recordMigration(ctx, tx, 3); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateV4 extends the existing proposal table so Charon proposals can also
// represent Memory Git merge requests. It deliberately does not introduce a
// second proposal store or lifecycle.
func (db *DB) migrateV4() error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	statements := []string{
		`ALTER TABLE proposals ADD COLUMN changeset_id TEXT`,
		`ALTER TABLE proposals ADD COLUMN base_changeset_id TEXT`,
		`ALTER TABLE proposals ADD COLUMN head_changeset_id TEXT`,
		`ALTER TABLE proposals ADD COLUMN target_ref TEXT`,
		`ALTER TABLE proposals ADD COLUMN selected_ops_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE proposals ADD COLUMN conflict_ids_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE proposals ADD COLUMN review_findings_json TEXT NOT NULL DEFAULT '[]'`,
		`CREATE INDEX idx_proposals_changeset ON proposals(changeset_id)`,
		`CREATE INDEX idx_proposals_base ON proposals(base_changeset_id)`,
		`CREATE INDEX idx_proposals_target_ref ON proposals(target_ref)`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration v4: %w", err)
		}
	}
	if err := recordMigration(ctx, tx, 4); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateV5 binds every proposal to the canonical request that created it and
// to a recomputed state integrity digest. request_digest makes merge-proposal
// creation exactly-once: an idempotent replay is matched against the canonical
// request, and a same-key different-request replay fails closed. state_digest
// is verified on every load so a corrupted or tampered persisted proposal can
// never be normalized into a weaker one (for example widening a selected-ops
// partial merge into a full merge). The digest backfill for pre-v5 rows runs
// as a Go data migration (proposal.BackfillStateDigests) because it requires
// the canonical digest algorithm; rows whose persisted JSON does not decode
// keep a NULL digest and are quarantined fail-closed on load.
func (db *DB) migrateV5() error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	statements := []string{
		`ALTER TABLE proposals ADD COLUMN request_digest TEXT`,
		`ALTER TABLE proposals ADD COLUMN state_digest TEXT`,
		`CREATE INDEX idx_proposals_request_digest ON proposals(request_digest)`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration v5: %w", err)
		}
	}
	if err := recordMigration(ctx, tx, 5); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateV6 makes the audit trail a security control. audit_outbox records
// durable mutation intents and their outcomes so no protected mutation can
// succeed without audit evidence and every ambiguous cross-service outcome is
// recoverable. Ledger entries gain a purpose-specific keyed MAC (with key ID
// for rotation) so tampering cannot be hidden by recomputing the unkeyed
// same-database hash chain.
func (db *DB) migrateV6() error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	statements := []string{
		`CREATE TABLE audit_outbox (
			intent_id TEXT PRIMARY KEY,
			request_id TEXT NOT NULL,
			principal_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			project_id TEXT,
			request_digest TEXT,
			target_resource TEXT,
			intended_transition TEXT,
			status TEXT NOT NULL DEFAULT 'intent' CHECK (status IN ('intent', 'completed', 'failed', 'ambiguous')),
			detail TEXT,
			created_at TEXT NOT NULL,
			completed_at TEXT
		)`,
		`CREATE INDEX idx_audit_outbox_status ON audit_outbox(status, created_at)`,
		`CREATE INDEX idx_audit_outbox_operation ON audit_outbox(operation, created_at)`,
		`ALTER TABLE ledger_entries ADD COLUMN key_id TEXT`,
		`ALTER TABLE ledger_entries ADD COLUMN entry_mac BLOB`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration v6: %w", err)
		}
	}
	if err := recordMigration(ctx, tx, 6); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateV7 adds process-instance ownership to the audit outbox. Charon
// deployments can run sibling gateways against one database (Compose runs
// author and reviewer profiles), so restart reconciliation must only retire
// intents whose owning process is actually dead — never a live sibling's
// in-flight intent. Instances heartbeat; intents stamp their owner.
func (db *DB) migrateV7() error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	statements := []string{
		`CREATE TABLE charon_instances (
			instance_id TEXT PRIMARY KEY,
			started_at TEXT NOT NULL,
			last_heartbeat TEXT NOT NULL
		)`,
		`ALTER TABLE audit_outbox ADD COLUMN owner_id TEXT`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration v7: %w", err)
		}
	}
	if err := recordMigration(ctx, tx, 7); err != nil {
		return err
	}
	return tx.Commit()
}

func recordMigration(ctx context.Context, tx *sql.Tx, version int) error {
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
		version, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("record migration v%d: %w", version, err)
	}
	return nil
}

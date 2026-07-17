package reconcile

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/openlethe/charon/internal/store"
)

// buildTestDatabases creates a minimal but realistic Charon+Lethe pair: an
// approved merge proposal with its matching advancement, a pending proposal,
// an idempotency record, and a protected ref.
func buildTestDatabases(t *testing.T, dir string) (charonDB, letheDB string) {
	t.Helper()
	charonDB = filepath.Join(dir, "charon.db")
	letheDB = filepath.Join(dir, "lethe.db")

	cdb, err := sql.Open("sqlite", charonDB)
	if err != nil {
		t.Fatal(err)
	}
	defer cdb.Close()
	charonDDL := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations VALUES (6, 'now')`,
		`CREATE TABLE proposals (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, proposed_event_type TEXT NOT NULL,
			kind TEXT NOT NULL, content TEXT NOT NULL, reason TEXT, confidence REAL, tags TEXT,
			originating_principal TEXT NOT NULL, originating_client TEXT, originating_provider TEXT,
			originating_session TEXT, request_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending', created_at TEXT NOT NULL,
			decided_at TEXT, decided_by TEXT, resulting_lethe_event_id TEXT,
			changeset_id TEXT, base_changeset_id TEXT, head_changeset_id TEXT, target_ref TEXT,
			selected_ops_json TEXT NOT NULL DEFAULT '[]', conflict_ids_json TEXT NOT NULL DEFAULT '[]',
			review_findings_json TEXT NOT NULL DEFAULT '[]', request_digest TEXT, state_digest TEXT)`,
		`CREATE TABLE idempotency_records (
			principal_id TEXT NOT NULL, operation TEXT NOT NULL, idempotency_key TEXT NOT NULL,
			request_hash BLOB NOT NULL, status TEXT NOT NULL, resulting_resource_id TEXT,
			stored_response TEXT, created_at TEXT NOT NULL,
			PRIMARY KEY (principal_id, operation, idempotency_key))`,
		`CREATE TABLE ledger_entries (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL,
			request_id TEXT NOT NULL, principal_id TEXT, operation TEXT NOT NULL,
			authorized_projects_json TEXT, result TEXT NOT NULL, latency_ms INTEGER NOT NULL,
			previous_hash BLOB, entry_hash BLOB NOT NULL, key_id TEXT, entry_mac BLOB)`,
		`INSERT INTO proposals (id, project_id, proposed_event_type, kind, content,
			originating_principal, request_id, idempotency_key, status, created_at,
			resulting_lethe_event_id, target_ref, base_changeset_id)
		 VALUES
			('proposal-approved', 'proj', 'merge_request', 'merge_request', 'merge',
			'author', 'req-1', 'key-1', 'approved', '2026-01-01T00:00:00Z', 'head-merged', 'refs/shared/main', 'head-base'),
			('proposal-pending', 'proj', 'merge_request', 'merge_request', 'merge2',
			'author', 'req-2', 'key-2', 'pending', '2026-01-02T00:00:00Z', NULL, 'refs/shared/main', 'head-merged')`,
		`INSERT INTO idempotency_records VALUES ('author', 'memory_merge_propose', 'key-1', X'00', 'pending', 'proposal-approved', '', '2026-01-01T00:00:00Z')`,
		`INSERT INTO ledger_entries (created_at, request_id, operation, result, latency_ms, entry_hash)
		 VALUES ('2026-01-01T00:00:00Z', 'req-1', 'memory_merge', 'success', 1, X'0102')`,
	}
	for _, stmt := range charonDDL {
		if _, err := cdb.Exec(stmt); err != nil {
			t.Fatalf("charon fixture: %v", err)
		}
	}

	ldb, err := sql.Open("sqlite", letheDB)
	if err != nil {
		t.Fatal(err)
	}
	defer ldb.Close()
	letheDDL := []string{
		`CREATE TABLE schema_versions (name TEXT PRIMARY KEY, applied DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO schema_versions VALUES ('012_merge_authorizations.sql', '2026-01-01')`,
		`CREATE TABLE memory_refs (
			project_id TEXT NOT NULL, ref_name TEXT NOT NULL, head_changeset_id TEXT NOT NULL,
			protected INTEGER NOT NULL DEFAULT 0, created_at DATETIME, updated_at DATETIME,
			created_by_principal TEXT, PRIMARY KEY (project_id, ref_name))`,
		`CREATE TABLE memory_protected_ref_advances (
			advance_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, ref_name TEXT NOT NULL,
			expected_head TEXT NOT NULL, new_head TEXT NOT NULL, merge_proposal_id TEXT NOT NULL,
			proposal_digest TEXT NOT NULL, reviewer_principal TEXT NOT NULL, merger_principal TEXT NOT NULL,
			strategy TEXT NOT NULL, authorization_nonce TEXT NOT NULL, created_at DATETIME NOT NULL)`,
		`INSERT INTO memory_refs VALUES ('proj', 'refs/shared/main', 'head-merged', 1, '2026-01-01', '2026-01-01', 'system')`,
		`INSERT INTO memory_protected_ref_advances VALUES (
			'adv-1', 'proj', 'refs/shared/main', 'head-base', 'head-merged', 'proposal-approved',
			'digest', 'reviewer', 'merger', 'fast_forward', 'nonce-1', '2026-01-01')`,
	}
	for _, stmt := range letheDDL {
		if _, err := ldb.Exec(stmt); err != nil {
			t.Fatalf("lethe fixture: %v", err)
		}
	}
	return charonDB, letheDB
}

// restoreBackupSet copies a backup set to restore destinations, the way an
// operator restore would.
func restoreBackupSet(t *testing.T, backupDir, restoreDir string) (charonPath, lethePath string) {
	t.Helper()
	if err := os.MkdirAll(restoreDir, 0700); err != nil {
		t.Fatal(err)
	}
	charonPath = filepath.Join(restoreDir, "charon.db")
	lethePath = filepath.Join(restoreDir, "lethe.db")
	for _, pair := range [][2]string{
		{filepath.Join(backupDir, "charon.db"), charonPath},
		{filepath.Join(backupDir, "lethe.db"), lethePath},
	} {
		data, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pair[1], data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return charonPath, lethePath
}

func TestBackupAndReconcileCleanPair(t *testing.T) {
	dir := t.TempDir()
	charonDB, letheDB := buildTestDatabases(t, dir)
	backupDir := filepath.Join(dir, "backup")

	m, err := Backup(context.Background(), charonDB, letheDB, backupDir, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if m.BackupID == "" || m.Charon.SHA256 == "" || m.Lethe.SHA256 == "" {
		t.Fatalf("manifest incomplete: %#v", m)
	}
	if len(m.ProtectedRefs) != 1 || m.ProtectedRefs[0].Head != "head-merged" {
		t.Fatalf("protected refs = %#v", m.ProtectedRefs)
	}
	if m.LedgerTailHash == "" {
		t.Fatal("ledger tail hash missing")
	}

	// Restore drill: restore the set to a fresh location and validate it
	// against the manifest.
	restoreCharon, restoreLethe := restoreBackupSet(t, backupDir, filepath.Join(dir, "restored"))
	if _, err := CheckBackupManifest(filepath.Join(backupDir, "manifest.json"), restoreCharon, restoreLethe); err != nil {
		t.Fatal(err)
	}

	// Reconciliation passes on a consistent pair.
	report, err := Reconcile(context.Background(), charonDB, letheDB)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Clean {
		t.Fatalf("expected clean report, got %#v", report.Findings)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("unexpected findings: %#v", report.Findings)
	}
}

func TestReconcileDetectsCharonOlderThanLethe(t *testing.T) {
	dir := t.TempDir()
	charonDB, letheDB := buildTestDatabases(t, dir)

	// Restore "Charon older than Lethe": drop the approved proposal Charon-side
	// while Lethe retains the advancement.
	cdb, err := sql.Open("sqlite", charonDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cdb.Exec(`DELETE FROM proposals WHERE id = 'proposal-approved'`); err != nil {
		t.Fatal(err)
	}
	cdb.Close()

	report, err := Reconcile(context.Background(), charonDB, letheDB)
	if err != nil {
		t.Fatal(err)
	}
	if report.Clean {
		t.Fatal("inconsistent pair reported clean")
	}
	found := false
	for _, f := range report.Findings {
		if f.Check == "advancement-has-authorization" && f.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing advancement-has-authorization finding: %#v", report.Findings)
	}
}

func TestReconcileDetectsLetheOlderThanCharon(t *testing.T) {
	dir := t.TempDir()
	charonDB, letheDB := buildTestDatabases(t, dir)

	// Restore "Lethe older than Charon": drop the advancement Lethe-side.
	ldb, err := sql.Open("sqlite", letheDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ldb.Exec(`DELETE FROM memory_protected_ref_advances`); err != nil {
		t.Fatal(err)
	}
	ldb.Close()

	report, err := Reconcile(context.Background(), charonDB, letheDB)
	if err != nil {
		t.Fatal(err)
	}
	if report.Clean {
		t.Fatal("inconsistent pair reported clean")
	}
	found := false
	for _, f := range report.Findings {
		if f.Check == "approved-merge-has-advancement" && f.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing approved-merge-has-advancement finding: %#v", report.Findings)
	}
}

func TestManifestDetectsCorruptedBackup(t *testing.T) {
	dir := t.TempDir()
	charonDB, letheDB := buildTestDatabases(t, dir)
	backupDir := filepath.Join(dir, "backup")
	if _, err := Backup(context.Background(), charonDB, letheDB, backupDir, "v"); err != nil {
		t.Fatal(err)
	}
	// Restore, then corrupt the restored copy: the manifest must reject it.
	restoreCharon, restoreLethe := restoreBackupSet(t, backupDir, filepath.Join(dir, "restored"))
	f, err := os.OpenFile(restoreCharon, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("corruption"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := CheckBackupManifest(filepath.Join(backupDir, "manifest.json"), restoreCharon, restoreLethe); err == nil {
		t.Fatal("corrupted database passed manifest check")
	}
}

func TestReconcileWarnsOnStalePendingMerge(t *testing.T) {
	dir := t.TempDir()
	charonDB, letheDB := buildTestDatabases(t, dir)

	// Advance the ref past the pending proposal's base (another merge landed).
	ldb, err := sql.Open("sqlite", letheDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ldb.Exec(`UPDATE memory_refs SET head_changeset_id = 'head-newer' WHERE project_id = 'proj'`); err != nil {
		t.Fatal(err)
	}
	ldb.Close()

	report, err := Reconcile(context.Background(), charonDB, letheDB)
	if err != nil {
		t.Fatal(err)
	}
	// Warning only: the pair is structurally consistent but the pending merge
	// needs completion or quarantine.
	found := false
	for _, f := range report.Findings {
		if f.Check == "pending-merge-ref" && f.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing pending-merge warning: %#v", report.Findings)
	}
	if !report.Clean {
		t.Fatal("warning-only report must remain clean")
	}
}

func TestVerifyLocalRunsIntegrityChecks(t *testing.T) {
	dir := t.TempDir()
	report := &Report{StartedAt: time.Now().UTC(), Clean: true, Findings: []Finding{}}
	if err := VerifyLocal(context.Background(), dir, "", report); err != nil {
		t.Fatal(err)
	}
	if !report.Clean {
		t.Fatalf("fresh store should verify clean: %#v", report.Findings)
	}
}

func TestStoreOpenForFixture(t *testing.T) {
	// Guard: the reconcile package must keep working against the real store
	// migrations as they evolve.
	if _, err := store.Open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

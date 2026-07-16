package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify tables exist
	tables := []string{
		"principals", "principal_projects", "principal_scopes",
		"obols", "idempotency_records", "ledger_entries", "proposals", "schema_migrations",
	}
	for _, table := range tables {
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count)
		if err != nil {
			t.Errorf("checking table %s: %v", table, err)
			continue
		}
		if count != 1 {
			t.Errorf("table %s not found", table)
		}
	}

	var migrationCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 5 {
		t.Fatalf("migration count = %d, want 5", migrationCount)
	}

	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if dirInfo.Mode().Perm() != 0700 {
			t.Fatalf("data dir mode = %o, want 700", dirInfo.Mode().Perm())
		}
		dbInfo, err := os.Stat(filepath.Join(dir, "charon.db"))
		if err != nil {
			t.Fatal(err)
		}
		if dbInfo.Mode().Perm() != 0600 {
			t.Fatalf("database mode = %o, want 600", dbInfo.Mode().Perm())
		}
	}
}

func TestMigrationRepeatability(t *testing.T) {
	dir := t.TempDir()

	// First open
	db1, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	db1.Close()

	// Second open should succeed without errors
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}
	db2.Close()
}

func TestForeignKeys(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Inserting a project for non-existent principal should fail
	_, err = db.Exec("INSERT INTO principal_projects (principal_id, project_id) VALUES (?, ?)",
		"nonexistent", "test")
	if err == nil {
		t.Error("expected foreign key violation for non-existent principal")
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

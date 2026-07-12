package store

import (
	"os"
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
		"obols", "idempotency_records", "ledger_entries", "schema_migrations",
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

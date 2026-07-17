package store

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDurabilityPragmasActive(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Fatalf("journal_mode = %s, want wal", journal)
	}
	var synchronous int
	if err := db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 {
		t.Fatalf("synchronous = %d, want 2 (FULL)", synchronous)
	}
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatal("foreign_keys not enforced")
	}
}

func TestDurabilityVerificationFailsClosed(t *testing.T) {
	// A database without the required pragmas must be rejected by the
	// verifier — this is what happens on storage that cannot honor them.
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "charon.db")+"?_pragma=journal_mode(DELETE)&_pragma=synchronous(OFF)&_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := &DB{DB: raw, dataDir: dir}
	if err := db.verifyDurabilityPragmas("FULL"); err == nil {
		t.Fatal("verification accepted unsupported storage configuration")
	}
}

// TestCommittedDataSurvivesAbruptKill proves a committed row is durable when
// the process dies without a clean close (kill -9 equivalent: _exit without
// closing the database).
func TestCommittedDataSurvivesAbruptKill(t *testing.T) {
	if os.Getenv("CHARON_KILL_TEST_HELPER") == "1" {
		// Helper process: write and commit a principal, then die abruptly.
		dir := os.Args[len(os.Args)-1]
		db, err := Open(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if _, err := db.Exec(`INSERT INTO principals (id, name, created_at) VALUES ('kill-victim', 'killed', '2026-01-01T00:00:00Z')`); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println("committed")
		os.Exit(0) // abrupt: no Close, no checkpoint
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestCommittedDataSurvivesAbruptKill", "--", dir)
	cmd.Env = append(os.Environ(), "CHARON_KILL_TEST_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "committed") {
		t.Fatalf("helper did not commit: %s", out)
	}

	// Reopen: WAL recovery must surface the committed row.
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	err = db.QueryRow(`SELECT name FROM principals WHERE id = 'kill-victim'`).Scan(&name)
	if err != nil {
		t.Fatalf("committed row lost after abrupt kill: %v", err)
	}
	if name != "killed" {
		t.Fatalf("name = %q", name)
	}
}

package store

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

func TestReconcilePrincipalCreatesAndReplacesGrants(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	id, created, err := db.ReconcilePrincipal(ctx, "chatgpt", []string{"default"}, []string{"memory.read", "memory.propose"})
	if err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if !created || id == "" {
		t.Fatalf("created=%v id=%q", created, id)
	}

	sameID, created, err := db.ReconcilePrincipal(ctx, "chatgpt", []string{"*"}, []string{"memory.read", "memory.search", "thread.read"})
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if created || sameID != id {
		t.Fatalf("created=%v id=%q, want existing %q", created, sameID, id)
	}

	assertColumnValues(t, db, "SELECT project_id FROM principal_projects WHERE principal_id = ?", id, []string{"*"})
	assertColumnValues(t, db, "SELECT scope FROM principal_scopes WHERE principal_id = ?", id, []string{"memory.read", "memory.search", "thread.read"})
}

func TestReconcilePrincipalRejectsAmbiguousName(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	for _, id := range []string{"one", "two"} {
		if _, err := db.Exec("INSERT INTO principals (id, name, agent_id, created_at) VALUES (?, 'chatgpt', ?, '2026-01-01T00:00:00Z')", id, id); err != nil {
			t.Fatalf("insert principal: %v", err)
		}
	}
	if _, _, err := db.ReconcilePrincipal(context.Background(), "chatgpt", []string{"*"}, []string{"memory.read"}); err == nil {
		t.Fatal("expected ambiguous principal name error")
	}
}

func assertColumnValues(t *testing.T, db *DB, query, id string, want []string) {
	t.Helper()
	rows, err := db.Query(query, id)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, value)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
}

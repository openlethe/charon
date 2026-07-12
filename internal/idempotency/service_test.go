package idempotency

import (
	"context"
	"testing"

	"github.com/openlethe/charon/internal/store"
)

func TestStoreAndCheck(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := NewService(db)
	ctx := context.Background()

	args := map[string]interface{}{"key": "value"}

	// Store a record
	if err := svc.Store(ctx, "p1", "op1", "key1", args, "success", "res1", "resp"); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Check should find it
	check, err := svc.Check(ctx, "p1", "op1", "key1", args)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !check.Found {
		t.Error("expected to find record")
	}
	if !check.Matching {
		t.Error("expected matching hash")
	}
	if check.Record.ResultingResourceID != "res1" {
		t.Errorf("expected res1, got %s", check.Record.ResultingResourceID)
	}
}

func TestDuplicateKey(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := NewService(db)
	ctx := context.Background()

	args := map[string]interface{}{"key": "value"}

	// Store first record
	if err := svc.Store(ctx, "p1", "op1", "key1", args, "success", "res1", "resp"); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Try to store again with same key - should get ErrDuplicateKey
	err = svc.Store(ctx, "p1", "op1", "key1", args, "success", "res2", "resp2")
	if err == nil {
		t.Fatal("expected error for duplicate key")
	}
	if err != ErrDuplicateKey {
		t.Errorf("expected ErrDuplicateKey, got %v", err)
	}
}

func TestMismatchedInput(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := NewService(db)
	ctx := context.Background()

	args1 := map[string]interface{}{"key": "value1"}
	args2 := map[string]interface{}{"key": "value2"}

	// Store first record
	if err := svc.Store(ctx, "p1", "op1", "key1", args1, "success", "res1", "resp"); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Check with different input
	check, err := svc.Check(ctx, "p1", "op1", "key1", args2)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !check.Found {
		t.Error("expected to find record")
	}
	if check.Matching {
		t.Error("expected non-matching hash")
	}
}

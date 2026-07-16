package obol

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openlethe/charon/internal/store"
)

func TestMintAndVerify(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// Create principal
	_, err = db.Exec("INSERT INTO principals (id, name, created_at) VALUES (?, ?, ?)",
		"p1", "test", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}

	svc := NewService(db, "test-hmac-key")
	ctx := context.Background()

	obol, err := svc.Mint(ctx, "p1", "https://charon.example.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	if obol.ID == "" {
		t.Error("expected obol ID")
	}
	if obol.FullToken == "" {
		t.Error("expected full token")
	}
	if !strings.HasPrefix(obol.FullToken, "obol_") {
		t.Errorf("expected obol_ prefix, got %s", obol.FullToken)
	}

	// Verify
	principalID, err := svc.Verify(ctx, obol.FullToken, "https://charon.example.com")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if principalID != "p1" {
		t.Errorf("expected p1, got %s", principalID)
	}
}

func TestVerifyWrongAudience(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	_, err = db.Exec("INSERT INTO principals (id, name, created_at) VALUES (?, ?, ?)",
		"p1", "test", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}

	svc := NewService(db, "test-hmac-key")
	ctx := context.Background()

	obol, _ := svc.Mint(ctx, "p1", "https://charon.example.com", 24*time.Hour)

	_, err = svc.Verify(ctx, obol.FullToken, "https://other.example.com")
	if err == nil {
		t.Error("expected error for wrong audience")
	}
}

func TestMalformedStoredExpiryFailsClosed(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO principals (id, name, created_at) VALUES (?, ?, ?)",
		"p1", "test", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, "test-hmac-key")
	obol, err := svc.Mint(context.Background(), "p1", "https://charon.example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE obols SET expires_at = 'not-a-time' WHERE id = ?", obol.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(context.Background(), obol.FullToken, "https://charon.example.com"); err == nil {
		t.Fatal("malformed expiry was accepted")
	}
	if _, err := svc.List(context.Background(), "p1"); err == nil {
		t.Fatal("malformed expiry was hidden from the operator")
	}
}

func TestRevoke(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	_, err = db.Exec("INSERT INTO principals (id, name, created_at) VALUES (?, ?, ?)",
		"p1", "test", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}

	svc := NewService(db, "test-hmac-key")
	ctx := context.Background()

	obol, _ := svc.Mint(ctx, "p1", "https://charon.example.com", 24*time.Hour)

	if err := svc.Revoke(ctx, obol.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	_, err = svc.Verify(ctx, obol.FullToken, "https://charon.example.com")
	if err == nil {
		t.Error("expected error for revoked obol")
	}
}

func TestMintThenListRoundtrip(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO principals (id, name, created_at) VALUES (?, ?, ?)",
		"p1", "test", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, "test-hmac-key")
	minted, err := svc.Mint(context.Background(), "p1", "https://charon.example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	obols, err := svc.List(context.Background(), "p1")
	if err != nil {
		t.Fatalf("list freshly minted obol: %v", err)
	}
	if len(obols) != 1 {
		t.Fatalf("listed %d obols, want 1", len(obols))
	}
	got := obols[0]
	if got.ID != minted.ID {
		t.Fatalf("listed id %s, want %s", got.ID, minted.ID)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("listed CreatedAt is zero")
	}
	if !got.CreatedAt.Equal(minted.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, minted.CreatedAt)
	}
	if got.ExpiresAt == nil {
		t.Fatal("listed ExpiresAt is nil")
	}
}

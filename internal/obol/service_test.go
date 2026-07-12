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

package proposal

import (
	"context"
	"fmt"
	"testing"

	"github.com/openlethe/charon/internal/store"
)

func TestCreateAndGet(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// Migration v2 creates proposals table
	svc := NewService(db, nil)
	ctx := context.Background()

	req := CreateRequest{
		ProjectID:      "charon",
		EventType:      "record",
		Kind:           "decision",
		Content:        "Use Cloudflare Tunnel for initial deployment",
		Reason:         "Reduces initial relay complexity",
		Confidence:     0.96,
		Tags:           []string{"architecture", "deployment"},
		Provenance:     Provenance{Principal: "p1", AuthorType: "model"},
		IdempotencyKey: "test-key-1",
		RequestID:      "req-1",
	}

	p, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if p.ID == "" {
		t.Error("expected proposal ID")
	}
	if p.Status != StatusPending {
		t.Errorf("expected pending, got %s", p.Status)
	}

	// Get
	got, err := svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Content != req.Content {
		t.Errorf("content mismatch: %q vs %q", got.Content, req.Content)
	}

	// Find by idempotency
	found, err := svc.FindByIdempotency(ctx, "p1", "test-key-1")
	if err != nil {
		t.Fatalf("find by idempotency: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find proposal by idempotency")
	}
	if found.ID != p.ID {
		t.Errorf("idempotency lookup mismatch: %s vs %s", found.ID, p.ID)
	}
}

func TestApprove(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil)
	ctx := context.Background()

	req := CreateRequest{
		ProjectID:      "charon",
		EventType:      "record",
		Kind:           "decision",
		Content:        "Test content",
		IdempotencyKey: "test-key-2",
		RequestID:      "req-2",
		Provenance:     Provenance{Principal: "p1"},
	}

	p, _ := svc.Create(ctx, req)

	approved, err := svc.Approve(ctx, ApproveRequest{ProposalID: p.ID, DecidedBy: "admin"})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != StatusApproved {
		t.Errorf("expected approved, got %s", approved.Status)
	}
	if approved.DecidedBy != "admin" {
		t.Errorf("expected admin, got %s", approved.DecidedBy)
	}

	// Re-approve should return existing (idempotency)
	_, err = svc.Approve(ctx, ApproveRequest{ProposalID: p.ID, DecidedBy: "admin"})
	if err == nil {
		t.Error("expected error for already approved proposal")
	}
}

func TestReject(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil)
	ctx := context.Background()

	req := CreateRequest{
		ProjectID:      "charon",
		EventType:      "record",
		Content:        "Test content",
		IdempotencyKey: "test-key-3",
		RequestID:      "req-3",
		Provenance:     Provenance{Principal: "p1"},
	}

	p, _ := svc.Create(ctx, req)

	rejected, err := svc.Reject(ctx, RejectRequest{ProposalID: p.ID, DecidedBy: "admin"})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != StatusRejected {
		t.Errorf("expected rejected, got %s", rejected.Status)
	}

	// Reject again should fail
	_, err = svc.Reject(ctx, RejectRequest{ProposalID: p.ID, DecidedBy: "admin"})
	if err == nil {
		t.Error("expected error for already rejected proposal")
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		req := CreateRequest{
			ProjectID:      "charon",
			EventType:      "record",
			Content:        fmt.Sprintf("Content %d", i),
			IdempotencyKey: fmt.Sprintf("key-%d", i),
			RequestID:      fmt.Sprintf("req-%d", i),
			Provenance:     Provenance{Principal: "p1"},
		}
		_, _ = svc.Create(ctx, req)
	}

	proposals, err := svc.List(ctx, StatusPending, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(proposals) != 3 {
		t.Errorf("expected 3 proposals, got %d", len(proposals))
	}
}

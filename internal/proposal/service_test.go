package proposal

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	missing, err := svc.FindByIdempotency(ctx, "p1", "missing")
	if err != nil || missing != nil {
		t.Fatalf("idempotency miss = %#v, %v", missing, err)
	}

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

func TestLegacyApproveFailsClosedWithoutChangingProposal(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil)
	ctx := context.Background()
	p, err := svc.Create(ctx, CreateRequest{
		ProjectID: "charon", EventType: "record", Kind: "decision", Content: "Test content",
		IdempotencyKey: "test-key-2", RequestID: "req-2", Provenance: Provenance{Principal: "p1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Approve(ctx, ApproveRequest{ProposalID: p.ID, DecidedBy: "admin"})
	if err == nil || !strings.Contains(err.Error(), "not idempotent") {
		t.Fatalf("expected fail-closed approval error, got %#v, %v", got, err)
	}
	stored, err := svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusPending || stored.ResultingLetheEventID != "" || stored.DecidedBy != "" {
		t.Fatalf("legacy approval mutated proposal: %#v", stored)
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

func TestMemoryGitMergeRequestUsesExistingProposalLifecycle(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	svc := NewService(db, nil)
	ctx := context.Background()

	p, err := svc.Create(ctx, CreateRequest{
		ProjectID: "lethe", EventType: "merge_request", Kind: "merge_request",
		Content: "review memory branch", Provenance: Provenance{Principal: "author"},
		IdempotencyKey: "merge-1", RequestID: "req-merge-1",
		BaseChangesetID: "base-1", HeadChangesetID: "head-1", TargetRef: "refs/shared/main",
		SelectedOps: []int{0, 2}, ConflictIDs: []string{"conflict-1"},
	})
	if err != nil {
		t.Fatalf("create merge request: %v", err)
	}
	got, err := svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("get merge request: %v", err)
	}
	if got.BaseChangesetID != "base-1" || got.HeadChangesetID != "head-1" || got.TargetRef != "refs/shared/main" {
		t.Fatalf("merge fields not preserved: %#v", got)
	}
	if len(got.SelectedOps) != 2 || len(got.ConflictIDs) != 1 {
		t.Fatalf("merge collections not preserved: %#v", got)
	}
	got, err = svc.SetMergeResult(ctx, p.ID, "merge-result-1")
	if err != nil || got.ChangesetID != "merge-result-1" {
		t.Fatalf("persist merge result = %#v, %v", got, err)
	}
	if _, err := svc.SetMergeResult(ctx, p.ID, "different-result"); err == nil {
		t.Fatal("merge result was allowed to change")
	}

	got, err = svc.AddReviewFinding(ctx, p.ID, ReviewFinding{
		Reviewer: "reviewer", Verdict: ReviewVerdictApprove, Comment: "scope checked",
		TargetHeadChangesetID: "target-head-1",
	})
	if err != nil || len(got.ReviewFindings) != 1 || got.ReviewFindings[0].Reviewer != "reviewer" {
		t.Fatalf("review finding = %#v, %v", got, err)
	}
	if got.ReviewFindings[0].Verdict != ReviewVerdictApprove ||
		got.ReviewFindings[0].TargetDigest != ReviewTargetDigest(got, "target-head-1") {
		t.Fatalf("review decision was not bound to proposal state: %#v", got.ReviewFindings[0])
	}
	if _, err := svc.CompleteMerge(ctx, p.ID, "author", "merge-result-1"); err == nil {
		t.Fatal("originator was allowed to approve its own merge")
	}
	approved, err := svc.CompleteMerge(ctx, p.ID, "reviewer", "merge-result-1")
	if err != nil {
		t.Fatalf("complete merge: %v", err)
	}
	if approved.Status != StatusApproved || approved.ResultingLetheEventID != "merge-result-1" {
		t.Fatalf("approved merge = %#v", approved)
	}
}

func TestConcurrentReviewFindingsAppendAtomically(t *testing.T) {
	dir := t.TempDir()
	db1, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	first := NewService(db1, nil)
	second := NewService(db2, nil)
	proposal, err := first.Create(context.Background(), CreateRequest{
		ProjectID: "project-1", EventType: "merge_request", Kind: "merge_request",
		Provenance: Provenance{Principal: "author"}, IdempotencyKey: "concurrent-reviews",
		BaseChangesetID: "base", HeadChangesetID: "head", TargetRef: "refs/shared/main",
	})
	if err != nil {
		t.Fatal(err)
	}

	const reviewers = 32
	var wg sync.WaitGroup
	errCh := make(chan error, reviewers)
	for i := 0; i < reviewers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := first
			if i%2 == 1 {
				svc = second
			}
			_, err := svc.AddReviewFinding(context.Background(), proposal.ID, ReviewFinding{
				Reviewer: fmt.Sprintf("reviewer-%d", i), Verdict: ReviewVerdictApprove,
				Comment: "reviewed", TargetHeadChangesetID: "target-head",
			})
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("append concurrent review: %v", err)
		}
	}

	got, err := first.Get(context.Background(), proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ReviewFindings) != reviewers {
		t.Fatalf("review count = %d, want %d", len(got.ReviewFindings), reviewers)
	}
}

func TestMergeCancellationPreservesProposal(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	svc := NewService(db, nil)
	p, err := svc.Create(context.Background(), CreateRequest{
		ProjectID: "lethe", EventType: "merge_request", Kind: "merge_request",
		Provenance: Provenance{Principal: "author"}, IdempotencyKey: "cancel-1",
		HeadChangesetID: "head", TargetRef: "refs/shared/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(context.Background(), p.ID, "intruder"); err == nil {
		t.Fatal("non-originator cancelled proposal")
	}
	cancelled, err := svc.Cancel(context.Background(), p.ID, "author")
	if err != nil || cancelled.Status != StatusCancelled {
		t.Fatalf("cancel = %#v, %v", cancelled, err)
	}
	if _, err := svc.Get(context.Background(), p.ID); err != nil {
		t.Fatalf("cancelled proposal history was not preserved: %v", err)
	}
}

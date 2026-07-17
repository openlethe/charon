package audit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openlethe/charon/internal/store"
)

func openAuditService(t *testing.T) (*Service, *store.DB) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewService(db), db
}

func testIntent(op string) Intent {
	return Intent{
		RequestID: "req-1", PrincipalID: "principal-1", Operation: op,
		ProjectID: "project-1", RequestDigest: strings.Repeat("a", 64),
		TargetResource: "project-1:refs/shared/main", IntendedTransition: "mutate",
	}
}

func TestIntentLifecycle(t *testing.T) {
	svc, _ := openAuditService(t)
	ctx := context.Background()

	intent, err := svc.BeginIntent(ctx, testIntent("memory_merge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(ctx, intent.IntentID, ""); err != nil {
		t.Fatal(err)
	}
	// A finished intent cannot be finished again.
	if err := svc.Complete(ctx, intent.IntentID, ""); err == nil {
		t.Fatal("double finish must fail")
	}

	intent2, err := svc.BeginIntent(ctx, testIntent("memory_merge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Fail(ctx, intent2.IntentID, "upstream 409"); err != nil {
		t.Fatal(err)
	}
	intent3, err := svc.BeginIntent(ctx, testIntent("memory_merge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Ambiguous(ctx, intent3.IntentID, "timeout after possible commit"); err != nil {
		t.Fatal(err)
	}

	// Backlog counts only the ambiguous entry.
	backlog, err := svc.Backlog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if backlog != 1 {
		t.Fatalf("backlog = %d, want 1", backlog)
	}

	entries, err := svc.List(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	statuses := map[string]int{}
	for _, e := range entries {
		statuses[e.Status]++
	}
	if statuses["completed"] != 1 || statuses["failed"] != 1 || statuses["ambiguous"] != 1 {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestBeginIntentFailsClosedWhenPersistenceUnavailable(t *testing.T) {
	svc, db := openAuditService(t)
	db.Close() // storage gone: intents cannot persist
	_, err := svc.BeginIntent(context.Background(), testIntent("memory_merge"))
	if err == nil {
		t.Fatal("intent persisted without a database")
	}
}

func TestReconcileAfterRestart(t *testing.T) {
	svc, _ := openAuditService(t)
	ctx := context.Background()

	// Two intents never completed (process "died" mid-mutation).
	if _, err := svc.BeginIntent(ctx, testIntent("memory_merge")); err != nil {
		t.Fatal(err)
	}
	done, err := svc.BeginIntent(ctx, testIntent("memory_merge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(ctx, done.IntentID, ""); err != nil {
		t.Fatal(err)
	}
	third, err := svc.BeginIntent(ctx, testIntent("memory_merge"))
	if err != nil {
		t.Fatal(err)
	}
	_ = third

	reconciled, err := svc.ReconcileAfterRestart(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled != 2 {
		t.Fatalf("reconciled = %d, want 2", reconciled)
	}
	ambiguous, err := svc.List(ctx, "ambiguous", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ambiguous) != 2 {
		t.Fatalf("ambiguous = %d, want 2", len(ambiguous))
	}
	// Reconciliation is idempotent.
	reconciled, err = svc.ReconcileAfterRestart(ctx, 0)
	if err != nil || reconciled != 0 {
		t.Fatalf("second reconcile = %d, %v", reconciled, err)
	}
}

// A sibling gateway restarting must never retire a live sibling's in-flight
// intents: reconciliation is ownership-aware.
func TestReconcileSkipsLiveSiblings(t *testing.T) {
	_, db := openAuditService(t)
	ctx := context.Background()

	// Live sibling instance with a fresh heartbeat.
	sibling := NewService(db)
	if _, err := sibling.RegisterInstance(ctx); err != nil {
		t.Fatal(err)
	}
	live, err := sibling.BeginIntent(ctx, testIntent("memory_merge"))
	if err != nil {
		t.Fatal(err)
	}
	// Intent from a dead/unregistered owner.
	dead := NewService(db)
	dead.ownerID = "dead-instance"
	if _, err := dead.BeginIntent(ctx, testIntent("memory_merge")); err != nil {
		t.Fatal(err)
	}

	// This process restarts and reconciles with a short TTL.
	restarting := NewService(db)
	if _, err := restarting.RegisterInstance(ctx); err != nil {
		t.Fatal(err)
	}
	// The TTL must exceed any scheduling delay between the sibling's
	// heartbeat and this call (the race detector stretches that window past
	// 1ms deterministically); the dead owner is unregistered, so it
	// reconciles under any TTL.
	reconciled, err := restarting.ReconcileAfterRestart(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled = %d, want exactly 1 (the dead owner)", reconciled)
	}
	entries, err := restarting.List(ctx, "ambiguous", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].IntentID == live.IntentID {
		t.Fatalf("live sibling intent touched: %#v", entries)
	}
	// The live sibling can still complete its intent.
	if err := sibling.Complete(ctx, live.IntentID, ""); err != nil {
		t.Fatalf("live sibling completion failed: %v", err)
	}
}

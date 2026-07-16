package audit

import (
	"context"
	"strings"
	"testing"

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

	reconciled, err := svc.ReconcileAfterRestart(ctx)
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
	reconciled, err = svc.ReconcileAfterRestart(ctx)
	if err != nil || reconciled != 0 {
		t.Fatalf("second reconcile = %d, %v", reconciled, err)
	}
}

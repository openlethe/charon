package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/openlethe/charon/internal/audit"
	"github.com/openlethe/charon/internal/auth"
	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/idempotency"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/store"
)

// A security-relevant mutation must be refused when audit persistence is
// unavailable: no durable intent, no side effect.
func TestMutationRefusedWhenAuditUnavailable(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(
		&config.Config{MaxResultCount: 100},
		nil,
		ledger.NewService(db),
		proposal.NewService(db, nil),
		idempotency.NewService(db),
		audit.NewService(db),
	)
	db.Close()

	_, err = srv.beginAuditIntent(context.Background(), auth.Principal{ID: "principal"}, "memory_merge", "project", "digest", "target", "transition")
	if err == nil || !strings.Contains(err.Error(), "mutation refused") {
		t.Fatalf("mutation must be refused without audit persistence, got %v", err)
	}
}

// The outbox records the outcome of each audited mutation.
func TestFinishAuditRecordsOutcomes(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auditSvc := audit.NewService(db)
	srv := NewServer(
		&config.Config{MaxResultCount: 100},
		nil,
		ledger.NewService(db),
		proposal.NewService(db, nil),
		idempotency.NewService(db),
		auditSvc,
	)
	ctx := context.Background()

	intent, err := srv.beginAuditIntent(ctx, auth.Principal{ID: "principal"}, "memory_merge", "project", "digest", "target", "transition")
	if err != nil {
		t.Fatal(err)
	}
	srv.finishAudit(intent, nil)

	intent2, err := srv.beginAuditIntent(ctx, auth.Principal{ID: "principal"}, "memory_merge", "project", "digest", "target", "transition")
	if err != nil {
		t.Fatal(err)
	}
	srv.finishAudit(intent2, context.DeadlineExceeded)

	entries, err := auditSvc.List(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	statuses := map[string]bool{}
	for _, e := range entries {
		statuses[e.Status] = true
	}
	if !statuses["completed"] || !statuses["ambiguous"] {
		t.Fatalf("statuses = %v", statuses)
	}
}

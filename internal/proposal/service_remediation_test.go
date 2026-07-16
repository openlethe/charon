package proposal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlethe/charon/internal/store"
)

func openTestService(t *testing.T) (*Service, *store.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewService(db, nil), db, dir
}

func mergeCreateRequest(key string) CreateRequest {
	return CreateRequest{
		ProjectID: "project-1", EventType: "merge_request", Kind: "merge_request",
		Content:         "merge session work",
		Provenance:      Provenance{Principal: "author-1"},
		IdempotencyKey:  key,
		RequestID:       "req-1",
		BaseChangesetID: "base-1",
		HeadChangesetID: "head-1",
		TargetRef:       "refs/shared/main",
		SelectedOps:     []int{0, 2},
		ConflictIDs:     []string{"conflict-1"},
	}
}

func mergeDigestInput(keyPrincipal string) RequestDigestInput {
	return RequestDigestInput{
		Operation: "memory_merge_propose", Principal: keyPrincipal,
		ProjectID: "project-1", TargetRef: "refs/shared/main",
		BaseChangesetID: "base-1", HeadChangesetID: "head-1",
		SelectedOps: []int{0, 2}, Message: "merge session work",
	}
}

// --- WP2: atomic idempotent proposal creation ---

func TestCreateIdempotentExactReplayReturnsOriginal(t *testing.T) {
	svc, db, _ := openTestService(t)
	ctx := context.Background()

	first, replay, err := svc.CreateIdempotent(ctx, mergeCreateRequest("k1"), mergeDigestInput("author-1"))
	if err != nil || replay {
		t.Fatalf("first create: replay=%v err=%v", replay, err)
	}
	second, replay, err := svc.CreateIdempotent(ctx, mergeCreateRequest("k1"), mergeDigestInput("author-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !replay || second.ID != first.ID {
		t.Fatalf("replay=%v id=%s want %s", replay, second.ID, first.ID)
	}
	if second.Status != StatusPending {
		t.Fatalf("status = %s", second.Status)
	}

	// The proposal row and the generic idempotency record commit atomically.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM idempotency_records
		WHERE principal_id = 'author-1' AND operation = 'memory_merge_propose' AND idempotency_key = 'k1'
		AND resulting_resource_id = ?`, first.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("idempotency record count=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM proposals
		WHERE originating_principal = 'author-1' AND idempotency_key = 'k1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("proposal count=%d err=%v", count, err)
	}
}

func TestCreateIdempotentMismatchedReplayFailsClosed(t *testing.T) {
	svc, _, _ := openTestService(t)
	ctx := context.Background()

	if _, _, err := svc.CreateIdempotent(ctx, mergeCreateRequest("k2"), mergeDigestInput("author-1")); err != nil {
		t.Fatal(err)
	}
	changed := mergeDigestInput("author-1")
	changed.HeadChangesetID = "head-DIFFERENT"
	_, _, err := svc.CreateIdempotent(ctx, mergeCreateRequest("k2"), changed)
	if !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("mismatched replay err = %v, want ErrIdempotencyMismatch", err)
	}
}

func TestCreateIdempotentConcurrentIdenticalConverge(t *testing.T) {
	svc, db, _ := openTestService(t)
	ctx := context.Background()

	const writers = 16
	var wg sync.WaitGroup
	results := make([]string, writers)
	replays := make([]bool, writers)
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, replay, err := svc.CreateIdempotent(ctx, mergeCreateRequest("k3"), mergeDigestInput("author-1"))
			if p != nil {
				results[i] = p.ID
			}
			replays[i] = replay
			errs[i] = err
		}(i)
	}
	wg.Wait()

	var winners int
	for i := 0; i < writers; i++ {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("writer %d got proposal %s, want %s", i, results[i], results[0])
		}
		if !replays[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM proposals
		WHERE originating_principal = 'author-1' AND idempotency_key = 'k3'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("proposal count = %d, want 1 (err=%v)", count, err)
	}
}

func TestCreateIdempotentConcurrentMismatchedSingleWinner(t *testing.T) {
	svc, _, _ := openTestService(t)
	ctx := context.Background()

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := mergeDigestInput("author-1")
			input.Message = fmt.Sprintf("variant-%d", i)
			_, _, errs[i] = svc.CreateIdempotent(ctx, mergeCreateRequest("k4"), input)
		}(i)
	}
	wg.Wait()

	var succeeded, mismatched int
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrIdempotencyMismatch):
			mismatched++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || mismatched != writers-1 {
		t.Fatalf("succeeded=%d mismatched=%d, want 1 and %d", succeeded, mismatched, writers-1)
	}
}

func TestCreateIdempotentReplayAcrossRestart(t *testing.T) {
	svc, _, dir := openTestService(t)
	ctx := context.Background()

	first, _, err := svc.CreateIdempotent(ctx, mergeCreateRequest("k5"), mergeDigestInput("author-1"))
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a restart: reopen the same database with a fresh service.
	db2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	svc2 := NewService(db2, nil)

	second, replay, err := svc2.CreateIdempotent(ctx, mergeCreateRequest("k5"), mergeDigestInput("author-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !replay || second.ID != first.ID {
		t.Fatalf("after restart: replay=%v id=%s want %s", replay, second.ID, first.ID)
	}
}

// --- WP3: fail-closed persisted corruption ---

var corruptKeySeq int

func createValidMergeProposal(t *testing.T, svc *Service) *Proposal {
	t.Helper()
	corruptKeySeq++
	key := fmt.Sprintf("corrupt-k-%d", corruptKeySeq)
	p, _, err := svc.CreateIdempotent(context.Background(), mergeCreateRequest(key), mergeDigestInput("author-1"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCorruptSelectedOpsFailsClosed(t *testing.T) {
	svc, db, _ := openTestService(t)
	p := createValidMergeProposal(t, svc)

	if _, err := db.Exec(`UPDATE proposals SET selected_ops_json = 'not-json' WHERE id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Get(context.Background(), p.ID)
	if !errors.Is(err, ErrProposalCorrupt) {
		t.Fatalf("err = %v, want ErrProposalCorrupt", err)
	}
}

func TestCorruptSelectedOpsNegativeAndDuplicateFailClosed(t *testing.T) {
	svc, db, _ := openTestService(t)
	for _, payload := range []string{"[-1]", "[1, 1]", "[1.5]"} {
		p := createValidMergeProposal(t, svc)
		if _, err := db.Exec(`UPDATE proposals SET selected_ops_json = ? WHERE id = ?`, payload, p.ID); err != nil {
			t.Fatal(err)
		}
		_, err := svc.Get(context.Background(), p.ID)
		if !errors.Is(err, ErrProposalCorrupt) {
			t.Fatalf("payload %s: err = %v, want ErrProposalCorrupt", payload, err)
		}
	}
}

func TestCorruptReviewFindingsFailClosed(t *testing.T) {
	svc, db, _ := openTestService(t)
	p := createValidMergeProposal(t, svc)

	cases := []string{
		`not-json`,
		`[{"reviewer":"r","verdict":"bogus","comment":"c","target_head_changeset_id":"h","target_digest":"` + strings.Repeat("a", 64) + `","created_at":"2026-01-01T00:00:00Z"}]`,
		`[{"reviewer":"","verdict":"approve","comment":"c","target_head_changeset_id":"h","target_digest":"` + strings.Repeat("a", 64) + `","created_at":"2026-01-01T00:00:00Z"}]`,
		`[{"reviewer":"r","verdict":"approve","comment":"c","target_head_changeset_id":"","target_digest":"","created_at":"2026-01-01T00:00:00Z"}]`,
	}
	for i, payload := range cases {
		if _, err := db.Exec(`UPDATE proposals SET review_findings_json = ? WHERE id = ?`, payload, p.ID); err != nil {
			t.Fatal(err)
		}
		_, err := svc.Get(context.Background(), p.ID)
		if !errors.Is(err, ErrProposalCorrupt) {
			t.Fatalf("case %d: err = %v, want ErrProposalCorrupt", i, err)
		}
	}
}

func TestCorruptConflictIDsFailClosed(t *testing.T) {
	svc, db, _ := openTestService(t)
	p := createValidMergeProposal(t, svc)

	if _, err := db.Exec(`UPDATE proposals SET conflict_ids_json = '[""]' WHERE id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Get(context.Background(), p.ID)
	if !errors.Is(err, ErrProposalCorrupt) {
		t.Fatalf("err = %v, want ErrProposalCorrupt", err)
	}
}

func TestCorruptTimestampsFailClosed(t *testing.T) {
	svc, db, _ := openTestService(t)
	p := createValidMergeProposal(t, svc)

	if _, err := db.Exec(`UPDATE proposals SET created_at = 'not-a-time' WHERE id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Get(context.Background(), p.ID)
	if !errors.Is(err, ErrProposalCorrupt) {
		t.Fatalf("err = %v, want ErrProposalCorrupt", err)
	}
}

func TestStateTamperingInvalidatesApproval(t *testing.T) {
	svc, db, _ := openTestService(t)
	p := createValidMergeProposal(t, svc)

	// Approval state that a merge would rely on.
	if _, err := svc.AddReviewFinding(context.Background(), p.ID, ReviewFinding{
		Reviewer: "reviewer-1", Verdict: ReviewVerdictApprove, Comment: "ok", TargetHeadChangesetID: "target-head",
	}); err != nil {
		t.Fatal(err)
	}

	// Tamper with each protected field; every one must make loading fail closed.
	tampers := map[string]string{
		"selected ops widened": `UPDATE proposals SET selected_ops_json = '[]' WHERE id = ?`,
		"target ref moved":     `UPDATE proposals SET target_ref = 'refs/shared/other' WHERE id = ?`,
		"base swapped":         `UPDATE proposals SET base_changeset_id = 'evil' WHERE id = ?`,
		"head swapped":         `UPDATE proposals SET head_changeset_id = 'evil' WHERE id = ?`,
		"merge result swapped": `UPDATE proposals SET changeset_id = 'evil' WHERE id = ?`,
		"status flipped":       `UPDATE proposals SET status = 'approved' WHERE id = ?`,
	}
	for name, query := range tampers {
		victim := createValidMergeProposal(t, svc)
		if _, err := svc.AddReviewFinding(context.Background(), victim.ID, ReviewFinding{
			Reviewer: "reviewer-1", Verdict: ReviewVerdictApprove, Comment: "ok", TargetHeadChangesetID: "target-head",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(query, victim.ID); err != nil {
			t.Fatal(err)
		}
		_, err := svc.Get(context.Background(), victim.ID)
		if !errors.Is(err, ErrProposalCorrupt) {
			t.Fatalf("%s: err = %v, want ErrProposalCorrupt", name, err)
		}
	}
}

func TestLegacyProposalBackfillAndQuarantine(t *testing.T) {
	svc, db, _ := openTestService(t)
	ctx := context.Background()

	// Insert rows directly, simulating pre-v5 persisted proposals with no digest.
	validTags := `[]`
	insertLegacy := func(id, selectedOps string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO proposals (
			id, project_id, proposed_event_type, kind, content, reason, confidence,
			tags, originating_principal, originating_client, originating_provider,
			originating_session, request_id, idempotency_key, status, created_at,
			decided_at, decided_by, resulting_lethe_event_id,
			changeset_id, base_changeset_id, head_changeset_id, target_ref,
			selected_ops_json, conflict_ids_json, review_findings_json
		) VALUES (?, 'project-1', 'merge_request', 'merge_request', 'legacy', '', 0,
			?, 'author-1', '', '', '', 'req', ?, 'pending', ?,
			NULL, NULL, NULL, NULL, 'base-1', 'head-1', 'refs/shared/main', ?, '[]', '[]')`,
			id, validTags, "legacy-"+id, time.Now().UTC().Format(time.RFC3339Nano), selectedOps)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertLegacy("legacy-valid", `[]`)
	insertLegacy("legacy-corrupt", `not-json`)

	migrated, quarantined, err := svc.MigrateStateDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if migrated != 1 || quarantined != 1 {
		t.Fatalf("migrated=%d quarantined=%d, want 1 and 1", migrated, quarantined)
	}

	// The valid legacy row now loads and verifies.
	if _, err := svc.Get(ctx, "legacy-valid"); err != nil {
		t.Fatalf("migrated legacy proposal should load: %v", err)
	}
	// The corrupt one fails closed; its digest stays NULL, so it can never
	// become loadable.
	_, err = svc.Get(ctx, "legacy-corrupt")
	if !errors.Is(err, ErrProposalCorrupt) {
		t.Fatalf("err = %v, want ErrProposalCorrupt", err)
	}
	var digest string
	if err := db.QueryRow(`SELECT COALESCE(state_digest, '') FROM proposals WHERE id = 'legacy-corrupt'`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != "" {
		t.Fatal("corrupt legacy row must keep NULL digest")
	}
	// Backfill is idempotent.
	migrated, quarantined, err = svc.MigrateStateDigests(ctx)
	if err != nil || migrated != 0 || quarantined != 1 {
		t.Fatalf("second backfill: migrated=%d quarantined=%d err=%v", migrated, quarantined, err)
	}
}

func TestLegacyProposalWithoutBackfillIsQuarantined(t *testing.T) {
	svc, db, _ := openTestService(t)
	ctx := context.Background()

	// A decodable legacy row that the backfill has not stamped (for example
	// written by an older binary after migration) is quarantined fail-closed.
	_, err := db.Exec(`INSERT INTO proposals (
		id, project_id, proposed_event_type, kind, content, reason, confidence,
		tags, originating_principal, originating_client, originating_provider,
		originating_session, request_id, idempotency_key, status, created_at,
		decided_at, decided_by, resulting_lethe_event_id,
		changeset_id, base_changeset_id, head_changeset_id, target_ref,
		selected_ops_json, conflict_ids_json, review_findings_json
	) VALUES ('legacy-unbackfilled', 'project-1', 'merge_request', 'merge_request', 'legacy', '', 0,
		'[]', 'author-1', '', '', '', 'req', 'legacy-unbackfilled-key', 'pending', ?,
		NULL, NULL, NULL, NULL, 'base-1', 'head-1', 'refs/shared/main', '[]', '[]', '[]')`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Get(ctx, "legacy-unbackfilled")
	if !errors.Is(err, ErrProposalQuarantined) {
		t.Fatalf("err = %v, want ErrProposalQuarantined", err)
	}
}

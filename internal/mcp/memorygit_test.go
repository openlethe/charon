package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	markmcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/openlethe/charon/internal/audit"
	"github.com/openlethe/charon/internal/auth"
	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/idempotency"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/store"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

func newMemoryGitPolicyServer(t *testing.T) (*Server, *proposal.Service) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	proposalSvc := proposal.NewService(db, nil)
	return NewServer(&config.Config{MaxResultCount: 100}, nil, ledger.NewService(db), proposalSvc, idempotency.NewService(db), audit.NewService(db)), proposalSvc
}

func TestMemoryGitOwnedRefPolicy(t *testing.T) {
	server, _ := newMemoryGitPolicyServer(t)
	p := auth.Principal{ID: "principal-chatgpt", AgentID: "chatgpt", Scopes: map[string]struct{}{"memory.commit": {}}, Projects: map[string]struct{}{"lethe": {}}}
	for _, ref := range []string{"refs/agents/chatgpt/main", "refs/sessions/chatgpt/session-1"} {
		if err := server.requireOwnedMemoryRef(context.Background(), p, "lethe", ref, true); err != nil {
			t.Errorf("own ref %s denied: %v", ref, err)
		}
	}
	for _, ref := range []string{"refs/agents/archimedes/main", "refs/sessions/archimedes/session-1", "refs/repos/lethe/main", "refs/shared/main"} {
		if err := server.requireOwnedMemoryRef(context.Background(), p, "lethe", ref, true); err == nil {
			t.Errorf("unowned ref %s allowed", ref)
		}
	}
	p.Scopes["memory.write"] = struct{}{}
	if err := server.requireOwnedMemoryRef(context.Background(), p, "lethe", "refs/repos/lethe/local", true); err != nil {
		t.Fatalf("trusted local principal broader ref denied: %v", err)
	}
}

func TestMemoryRepoInitRequiresCommitOnExactProject(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/memory/allowed/legacy-root" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"changeset":null,"ref":{"project_id":"allowed","ref_name":"refs/shared/main","head_changeset_id":"root","protected":true}}`)
	}))
	defer upstream.Close()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := lethe.NewClient(upstream.URL, "key", time.Second, 1<<20)
	server := NewServer(&config.Config{MaxResultCount: 100}, client, ledger.NewService(db), proposal.NewService(db, client), idempotency.NewService(db), audit.NewService(db))
	request := markmcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{"project": "allowed"}

	wildcardCtx := WithAuth(context.Background(), auth.Principal{
		ID: "wildcard-writer", Projects: map[string]struct{}{"*": {}},
		Scopes: map[string]struct{}{"memory.commit": {}},
	}, "req-init-wildcard")
	if _, err := server.handleMemoryRepoInit(wildcardCtx, request); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("wildcard repository initialization should be denied, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("denied initialization reached upstream %d times", calls)
	}

	exactCtx := WithAuth(context.Background(), auth.Principal{
		ID: "principal-chatgpt", AgentID: "chatgpt", Projects: map[string]struct{}{"allowed": {}},
		Scopes: map[string]struct{}{"memory.commit": {}},
	}, "req-init")
	result, err := server.handleMemoryRepoInit(exactCtx, request)
	if err != nil || result == nil {
		t.Fatalf("repository initialization = %#v, %v", result, err)
	}
	if calls != 1 {
		t.Fatalf("successful initialization reached upstream %d times, want 1", calls)
	}
}

func TestMemoryChangesetRequiresCASBeforeUpstream(t *testing.T) {
	server, _ := newMemoryGitPolicyServer(t)
	request := markmcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{
		"project": "lethe", "ref": "refs/agents/chatgpt/main", "message": "local",
		"idempotency_key": "commit-1", "ops": []interface{}{},
	}
	ctx := WithAuth(context.Background(), auth.Principal{
		ID: "principal-chatgpt", AgentID: "chatgpt", Projects: map[string]struct{}{"lethe": {}},
		Scopes: map[string]struct{}{"memory.commit": {}},
	}, "req-commit")
	_, err := server.handleMemoryChangesetCreate(ctx, request)
	if err == nil || !strings.Contains(err.Error(), "expected_head") {
		t.Fatalf("missing CAS head should fail before upstream, got %v", err)
	}
}

func TestMemoryMergeDeniesSelfApprovalBeforeUpstream(t *testing.T) {
	server, proposalSvc := newMemoryGitPolicyServer(t)
	p, err := proposalSvc.Create(context.Background(), proposal.CreateRequest{
		ProjectID: "lethe", EventType: "merge_request", Kind: "merge_request",
		Provenance: proposal.Provenance{Principal: "author"}, IdempotencyKey: "proposal-1",
		BaseChangesetID: "base", HeadChangesetID: "head", TargetRef: "refs/shared/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := markmcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{"proposal_id": p.ID, "idempotency_key": "merge-1"}
	ctx := WithAuth(context.Background(), auth.Principal{
		ID: "author", AgentID: "chatgpt", Projects: map[string]struct{}{"lethe": {}},
		Scopes: map[string]struct{}{"memory.merge": {}},
	}, "req-merge")
	_, err = server.handleMemoryMerge(ctx, request)
	if err == nil || !strings.Contains(err.Error(), "own merge") {
		t.Fatalf("self merge should be denied before upstream, got %v", err)
	}
}

func TestMemoryMergeReviewDeniesSelfReview(t *testing.T) {
	server, proposalSvc := newMemoryGitPolicyServer(t)
	p, err := proposalSvc.Create(context.Background(), proposal.CreateRequest{
		ProjectID: "lethe", EventType: "merge_request", Kind: "merge_request",
		Provenance: proposal.Provenance{Principal: "author"}, IdempotencyKey: "proposal-review-1",
		BaseChangesetID: "base", HeadChangesetID: "head", TargetRef: "refs/shared/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := markmcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{"proposal_id": p.ID, "verdict": "approve", "comment": "looks good"}
	ctx := WithAuth(context.Background(), auth.Principal{
		ID: "author", AgentID: "chatgpt", Projects: map[string]struct{}{"lethe": {}},
		Scopes: map[string]struct{}{"memory.review": {}},
	}, "req-review")
	_, err = server.handleMemoryMergeReview(ctx, request)
	if err == nil || !strings.Contains(err.Error(), "own merge") {
		t.Fatalf("self review should be denied, got %v", err)
	}
	got, err := proposalSvc.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ReviewFindings) != 0 {
		t.Fatalf("self review was persisted: %#v", got.ReviewFindings)
	}
}

func TestMemoryMergeReviewBindsCurrentProtectedTarget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/memory/lethe/refs" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"project_id":"lethe","ref_name":"refs/shared/main","head_changeset_id":"target-head","protected":true}]`)
	}))
	defer upstream.Close()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := lethe.NewClient(upstream.URL, "key", time.Second, 1<<20)
	proposalSvc := proposal.NewService(db, client)
	server := NewServer(&config.Config{MaxResultCount: 100}, client, ledger.NewService(db), proposalSvc, idempotency.NewService(db), audit.NewService(db))
	p, err := proposalSvc.Create(context.Background(), proposal.CreateRequest{
		ProjectID: "lethe", EventType: "merge_request", Kind: "merge_request",
		Provenance: proposal.Provenance{Principal: "author"}, IdempotencyKey: "proposal-review-target",
		BaseChangesetID: "base", HeadChangesetID: "head", TargetRef: "refs/shared/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := markmcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{"proposal_id": p.ID, "verdict": "approve", "comment": "reviewed exact target"}
	ctx := WithAuth(context.Background(), auth.Principal{
		ID: "reviewer", AgentID: "reviewer", Projects: map[string]struct{}{"lethe": {}},
		Scopes: map[string]struct{}{"memory.review": {}},
	}, "req-review-target")
	if _, err := server.handleMemoryMergeReview(ctx, request); err != nil {
		t.Fatalf("review: %v", err)
	}
	got, err := proposalSvc.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ReviewFindings) != 1 || got.ReviewFindings[0].TargetHeadChangesetID != "target-head" {
		t.Fatalf("review target was not persisted: %#v", got.ReviewFindings)
	}
	if got.ReviewFindings[0].TargetDigest != proposal.ReviewTargetDigest(got, "target-head") {
		t.Fatalf("review digest was not bound to the protected target: %#v", got.ReviewFindings[0])
	}
}

func TestMemoryMergeRequiresCurrentIndependentApproval(t *testing.T) {
	p := &proposal.Proposal{
		ID: "proposal-1", ProjectID: "lethe", OriginatingPrincipal: "author",
		BaseChangesetID: "base", HeadChangesetID: "head", TargetRef: "refs/shared/main",
		SelectedOps: []int{0, 2},
	}
	const targetHead = "target-head"
	digest := proposal.ReviewTargetDigest(p, targetHead)
	cases := []struct {
		name    string
		finding proposal.ReviewFinding
		want    bool
	}{
		{name: "self approval", finding: proposal.ReviewFinding{Reviewer: "author", Verdict: proposal.ReviewVerdictApprove, TargetDigest: digest}},
		{name: "request changes", finding: proposal.ReviewFinding{Reviewer: "reviewer", Verdict: proposal.ReviewVerdictRequestChanges, TargetDigest: digest}},
		{name: "rejection", finding: proposal.ReviewFinding{Reviewer: "reviewer", Verdict: proposal.ReviewVerdictReject, TargetDigest: digest}},
		{name: "stale approval", finding: proposal.ReviewFinding{Reviewer: "reviewer", Verdict: proposal.ReviewVerdictApprove, TargetDigest: "stale"}},
		{name: "current approval", finding: proposal.ReviewFinding{Reviewer: "reviewer", Verdict: proposal.ReviewVerdictApprove, TargetDigest: digest}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p.ReviewFindings = []proposal.ReviewFinding{tc.finding}
			reviewer, ok := independentApprovalReviewer(p, targetHead)
			if ok != tc.want {
				t.Fatalf("approval = %v, want %v", ok, tc.want)
			}
			if tc.want && reviewer != "reviewer" {
				t.Fatalf("reviewer = %q", reviewer)
			}
		})
	}

	p.ReviewFindings = []proposal.ReviewFinding{
		{Reviewer: "reviewer", Verdict: proposal.ReviewVerdictApprove, TargetDigest: digest},
		{Reviewer: "reviewer", Verdict: proposal.ReviewVerdictRequestChanges, TargetDigest: digest},
	}
	if _, ok := independentApprovalReviewer(p, targetHead); ok {
		t.Fatal("later request-changes verdict did not revoke the earlier approval")
	}

	p.ReviewFindings = append(p.ReviewFindings,
		proposal.ReviewFinding{Reviewer: "reviewer", Verdict: proposal.ReviewVerdictApprove, TargetDigest: digest},
	)
	if reviewer, ok := independentApprovalReviewer(p, targetHead); !ok || reviewer != "reviewer" {
		t.Fatalf("latest approval did not resolve the reviewer's objection: %q %v", reviewer, ok)
	}

	p.ReviewFindings = append(p.ReviewFindings,
		proposal.ReviewFinding{Reviewer: "second-reviewer", Verdict: proposal.ReviewVerdictReject, TargetDigest: digest},
	)
	if _, ok := independentApprovalReviewer(p, targetHead); ok {
		t.Fatal("an unresolved independent rejection did not block the merge")
	}

	p.ReviewFindings = []proposal.ReviewFinding{
		{Reviewer: "reviewer", Verdict: proposal.ReviewVerdictApprove, TargetDigest: digest},
	}
	if _, ok := independentApprovalReviewer(p, "moved-target-head"); ok {
		t.Fatal("approval remained valid after the protected target moved")
	}
}

func TestMemoryContextAtUsesPolicyAndPinsManifest(t *testing.T) {
	var gotRequest lethe.MemoryContextRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/memory/allowed/context" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(w, `{"project_id":"allowed","ref_name":"refs/shared/main","head_changeset_id":"head","manifest_id":"manifest","projection_version":"memory-context/v1","total_active":1,"memories":[{"memory_id":"m1","content":"accepted","status":"active","source":"memory_git"}]}`)
	}))
	defer upstream.Close()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := lethe.NewClient(upstream.URL, "key", time.Second, 1<<20)
	server := NewServer(&config.Config{MaxResultCount: 100}, client, ledger.NewService(db), proposal.NewService(db, client), idempotency.NewService(db), audit.NewService(db))
	principal := auth.Principal{
		ID: "principal-chatgpt", AgentID: "chatgpt",
		Projects: map[string]struct{}{"allowed": {}},
		Scopes:   map[string]struct{}{"memory.read": {}, "memory.commit": {}},
	}
	ctx := WithAuth(context.Background(), principal, "req-context")
	request := markmcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{
		"project": "allowed", "query": "accepted", "limit": float64(5), "session_id": "sess-1",
	}
	result, err := server.handleMemoryContextAt(ctx, request)
	if err != nil || result == nil {
		t.Fatalf("memory context = %#v, %v", result, err)
	}
	if gotRequest.RefName != "refs/shared/main" || !gotRequest.CreateManifest ||
		gotRequest.ActorID != principal.ID || gotRequest.SessionID != "sess-1" {
		t.Fatalf("upstream request = %#v", gotRequest)
	}

	wildcardCtx := WithAuth(context.Background(), auth.Principal{
		ID: "wildcard-reader", Projects: map[string]struct{}{"*": {}},
		Scopes: map[string]struct{}{"memory.read": {}},
	}, "req-context-wildcard")
	gotRequest = lethe.MemoryContextRequest{}
	request.Params.Arguments = map[string]interface{}{"project": "allowed"}
	if _, err := server.handleMemoryContextAt(wildcardCtx, request); err != nil {
		t.Fatalf("wildcard read context failed: %v", err)
	}
	if gotRequest.CreateManifest {
		t.Fatal("read-only wildcard principal created a manifest")
	}

	request.Params.Arguments = map[string]interface{}{
		"project": "allowed", "ref": "refs/agents/archimedes/main",
	}
	if _, err := server.handleMemoryContextAt(ctx, request); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("unowned context ref should be denied, got %v", err)
	}
}

func TestGetIntSliceRejectsFractionalNegativeAndDuplicateOrdinals(t *testing.T) {
	for name, value := range map[string][]interface{}{
		"fractional": {float64(0.9)},
		"negative":   {float64(-1)},
		"duplicate":  {float64(1), float64(1)},
		"string":     {"1"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := getIntSlice(map[string]interface{}{"selected_ops": value}, "selected_ops"); err == nil {
				t.Fatalf("expected invalid selected_ops %v to fail", value)
			}
		})
	}

	got, err := getIntSlice(map[string]interface{}{"selected_ops": []interface{}{float64(0), float64(2)}}, "selected_ops")
	if err != nil {
		t.Fatalf("valid selected_ops failed: %v", err)
	}
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("selected_ops = %v, want [0 2]", got)
	}
}

func TestMemoryDiffRejectsCrossProjectBaseBeforeDiff(t *testing.T) {
	diffCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/memory/changesets/target":
			fmt.Fprint(w, `{"changeset_id":"target","project_id":"allowed","parent_ids":[],"ops":[]}`)
		case "/api/memory/changesets/base":
			fmt.Fprint(w, `{"changeset_id":"base","project_id":"secret","parent_ids":[],"ops":[]}`)
		case "/api/memory/changesets/target/diff":
			diffCalled = true
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := lethe.NewClient(upstream.URL, "key", time.Second, 1<<20)
	server := NewServer(&config.Config{MaxResultCount: 100}, client, ledger.NewService(db), proposal.NewService(db, client), idempotency.NewService(db), audit.NewService(db))
	request := markmcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{"target_changeset_id": "target", "base_changeset_id": "base"}
	ctx := WithAuth(context.Background(), auth.Principal{
		ID: "reader", Projects: map[string]struct{}{"allowed": {}}, Scopes: map[string]struct{}{"memory.read": {}},
	}, "req-diff")
	_, err = server.handleMemoryDiff(ctx, request)
	if err == nil || !strings.Contains(err.Error(), "same project") {
		t.Fatalf("cross-project base should be rejected, got %v", err)
	}
	if diffCalled {
		t.Fatal("cross-project diff reached upstream diff endpoint")
	}
}

func TestMemoryChangesetCreateIdempotencyStrictMatching(t *testing.T) {
	createCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/memory/lethe/refs":
			fmt.Fprint(w, `[{"project_id":"lethe","ref_name":"refs/agents/chatgpt/main","head_changeset_id":"head-1","protected":false}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/memory/changesets":
			createCalls++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"changeset_id":"cs-1","project_id":"lethe","ref_name":"refs/agents/chatgpt/main","parent_ids":["head-1"],"ops":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/memory/changesets/cs-1":
			fmt.Fprint(w, `{"changeset_id":"cs-1","project_id":"lethe","ref_name":"refs/agents/chatgpt/main","parent_ids":["head-1"],"ops":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := lethe.NewClient(upstream.URL, "key", time.Second, 1<<20)
	server := NewServer(&config.Config{MaxResultCount: 100}, client, ledger.NewService(db), proposal.NewService(db, client), idempotency.NewService(db), audit.NewService(db))

	ctx := WithAuth(context.Background(), auth.Principal{
		ID: "principal-chatgpt", AgentID: "chatgpt", Projects: map[string]struct{}{"lethe": {}},
		Scopes: map[string]struct{}{"memory.commit": {}},
	}, "req-commit-idem")
	newRequest := func(message string) markmcp.CallToolRequest {
		request := markmcp.CallToolRequest{}
		request.Params.Arguments = map[string]interface{}{
			"project": "lethe", "ref": "refs/agents/chatgpt/main", "message": message,
			"idempotency_key": "commit-idem", "expected_head": "head-1",
			"ops": []interface{}{map[string]interface{}{"op_type": "add_memory", "payload": map[string]interface{}{"content": "note"}}},
		}
		return request
	}

	first, err := server.handleMemoryChangesetCreate(ctx, newRequest("first"))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("upstream creates = %d, want 1", createCalls)
	}

	// Identical replay returns the original changeset without a second write.
	replay, err := server.handleMemoryChangesetCreate(ctx, newRequest("first"))
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("identical replay reached upstream create %d times, want 1", createCalls)
	}
	firstJSON, _ := json.Marshal(first)
	replayJSON, _ := json.Marshal(replay)
	if string(firstJSON) != string(replayJSON) {
		t.Fatalf("replay result differs:\nfirst:  %s\nreplay: %s", firstJSON, replayJSON)
	}

	// Same key with a different request is rejected locally.
	_, err = server.handleMemoryChangesetCreate(ctx, newRequest("different"))
	if err == nil || !strings.Contains(err.Error(), "different input") {
		t.Fatalf("mismatched replay should be rejected, got %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("mismatched replay reached upstream create %d times, want 1", createCalls)
	}
}

func TestMemoryChangesetCreateRetryAfterCrashReplaysUpstream(t *testing.T) {
	// Upstream is stateful: the ref head advances after the first create, and
	// a replayed idempotency key returns the original changeset (mirroring
	// Lethe's strict idempotency).
	head := "head-1"
	createCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/memory/lethe/refs":
			fmt.Fprintf(w, `[{"project_id":"lethe","ref_name":"refs/agents/chatgpt/main","head_changeset_id":%q,"protected":false}]`, head)
		case r.Method == http.MethodPost && r.URL.Path == "/api/memory/changesets":
			createCalls++
			head = "cs-1"
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"changeset_id":"cs-1","project_id":"lethe","ref_name":"refs/agents/chatgpt/main","parent_ids":["head-1"],"ops":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/memory/changesets/cs-1":
			fmt.Fprint(w, `{"changeset_id":"cs-1","project_id":"lethe","ref_name":"refs/agents/chatgpt/main","parent_ids":["head-1"],"ops":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	newServer := func(t *testing.T) *Server {
		db, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		client := lethe.NewClient(upstream.URL, "key", time.Second, 1<<20)
		return NewServer(&config.Config{MaxResultCount: 100}, client, ledger.NewService(db), proposal.NewService(db, client), idempotency.NewService(db), audit.NewService(db))
	}
	ctx := WithAuth(context.Background(), auth.Principal{
		ID: "principal-chatgpt", AgentID: "chatgpt", Projects: map[string]struct{}{"lethe": {}},
		Scopes: map[string]struct{}{"memory.commit": {}},
	}, "req-crash")
	newRequest := func() markmcp.CallToolRequest {
		request := markmcp.CallToolRequest{}
		request.Params.Arguments = map[string]interface{}{
			"project": "lethe", "ref": "refs/agents/chatgpt/main", "message": "note",
			"idempotency_key": "commit-crash", "expected_head": "head-1",
			"ops": []interface{}{map[string]interface{}{"op_type": "add_memory", "payload": map[string]interface{}{"content": "note"}}},
		}
		return request
	}

	// First call succeeds; upstream head advances to cs-1.
	if _, err := newServer(t).handleMemoryChangesetCreate(ctx, newRequest()); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("upstream creates = %d, want 1", createCalls)
	}

	// Simulate a crash before the idempotency record persisted: a fresh
	// Charon store retries the exact request. The upstream replay must win
	// over the moved head — no compare-and-swap failure.
	retry, err := newServer(t).handleMemoryChangesetCreate(ctx, newRequest())
	if err != nil {
		t.Fatalf("post-crash retry should replay upstream idempotency, got %v", err)
	}
	if retry == nil {
		t.Fatal("post-crash retry returned no result")
	}
	if createCalls != 2 {
		t.Fatalf("retry never reached upstream replay, creates = %d, want 2", createCalls)
	}
}

func TestMemoryMergeRetryAfterCrashRecoversPendingProposal(t *testing.T) {
	// Crash state: Lethe's protected ref already advanced to the persisted
	// merge result, but Charon never recorded the idempotency record nor
	// completed the proposal. The review approval is bound to the pre-merge
	// target head T0.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/memory/lethe/refs":
			fmt.Fprint(w, `[{"project_id":"lethe","ref_name":"refs/shared/main","head_changeset_id":"merge-cs","protected":true}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/memory/changesets/merge-cs":
			fmt.Fprint(w, `{"changeset_id":"merge-cs","project_id":"lethe","ref_name":"refs/shared/main","parent_ids":["T0","head"],"ops":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/memory/changesets/head":
			fmt.Fprint(w, `{"changeset_id":"head","project_id":"lethe","ref_name":"refs/agents/chatgpt/main","parent_ids":["T0"],"ops":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := lethe.NewClient(upstream.URL, "key", time.Second, 1<<20)
	proposalSvc := proposal.NewService(db, client)
	server := NewServer(&config.Config{MaxResultCount: 100}, client, ledger.NewService(db), proposalSvc, idempotency.NewService(db), audit.NewService(db))

	p, err := proposalSvc.Create(context.Background(), proposal.CreateRequest{
		ProjectID: "lethe", EventType: "merge_request", Kind: "merge_request",
		Provenance: proposal.Provenance{Principal: "author"}, IdempotencyKey: "proposal-crash-1",
		BaseChangesetID: "T0", HeadChangesetID: "head", TargetRef: "refs/shared/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proposalSvc.AddReviewFinding(context.Background(), p.ID, proposal.ReviewFinding{
		Reviewer: "reviewer", Verdict: proposal.ReviewVerdictApprove, Comment: "approved at T0",
		TargetHeadChangesetID: "T0",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proposalSvc.SetMergeResult(context.Background(), p.ID, "merge-cs"); err != nil {
		t.Fatal(err)
	}

	request := markmcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{"proposal_id": p.ID, "idempotency_key": "merge-crash-1"}
	ctx := WithAuth(context.Background(), auth.Principal{
		ID: "merger", AgentID: "merger", Projects: map[string]struct{}{"lethe": {}},
		Scopes: map[string]struct{}{"memory.merge": {}},
	}, "req-merge-crash")

	result, err := server.handleMemoryMerge(ctx, request)
	if err != nil {
		t.Fatalf("post-crash merge retry should recover, got %v", err)
	}
	if result == nil {
		t.Fatal("post-crash merge retry returned no result")
	}
	got, err := proposalSvc.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != proposal.StatusApproved {
		t.Fatalf("proposal status = %s, want approved", got.Status)
	}
}

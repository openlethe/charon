package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	markmcp "github.com/mark3labs/mcp-go/mcp"
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
	return NewServer(&config.Config{MaxResultCount: 100}, nil, ledger.NewService(db), proposalSvc, idempotency.NewService(db)), proposalSvc
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

func TestMemoryMergeRequiresIndependentReview(t *testing.T) {
	p := &proposal.Proposal{OriginatingPrincipal: "author", ReviewFindings: []proposal.ReviewFinding{{Reviewer: "author", Comment: "self review"}}}
	if hasIndependentReview(p) {
		t.Fatal("originator-authored finding counted as independent review")
	}
	p.ReviewFindings = append(p.ReviewFindings, proposal.ReviewFinding{Reviewer: "reviewer", Comment: "independent review"})
	if !hasIndependentReview(p) {
		t.Fatal("independent reviewer finding was not recognized")
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
	server := NewServer(&config.Config{MaxResultCount: 100}, client, ledger.NewService(db), proposal.NewService(db, client), idempotency.NewService(db))
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

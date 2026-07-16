package lethe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMemoryGitRefPathsPreserveSlashNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/memory/project/refs":
			fmt.Fprint(w, `[{"project_id":"project","ref_name":"refs/shared/main","head_changeset_id":"head","protected":true},{"project_id":"project","ref_name":"refs/agents/a/main","head_changeset_id":"head","protected":false}]`)
		case "/api/memory/project/refs/advance":
			var req CASAdvanceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.RefName != "refs/agents/a/main" {
				t.Fatalf("ref_name = %q", req.RefName)
			}
			fmt.Fprint(w, `{"project_id":"project","ref_name":"refs/agents/a/main","head_changeset_id":"new","protected":false}`)
		case "/api/memory/project/refs/merge":
			var req MergeAdvanceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.RefName != "refs/shared/main" || req.MergeProposalID == "" || req.ReviewerPrincipal == "" || req.Authorization == "" {
				t.Fatalf("merge request = %#v", req)
			}
			fmt.Fprint(w, `{"project_id":"project","ref_name":"refs/shared/main","head_changeset_id":"merged","protected":true}`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "key", time.Second, 1<<20)
	client.SetMergeSigningKey("0123456789abcdef0123456789abcdef")
	ref, err := client.GetRef(context.Background(), "project", "refs/shared/main")
	if err != nil || ref == nil || ref.HeadChangesetID != "head" {
		t.Fatalf("GetRef = %#v, %v", ref, err)
	}
	advanced, err := client.CASAdvanceRef(context.Background(), "project", "refs/agents/a/main", CASAdvanceRequest{ExpectedHead: "head", NewHead: "new"})
	if err != nil || advanced.HeadChangesetID != "new" {
		t.Fatalf("CASAdvanceRef = %#v, %v", advanced, err)
	}
	merged, err := client.MergeAdvanceRef(context.Background(), "project", "refs/shared/main", MergeAdvanceRequest{
		ExpectedHead: "new", NewHead: "merged", MergeProposalID: "proposal-1", ReviewerPrincipal: "reviewer",
		ProposalDigest:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		MergerPrincipal: "merger", Strategy: StrategyFastForward,
	})
	if err != nil || merged.HeadChangesetID != "merged" {
		t.Fatalf("MergeAdvanceRef = %#v, %v", merged, err)
	}
}

func TestEnsureLegacyRootEscapesExactProjectID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/memory/team%2Fblue%3Fprod/legacy-root" {
			t.Fatalf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		fmt.Fprint(w, `{"changeset":{"changeset_id":"root"},"ref":{"project_id":"team/blue?prod","ref_name":"refs/shared/main","head_changeset_id":"root","protected":true}}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", time.Second, 1<<20)
	root, err := client.EnsureLegacyRoot(context.Background(), "team/blue?prod")
	if err != nil || root == nil || root.Ref == nil || root.Ref.ProjectID != "team/blue?prod" {
		t.Fatalf("EnsureLegacyRoot = %#v, %v", root, err)
	}
}

func TestBranchAndRefPathsEscapeExactProjectID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.EscapedPath() == "/api/memory/team%2Fblue%3Fprod/branches":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"project_id":"team/blue?prod","ref_name":"refs/sessions/principal/run","head_changeset_id":"root","protected":false}`)
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/api/memory/team%2Fblue%3Fprod/refs":
			fmt.Fprint(w, `[{"project_id":"team/blue?prod","ref_name":"refs/shared/main","head_changeset_id":"root","protected":true}]`)
		default:
			t.Fatalf("unexpected request = %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", time.Second, 1<<20)
	branch, err := client.CreateBranch(context.Background(), "team/blue?prod", CreateBranchRequest{
		RefName: "refs/sessions/principal/run", HeadChangesetID: "root", Principal: "principal",
	})
	if err != nil || branch == nil || branch.ProjectID != "team/blue?prod" {
		t.Fatalf("CreateBranch = %#v, %v", branch, err)
	}
	refs, err := client.ListRefs(context.Background(), "team/blue?prod")
	if err != nil || len(refs) != 1 || refs[0].ProjectID != "team/blue?prod" {
		t.Fatalf("ListRefs = %#v, %v", refs, err)
	}
}

func TestGetMemoryContextContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/memory/project/context" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var req MemoryContextRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.RefName != "refs/shared/main" || !req.CreateManifest || req.ActorID != "principal" {
			t.Fatalf("request = %#v", req)
		}
		fmt.Fprint(w, `{"project_id":"project","ref_name":"refs/shared/main","head_changeset_id":"head","manifest_id":"manifest","projection_version":"memory-context/v1","total_active":1,"memories":[{"memory_id":"m1","content":"accepted","status":"active","source":"memory_git"}]}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "key", time.Second, 1<<20)
	view, err := client.GetMemoryContext(context.Background(), "project", MemoryContextRequest{
		RefName: "refs/shared/main", ActorID: "principal", CreateManifest: true,
	})
	if err != nil || view.ManifestID != "manifest" || len(view.Memories) != 1 {
		t.Fatalf("GetMemoryContext = %#v, %v", view, err)
	}
}

func TestDetectConflictsContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/memory/project/conflicts/detect" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"conflicts":[{"conflict_id":"c1","project_id":"project","conflict_type":"stale_base","severity":"warning","status":"open"}]}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "key", time.Second, 1<<20)
	conflicts, err := client.DetectConflicts(context.Background(), "project", DetectConflictsRequest{BaseChangesetID: "base", LeftChangesetID: "left", RightChangesetID: "right"})
	if err != nil || len(conflicts) != 1 || conflicts[0].ConflictID != "c1" {
		t.Fatalf("DetectConflicts = %#v, %v", conflicts, err)
	}
}

func TestMemoryGitIdentifierPathsAreEscaped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/memory/changesets/change%2Fset%3Fprod":
			fmt.Fprint(w, `{"changeset_id":"change/set?prod","project_id":"project","parent_ids":[],"ops":[]}`)
		case "/api/memory/changesets/change%2Fset%3Fprod/diff":
			fmt.Fprint(w, `{"project_id":"project","target_changeset_id":"change/set?prod"}`)
		case "/api/memory/manifests/manifest%2Fid%3Fprod":
			fmt.Fprint(w, `{"manifest_id":"manifest/id?prod","project_id":"project"}`)
		default:
			t.Fatalf("unexpected escaped path %q", r.URL.EscapedPath())
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", time.Second, 1<<20)
	if _, err := client.GetChangeset(context.Background(), "change/set?prod"); err != nil {
		t.Fatalf("GetChangeset: %v", err)
	}
	if _, err := client.DiffChangeset(context.Background(), "change/set?prod", DiffRequest{}); err != nil {
		t.Fatalf("DiffChangeset: %v", err)
	}
	if _, err := client.GetManifest(context.Background(), "manifest/id?prod"); err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
}

func TestMemoryGitResponsesRespectBodyLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"project_id":"project","target_changeset_id":"target","kind_notes":[{"kind":"%s"}]}`, strings.Repeat("x", 256))
	}))
	defer server.Close()

	client := NewClient(server.URL, "key", time.Second, 64)
	if _, err := client.DiffChangeset(context.Background(), "target", DiffRequest{}); err == nil || !strings.Contains(err.Error(), "exceeds 64 bytes") {
		t.Fatalf("oversized diff response error = %v", err)
	}
}

package lethe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	})
	if err != nil || merged.HeadChangesetID != "merged" {
		t.Fatalf("MergeAdvanceRef = %#v, %v", merged, err)
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

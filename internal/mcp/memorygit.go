package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/openlethe/charon/internal/auth"
	"github.com/openlethe/charon/internal/policy"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

// registerMemoryGitTools adds Memory Git V1 MCP tools to the server.
func (s *Server) registerMemoryGitTools() {
	statusTool := mcp.NewTool("memory_status",
		mcp.WithDescription("Show project memory ref heads and principal capabilities"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithReadOnlyHintAnnotation(true),
	)
	logTool := mcp.NewTool("memory_log",
		mcp.WithDescription("Show changeset log for a project ref"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithString("ref", mcp.Required(), mcp.Description("Ref name (e.g., refs/shared/main)")),
		mcp.WithNumber("limit", mcp.Description("Maximum results (default 20, max 100)"), mcp.Max(100)),
		mcp.WithReadOnlyHintAnnotation(true),
	)
	showTool := mcp.NewTool("memory_show",
		mcp.WithDescription("Show a single changeset by ID"),
		mcp.WithString("id", mcp.Required(), mcp.Description("Changeset ID")),
		mcp.WithReadOnlyHintAnnotation(true),
	)
	branchTool := mcp.NewTool("memory_branch_create",
		mcp.WithDescription("Create a branch from an expected head changeset"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithString("ref_name", mcp.Required(), mcp.Description("New ref name (e.g., refs/sessions/chatgpt/s1)")),
		mcp.WithString("head_changeset_id", mcp.Required(), mcp.Description("Head changeset ID to start from")),
	)
	diffTool := mcp.NewTool("memory_diff",
		mcp.WithDescription("Compute a deterministic semantic diff between two changesets"),
		mcp.WithString("target_changeset_id", mcp.Required(), mcp.Description("Target changeset ID")),
		mcp.WithString("base_changeset_id", mcp.Description("Base changeset ID (optional, defaults to first parent)")),
		mcp.WithReadOnlyHintAnnotation(true),
	)
	changesetTool := mcp.NewTool("memory_changeset_create",
		mcp.WithDescription("Commit a changeset to an authorized non-protected ref"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithString("ref", mcp.Required(), mcp.Description("Ref name to commit to")),
		mcp.WithString("message", mcp.Required(), mcp.Description("Commit message")),
		mcp.WithArray("parent_ids", mcp.Description("Parent changeset IDs"), mcp.WithStringItems()),
		mcp.WithArray("ops", mcp.Required(), mcp.Description("Semantic operations JSON array")),
		mcp.WithString("expected_head", mcp.Required(), mcp.Description("Current ref head for compare-and-swap")),
		mcp.WithString("idempotency_key", mcp.Required(), mcp.Description("Client-generated unique key")),
	)
	mergeProposeTool := mcp.NewTool("memory_merge_propose",
		mcp.WithDescription("Propose a merge into a protected ref (e.g., refs/shared/main)"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithString("target_ref", mcp.Required(), mcp.Description("Target ref to merge into")),
		mcp.WithString("head_changeset_id", mcp.Required(), mcp.Description("Head changeset to merge")),
		mcp.WithString("base_changeset_id", mcp.Description("Base changeset ID (for conflict detection)")),
		mcp.WithString("message", mcp.Description("Merge proposal message")),
		mcp.WithArray("selected_ops", mcp.Description("Optional operation ordinals for cherry-pick"), mcp.WithNumberItems()),
		mcp.WithString("idempotency_key", mcp.Required(), mcp.Description("Client-generated unique key")),
	)
	mergeReviewTool := mcp.NewTool("memory_merge_review",
		mcp.WithDescription("Attach an attributed review finding to a Memory Git merge proposal"),
		mcp.WithString("proposal_id", mcp.Required(), mcp.Description("Charon proposal ID")),
		mcp.WithString("comment", mcp.Required(), mcp.Description("Review comment or finding")),
		mcp.WithString("severity", mcp.Description("Optional severity")),
	)
	mergeTool := mcp.NewTool("memory_merge",
		mcp.WithDescription("Approve and apply a reviewed merge proposal using protected-ref CAS"),
		mcp.WithString("proposal_id", mcp.Required(), mcp.Description("Charon proposal ID")),
		mcp.WithString("idempotency_key", mcp.Required(), mcp.Description("Unique merge operation key")),
	)
	mergeCancelTool := mcp.NewTool("memory_merge_cancel",
		mcp.WithDescription("Cancel an originating principal's pending merge proposal without deleting history"),
		mcp.WithString("proposal_id", mcp.Required(), mcp.Description("Charon proposal ID")),
	)
	refListTool := mcp.NewTool("memory_ref_list",
		mcp.WithDescription("List refs in authorized projects"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.mcpServer.AddTool(statusTool, s.handleMemoryStatus)
	s.mcpServer.AddTool(logTool, s.handleMemoryLog)
	s.mcpServer.AddTool(showTool, s.handleMemoryShow)
	s.mcpServer.AddTool(branchTool, s.handleMemoryBranchCreate)
	s.mcpServer.AddTool(diffTool, s.handleMemoryDiff)
	s.mcpServer.AddTool(changesetTool, s.handleMemoryChangesetCreate)
	s.mcpServer.AddTool(mergeProposeTool, s.handleMemoryMergePropose)
	s.mcpServer.AddTool(mergeReviewTool, s.handleMemoryMergeReview)
	s.mcpServer.AddTool(mergeTool, s.handleMemoryMerge)
	s.mcpServer.AddTool(mergeCancelTool, s.handleMemoryMergeCancel)
	s.mcpServer.AddTool(refListTool, s.handleMemoryRefList)
}

func (s *Server) handleMemoryStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.read"); err != nil {
		s.recordLedger(reqID, principal, "memory_status", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	project := getString(args, "project")
	if err := policy.RequireReadProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_status", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	refs, err := s.lethe.ListRefs(ctx, project)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_status", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("status failed: %w", err)
	}
	resp := map[string]interface{}{"project": project, "refs": refs, "scopes": principal.Scopes}
	data, _ := json.MarshalIndent(resp, "", "  ")
	s.recordLedger(reqID, principal, "memory_status", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryLog(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.read"); err != nil {
		s.recordLedger(reqID, principal, "memory_log", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	project := getString(args, "project")
	ref := getString(args, "ref")
	if err := policy.RequireReadProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_log", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	if limit > 100 {
		limit = 100
	}
	log, err := s.lethe.ListChangesets(ctx, project, ref, limit)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_log", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("log failed: %w", err)
	}
	resp := map[string]interface{}{"project": project, "ref": ref, "changesets": log, "count": len(log)}
	data, _ := json.MarshalIndent(resp, "", "  ")
	s.recordLedger(reqID, principal, "memory_log", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryShow(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.read"); err != nil {
		s.recordLedger(reqID, principal, "memory_show", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	id := getString(request.GetArguments(), "id")
	if id == "" {
		return nil, fmt.Errorf("missing required argument: id")
	}
	cs, err := s.lethe.GetChangeset(ctx, id)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_show", nil, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("show failed: %w", err)
	}
	if err := policy.RequireReadProject(principal, cs.ProjectID); err != nil {
		s.recordLedger(reqID, principal, "memory_show", []string{cs.ProjectID}, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	data, _ := json.MarshalIndent(cs, "", "  ")
	s.recordLedger(reqID, principal, "memory_show", []string{cs.ProjectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryBranchCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.branch"); err != nil {
		s.recordLedger(reqID, principal, "memory_branch_create", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	project := getString(args, "project")
	if err := policy.RequireProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_branch_create", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	refName := getString(args, "ref_name")
	if err := s.requireOwnedMemoryRef(ctx, principal, project, refName, true); err != nil {
		s.recordLedger(reqID, principal, "memory_branch_create", []string{project}, "denied: ref ownership", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	headID := getString(args, "head_changeset_id")
	head, err := s.lethe.GetChangeset(ctx, headID)
	if err != nil || head.ProjectID != project {
		return nil, fmt.Errorf("branch head does not belong to project %s", project)
	}
	ref, err := s.lethe.CreateBranch(ctx, project, lethe.CreateBranchRequest{
		RefName:         refName,
		HeadChangesetID: headID,
		Principal:       principal.ID,
	})
	if err != nil {
		s.recordLedger(reqID, principal, "memory_branch_create", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("branch create failed: %w", err)
	}
	data, _ := json.MarshalIndent(ref, "", "  ")
	s.recordLedger(reqID, principal, "memory_branch_create", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryDiff(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.read"); err != nil {
		s.recordLedger(reqID, principal, "memory_diff", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	targetID := getString(args, "target_changeset_id")
	baseID := getString(args, "base_changeset_id")
	if targetID == "" {
		return nil, fmt.Errorf("missing required argument: target_changeset_id")
	}
	// Verify target project access
	cs, err := s.lethe.GetChangeset(ctx, targetID)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_diff", nil, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("diff failed: %w", err)
	}
	if err := policy.RequireReadProject(principal, cs.ProjectID); err != nil {
		s.recordLedger(reqID, principal, "memory_diff", []string{cs.ProjectID}, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	if baseID != "" {
		base, err := s.lethe.GetChangeset(ctx, baseID)
		if err != nil {
			return nil, fmt.Errorf("diff failed: %w", err)
		}
		if base.ProjectID != cs.ProjectID {
			return nil, fmt.Errorf("diff base and target must belong to the same project")
		}
		if err := policy.RequireReadProject(principal, base.ProjectID); err != nil {
			return nil, fmt.Errorf("unauthorized: %w", err)
		}
	}
	diff, err := s.lethe.DiffChangeset(ctx, targetID, lethe.DiffRequest{BaseChangesetID: baseID})
	if err != nil {
		s.recordLedger(reqID, principal, "memory_diff", []string{cs.ProjectID}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("diff failed: %w", err)
	}
	data, _ := json.MarshalIndent(diff, "", "  ")
	s.recordLedger(reqID, principal, "memory_diff", []string{cs.ProjectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryChangesetCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.commit"); err != nil {
		s.recordLedger(reqID, principal, "memory_changeset_create", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	project := getString(args, "project")
	if err := policy.RequireProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_changeset_create", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	// Ensure ref is not protected (models cannot directly manipulate shared refs)
	refName := getString(args, "ref")
	if refName == "" {
		return nil, fmt.Errorf("missing required argument: ref")
	}
	if refName == "refs/shared/main" || len(refName) > len("refs/shared/") && refName[:len("refs/shared/")] == "refs/shared/" {
		s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "denied: protected ref", time.Since(start))
		return nil, fmt.Errorf("unauthorized: cannot commit directly to protected ref %s", refName)
	}
	if err := s.requireOwnedMemoryRef(ctx, principal, project, refName, false); err != nil {
		s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "denied: ref ownership", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	expectedHead := getString(args, "expected_head")
	if expectedHead == "" {
		return nil, fmt.Errorf("missing required argument: expected_head")
	}
	ref, err := s.lethe.GetRef(ctx, project, refName)
	if err != nil {
		return nil, fmt.Errorf("load ref: %w", err)
	}
	if ref == nil {
		return nil, fmt.Errorf("ref does not exist; create it with memory_branch_create")
	}
	if ref.HeadChangesetID != expectedHead {
		return nil, fmt.Errorf("ref compare-and-swap conflict: expected %s, current %s", expectedHead, ref.HeadChangesetID)
	}

	var ops []lethe.SemanticOp
	if opsArr, ok := args["ops"].([]interface{}); ok {
		for _, v := range opsArr {
			if m, ok := v.(map[string]interface{}); ok {
				op := lethe.SemanticOp{Payload: map[string]interface{}{}}
				if t, ok := m["op_type"].(string); ok {
					op.OpType = t
				}
				if t, ok := m["target_event_id"].(string); ok {
					op.TargetEventID = t
				}
				if t, ok := m["resulting_event_id"].(string); ok {
					op.ResultingEventID = t
				}
				if p, ok := m["payload"].(map[string]interface{}); ok {
					op.Payload = p
				}
				ops = append(ops, op)
			}
		}
	}

	var parents []string
	if pArr, ok := args["parent_ids"].([]interface{}); ok {
		for _, v := range pArr {
			if s, ok := v.(string); ok {
				parents = append(parents, s)
			}
		}
	}
	if len(parents) == 0 {
		parents = []string{expectedHead}
	} else if parents[0] != expectedHead {
		return nil, fmt.Errorf("first parent must match expected_head")
	}

	cs, err := s.lethe.CreateChangeset(ctx, lethe.CreateChangesetRequest{
		ProjectID:          project,
		RefName:            refName,
		ParentIDs:          parents,
		AuthorPrincipal:    principal.ID,
		ActorID:            principal.AgentID,
		Message:            getString(args, "message"),
		IdempotencyKey:     getString(args, "idempotency_key"),
		Ops:                ops,
		ExpectedHead:       expectedHead,
		AdvanceRef:         true,
		CreateRefIfMissing: false,
	})
	if err != nil {
		s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("changeset create failed: %w", err)
	}
	data, _ := json.MarshalIndent(cs, "", "  ")
	s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryMergePropose(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.propose"); err != nil {
		s.recordLedger(reqID, principal, "memory_merge_propose", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	project := getString(args, "project")
	if err := policy.RequireProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_merge_propose", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	// Merge proposals target protected shared refs. Owned non-protected refs use
	// the normal CAS commit path.
	targetRef := getString(args, "target_ref")
	if targetRef != "refs/shared/main" && !strings.HasPrefix(targetRef, "refs/shared/") {
		return nil, fmt.Errorf("merge proposals must target a protected shared ref")
	}

	headID := getString(args, "head_changeset_id")
	baseID := getString(args, "base_changeset_id")
	idempotencyKey := getString(args, "idempotency_key")
	if headID == "" || idempotencyKey == "" {
		return nil, fmt.Errorf("missing required arguments: head_changeset_id and idempotency_key")
	}
	head, err := s.lethe.GetChangeset(ctx, headID)
	if err != nil || head.ProjectID != project {
		return nil, fmt.Errorf("merge head does not belong to project %s", project)
	}
	target, err := s.lethe.GetRef(ctx, project, targetRef)
	if err != nil {
		return nil, fmt.Errorf("load target ref: %w", err)
	}
	if target == nil || !target.Protected {
		return nil, fmt.Errorf("target ref is missing or not protected")
	}
	if baseID == "" {
		baseID = target.HeadChangesetID
	}
	base, err := s.lethe.GetChangeset(ctx, baseID)
	if err != nil || base.ProjectID != project {
		return nil, fmt.Errorf("merge base does not belong to project %s", project)
	}
	selectedOps := getIntSlice(args, "selected_ops")
	for _, ordinal := range selectedOps {
		if ordinal < 0 || ordinal >= len(head.Ops) {
			return nil, fmt.Errorf("selected operation ordinal %d is out of range", ordinal)
		}
	}
	var conflictIDs []string
	if baseID != target.HeadChangesetID {
		conflicts, err := s.lethe.DetectConflicts(ctx, project, lethe.DetectConflictsRequest{
			BaseChangesetID:  baseID,
			LeftChangesetID:  target.HeadChangesetID,
			RightChangesetID: headID,
		})
		if err != nil {
			return nil, fmt.Errorf("conflict detection failed: %w", err)
		}
		for _, conflict := range conflicts {
			conflictIDs = append(conflictIDs, conflict.ConflictID)
		}
	}

	// Check idempotency
	existing, err := s.checkIdempotency(ctx, principal.ID, "memory_merge_propose", idempotencyKey, args)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		data, _ := json.MarshalIndent(map[string]interface{}{"proposal_id": existing.ResultingResourceID, "existing": true}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	// Create proposal via Charon proposal store
	prop, err := s.proposalSvc.Create(ctx, proposal.CreateRequest{
		ProjectID: project,
		EventType: "merge_request",
		Kind:      "merge_request",
		Content:   getString(args, "message"),
		Provenance: proposal.Provenance{
			Principal:     principal.ID,
			Session:       reqID,
			CaptureMethod: "memory_merge_propose",
			AuthorType:    "model",
		},
		IdempotencyKey:  idempotencyKey,
		RequestID:       reqID,
		BaseChangesetID: baseID,
		HeadChangesetID: headID,
		TargetRef:       targetRef,
		SelectedOps:     selectedOps,
		ConflictIDs:     conflictIDs,
	})
	if err != nil {
		s.recordLedger(reqID, principal, "memory_merge_propose", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("merge propose failed: %w", err)
	}

	// Store idempotency
	existingIdem, err := s.storeIdempotency(ctx, principal.ID, "memory_merge_propose", idempotencyKey, args, "pending", prop.ID)
	if err != nil {
		return nil, err
	}
	if existingIdem != nil {
		data, _ := json.MarshalIndent(map[string]interface{}{"proposal_id": existingIdem.ResultingResourceID, "existing": true}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	data, _ := json.MarshalIndent(map[string]interface{}{
		"proposal_id": prop.ID, "status": prop.Status, "target_ref": targetRef,
		"base_changeset_id": baseID, "head_changeset_id": headID, "conflict_ids": conflictIDs,
	}, "", "  ")
	s.recordLedger(reqID, principal, "memory_merge_propose", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryMergeReview(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.review"); err != nil {
		s.recordLedger(reqID, principal, "memory_merge_review", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	proposalID := getString(args, "proposal_id")
	p, err := s.proposalSvc.Get(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if err := policy.RequireProject(principal, p.ProjectID); err != nil {
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	p, err = s.proposalSvc.AddReviewFinding(ctx, proposalID, proposal.ReviewFinding{
		Reviewer: principal.ID,
		Comment:  getString(args, "comment"),
		Severity: getString(args, "severity"),
	})
	if err != nil {
		return nil, err
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	s.recordLedger(reqID, principal, "memory_merge_review", []string{p.ProjectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryMerge(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.merge"); err != nil {
		s.recordLedger(reqID, principal, "memory_merge", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	proposalID := getString(args, "proposal_id")
	idempotencyKey := getString(args, "idempotency_key")
	if proposalID == "" || idempotencyKey == "" {
		return nil, fmt.Errorf("proposal_id and idempotency_key required")
	}
	existing, err := s.checkIdempotency(ctx, principal.ID, "memory_merge", idempotencyKey, args)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		p, err := s.proposalSvc.Get(ctx, existing.ResultingResourceID)
		if err != nil {
			return nil, err
		}
		if p.Status == proposal.StatusPending && p.ChangesetID != "" {
			p, err = s.proposalSvc.CompleteMerge(ctx, p.ID, principal.ID, p.ChangesetID)
			if err != nil {
				return nil, err
			}
		}
		data, _ := json.MarshalIndent(map[string]interface{}{
			"proposal_id": p.ID, "status": p.Status,
			"resulting_changeset_id": p.ResultingLetheEventID, "existing": true,
		}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
	p, err := s.proposalSvc.Get(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if err := policy.RequireProject(principal, p.ProjectID); err != nil {
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	if p.Kind != "merge_request" {
		return nil, fmt.Errorf("proposal is not a merge request")
	}
	if p.OriginatingPrincipal == principal.ID {
		s.recordLedger(reqID, principal, "memory_merge", []string{p.ProjectID}, "denied: self approval", time.Since(start))
		return nil, fmt.Errorf("proposal originator cannot approve its own merge")
	}
	if p.Status != proposal.StatusPending {
		return nil, fmt.Errorf("proposal already %s", p.Status)
	}
	if !hasIndependentReview(p) {
		return nil, fmt.Errorf("merge proposal has no independent review finding")
	}
	target, err := s.lethe.GetRef(ctx, p.ProjectID, p.TargetRef)
	if err != nil || target == nil || !target.Protected {
		return nil, fmt.Errorf("protected target ref is unavailable")
	}
	head, err := s.lethe.GetChangeset(ctx, p.HeadChangesetID)
	if err != nil || head.ProjectID != p.ProjectID {
		return nil, fmt.Errorf("merge head is unavailable or crossed project boundary")
	}

	resultID := p.ChangesetID
	if resultID != "" {
		result, err := s.lethe.GetChangeset(ctx, resultID)
		if err != nil || result.ProjectID != p.ProjectID {
			return nil, fmt.Errorf("persisted merge result is unavailable or crossed project boundary")
		}
		if target.HeadChangesetID != resultID {
			if resultID == p.HeadChangesetID {
				ancestor, err := s.isChangesetAncestor(ctx, target.HeadChangesetID, resultID, p.ProjectID)
				if err != nil || !ancestor {
					return nil, fmt.Errorf("persisted fast-forward result is stale against current target")
				}
			} else if len(result.ParentIDs) == 0 || result.ParentIDs[0] != target.HeadChangesetID {
				return nil, fmt.Errorf("persisted merge result is stale against current target")
			}
			if err := s.advanceProtectedMerge(ctx, p, target.HeadChangesetID, resultID, principal.ID); err != nil {
				return nil, fmt.Errorf("recover protected merge: %w", err)
			}
		}
	} else if len(p.SelectedOps) > 0 {
		selected := make([]lethe.SemanticOp, 0, len(p.SelectedOps))
		for _, ordinal := range p.SelectedOps {
			found := false
			for _, op := range head.Ops {
				if op.Ordinal == ordinal {
					selected = append(selected, op)
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("selected operation ordinal %d no longer exists", ordinal)
			}
		}
		merged, err := s.lethe.CreateChangeset(ctx, lethe.CreateChangesetRequest{
			ProjectID: p.ProjectID, RefName: p.TargetRef,
			ParentIDs: []string{target.HeadChangesetID}, AuthorPrincipal: principal.ID,
			ActorID: principal.AgentID, Message: "cherry-pick proposal " + p.ID,
			IdempotencyKey: "merge/" + p.ID + "/" + idempotencyKey, Ops: selected,
		})
		if err != nil {
			return nil, fmt.Errorf("cherry-pick merge failed: %w", err)
		}
		resultID = merged.ChangesetID
		p, err = s.proposalSvc.SetMergeResult(ctx, p.ID, resultID)
		if err != nil {
			return nil, err
		}
		if err := s.advanceProtectedMerge(ctx, p, target.HeadChangesetID, resultID, principal.ID); err != nil {
			return nil, fmt.Errorf("cherry-pick merge failed: %w", err)
		}
	} else {
		ancestor, err := s.isChangesetAncestor(ctx, p.BaseChangesetID, p.HeadChangesetID, p.ProjectID)
		if err != nil {
			return nil, err
		}
		if target.HeadChangesetID == p.HeadChangesetID {
			// Recovery path when Lethe CAS succeeded but Charon status update did not.
			resultID = p.HeadChangesetID
		} else if target.HeadChangesetID == p.BaseChangesetID && ancestor {
			p, err = s.proposalSvc.SetMergeResult(ctx, p.ID, p.HeadChangesetID)
			if err != nil {
				return nil, err
			}
			if _, err := s.lethe.MergeAdvanceRef(ctx, p.ProjectID, p.TargetRef, lethe.MergeAdvanceRequest{
				ExpectedHead: target.HeadChangesetID, NewHead: p.HeadChangesetID,
				MergeProposalID: p.ID, ReviewerPrincipal: principal.ID,
			}); err != nil {
				return nil, fmt.Errorf("fast-forward merge failed: %w", err)
			}
			resultID = p.HeadChangesetID
		} else {
			conflicts, err := s.lethe.DetectConflicts(ctx, p.ProjectID, lethe.DetectConflictsRequest{
				BaseChangesetID:  p.BaseChangesetID,
				LeftChangesetID:  target.HeadChangesetID,
				RightChangesetID: p.HeadChangesetID,
			})
			if err != nil {
				return nil, fmt.Errorf("merge conflict recheck failed: %w", err)
			}
			ids := make([]string, 0, len(conflicts))
			blocking := false
			for _, conflict := range conflicts {
				ids = append(ids, conflict.ConflictID)
				if conflict.Severity != "info" {
					blocking = true
				}
			}
			if _, err := s.proposalSvc.SetConflictIDs(ctx, p.ID, ids); err != nil {
				return nil, err
			}
			if blocking {
				return nil, fmt.Errorf("merge has unresolved conflicts: %s", strings.Join(ids, ","))
			}
			mergeOp := lethe.SemanticOp{OpType: "attach_verification", Payload: map[string]interface{}{
				"kind": "reviewed_merge", "proposal_id": p.ID,
				"source_changeset_id": p.HeadChangesetID,
			}}
			merged, err := s.lethe.CreateChangeset(ctx, lethe.CreateChangesetRequest{
				ProjectID: p.ProjectID, RefName: p.TargetRef,
				ParentIDs:       []string{target.HeadChangesetID, p.HeadChangesetID},
				AuthorPrincipal: principal.ID, ActorID: principal.AgentID,
				Message:        "reviewed merge proposal " + p.ID,
				IdempotencyKey: "merge/" + p.ID + "/" + idempotencyKey,
				Ops:            []lethe.SemanticOp{mergeOp},
			})
			if err != nil {
				return nil, fmt.Errorf("reviewed merge failed: %w", err)
			}
			resultID = merged.ChangesetID
			p, err = s.proposalSvc.SetMergeResult(ctx, p.ID, resultID)
			if err != nil {
				return nil, err
			}
			if err := s.advanceProtectedMerge(ctx, p, target.HeadChangesetID, resultID, principal.ID); err != nil {
				return nil, fmt.Errorf("reviewed merge failed: %w", err)
			}
		}
	}

	existing, err = s.storeIdempotency(ctx, principal.ID, "memory_merge", idempotencyKey, args, "success", p.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		p, err = s.proposalSvc.Get(ctx, existing.ResultingResourceID)
		if err != nil {
			return nil, err
		}
		if p.Status == proposal.StatusApproved {
			resultID = p.ResultingLetheEventID
		} else {
			resultID = p.ChangesetID
		}
	}
	if p.Status == proposal.StatusPending {
		p, err = s.proposalSvc.CompleteMerge(ctx, p.ID, principal.ID, resultID)
		if err != nil {
			return nil, err
		}
	}
	data, _ := json.MarshalIndent(map[string]interface{}{
		"proposal_id": p.ID, "status": p.Status, "resulting_changeset_id": resultID,
	}, "", "  ")
	s.recordLedger(reqID, principal, "memory_merge", []string{p.ProjectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) advanceProtectedMerge(ctx context.Context, p *proposal.Proposal, expectedHead, newHead, reviewer string) error {
	if expectedHead == newHead {
		return nil
	}
	_, err := s.lethe.MergeAdvanceRef(ctx, p.ProjectID, p.TargetRef, lethe.MergeAdvanceRequest{
		ExpectedHead: expectedHead, NewHead: newHead,
		MergeProposalID: p.ID, ReviewerPrincipal: reviewer,
	})
	return err
}

func hasIndependentReview(p *proposal.Proposal) bool {
	for _, finding := range p.ReviewFindings {
		if finding.Reviewer != "" && finding.Reviewer != p.OriginatingPrincipal {
			return true
		}
	}
	return false
}

func (s *Server) handleMemoryMergeCancel(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.propose"); err != nil {
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	p, err := s.proposalSvc.Cancel(ctx, getString(request.GetArguments(), "proposal_id"), principal.ID)
	if err != nil {
		return nil, err
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	s.recordLedger(reqID, principal, "memory_merge_cancel", []string{p.ProjectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryRefList(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.read"); err != nil {
		s.recordLedger(reqID, principal, "memory_ref_list", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	project := getString(args, "project")
	if err := policy.RequireReadProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_ref_list", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	refs, err := s.lethe.ListRefs(ctx, project)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_ref_list", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("ref list failed: %w", err)
	}
	resp := map[string]interface{}{"project": project, "refs": refs, "count": len(refs)}
	data, _ := json.MarshalIndent(resp, "", "  ")
	s.recordLedger(reqID, principal, "memory_ref_list", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) requireOwnedMemoryRef(ctx context.Context, principal auth.Principal, project, refName string, creating bool) error {
	if refName == "" || strings.HasPrefix(refName, "refs/shared/") {
		return fmt.Errorf("protected or empty ref is not actor-owned")
	}
	// A local trusted principal with memory.write may operate broader project
	// refs, but the action remains attributed and ledgered by the caller.
	if policy.RequireScope(principal, "memory.write") == nil {
		return nil
	}
	actorID := principal.AgentID
	if actorID == "" {
		actorID = principal.ID
	}
	if refName == "refs/agents/"+actorID+"/main" {
		return nil
	}
	if strings.HasPrefix(refName, "refs/sessions/"+actorID+"/") &&
		!strings.Contains(strings.TrimPrefix(refName, "refs/sessions/"+actorID+"/"), "/") {
		return nil
	}
	if strings.HasPrefix(refName, "refs/topics/") {
		topicID := strings.TrimPrefix(refName, "refs/topics/")
		if topicID == "" || strings.Contains(topicID, "/") {
			return fmt.Errorf("invalid topic ref")
		}
		if creating {
			return nil
		}
		ref, err := s.lethe.GetRef(ctx, project, refName)
		if err != nil {
			return err
		}
		if ref != nil && ref.CreatedByPrincipal == principal.ID {
			return nil
		}
		return fmt.Errorf("topic ref is not owned by principal")
	}
	return fmt.Errorf("principal may only operate its agent, session, or owned topic refs")
}

func (s *Server) isChangesetAncestor(ctx context.Context, ancestorID, descendantID, project string) (bool, error) {
	if ancestorID == "" || descendantID == "" {
		return false, nil
	}
	queue := []string{descendantID}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == ancestorID {
			return true, nil
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if len(seen) > 10000 {
			return false, fmt.Errorf("changeset ancestry exceeds safety limit")
		}
		cs, err := s.lethe.GetChangeset(ctx, id)
		if err != nil {
			return false, err
		}
		if cs.ProjectID != project {
			return false, fmt.Errorf("changeset ancestry crossed project boundary")
		}
		queue = append(queue, cs.ParentIDs...)
	}
	return false, nil
}

func getIntSlice(args map[string]interface{}, key string) []int {
	var out []int
	if values, ok := args[key].([]interface{}); ok {
		for _, value := range values {
			switch n := value.(type) {
			case float64:
				out = append(out, int(n))
			case int:
				out = append(out, n)
			}
		}
	}
	return out
}

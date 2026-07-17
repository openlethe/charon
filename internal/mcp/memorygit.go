package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/openlethe/charon/internal/auth"
	"github.com/openlethe/charon/internal/canonical"
	"github.com/openlethe/charon/internal/metrics"
	"github.com/openlethe/charon/internal/policy"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

// registerMemoryGitTools adds Memory Git V1 MCP tools to the server.
func (s *Server) registerMemoryGitTools() {
	initTool := mcp.NewTool("memory_repo_init",
		mcp.WithDescription("Idempotently initialize a project's Memory Git root and protected refs/shared/main"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
	)
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
	contextTool := mcp.NewTool("memory_context_at",
		mcp.WithDescription("Reconstruct policy-authorized semantic memory at an exact ref/head; pin an input manifest only when the principal also has exact-project commit/write authority"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithString("ref", mcp.Description("Memory ref (default refs/shared/main)")),
		mcp.WithString("head_changeset_id", mcp.Description("Optional historical head reachable from the ref")),
		mcp.WithString("query", mcp.Description("Optional relevance query")),
		mcp.WithNumber("limit", mcp.Description("Maximum selected memories (default 20, max 100)"), mcp.Max(100)),
		mcp.WithString("session_id", mcp.Description("Optional Lethe session ID/key to attach to the manifest")),
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
		mcp.WithDescription("Record an explicit attributed review verdict for the current immutable merge proposal state"),
		mcp.WithString("proposal_id", mcp.Required(), mcp.Description("Charon proposal ID")),
		mcp.WithString("verdict", mcp.Required(), mcp.Description("Review decision"), mcp.Enum("approve", "request_changes", "reject")),
		mcp.WithString("comment", mcp.Required(), mcp.Description("Review rationale or finding")),
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

	s.mcpServer.AddTool(initTool, s.handleMemoryRepoInit)
	s.mcpServer.AddTool(statusTool, s.handleMemoryStatus)
	s.mcpServer.AddTool(logTool, s.handleMemoryLog)
	s.mcpServer.AddTool(showTool, s.handleMemoryShow)
	s.mcpServer.AddTool(branchTool, s.handleMemoryBranchCreate)
	s.mcpServer.AddTool(diffTool, s.handleMemoryDiff)
	s.mcpServer.AddTool(contextTool, s.handleMemoryContextAt)
	s.mcpServer.AddTool(changesetTool, s.handleMemoryChangesetCreate)
	s.mcpServer.AddTool(mergeProposeTool, s.handleMemoryMergePropose)
	s.mcpServer.AddTool(mergeReviewTool, s.handleMemoryMergeReview)
	s.mcpServer.AddTool(mergeTool, s.handleMemoryMerge)
	s.mcpServer.AddTool(mergeCancelTool, s.handleMemoryMergeCancel)
	s.mcpServer.AddTool(refListTool, s.handleMemoryRefList)
}

func (s *Server) handleMemoryRepoInit(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	if err := s.requireMutationsEnabled(); err != nil {
		return nil, err
	}
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.commit"); err != nil {
		s.recordLedger(reqID, principal, "memory_repo_init", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	project := getString(request.GetArguments(), "project")
	if err := policy.RequireProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_repo_init", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	intent, err := s.beginAuditIntent(ctx, principal, "memory_repo_init", project,
		canonical.MustDigest("charon/audit-request/v1", map[string]string{"project": project}),
		project+":refs/shared/main", "ensure legacy root and protected ref")
	if err != nil {
		return nil, err
	}
	root, err := s.lethe.EnsureLegacyRoot(ctx, project)
	if err != nil {
		s.finishAudit(intent, err)
		s.recordLedger(reqID, principal, "memory_repo_init", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("repository initialization failed: %w", err)
	}
	s.finishAudit(intent, nil)
	data, _ := json.MarshalIndent(root, "", "  ")
	s.recordLedger(reqID, principal, "memory_repo_init", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
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
	if err := s.requireMutationsEnabled(); err != nil {
		return nil, err
	}
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
	intent, err := s.beginAuditIntent(ctx, principal, "memory_branch_create", project,
		canonical.MustDigest("charon/audit-request/v1", map[string]string{"project": project, "ref_name": refName, "head_changeset_id": headID}),
		project+":"+refName, "create ref at "+headID)
	if err != nil {
		return nil, err
	}
	ref, err := s.lethe.CreateBranch(ctx, project, lethe.CreateBranchRequest{
		RefName:         refName,
		HeadChangesetID: headID,
		Principal:       principal.ID,
	})
	if err != nil {
		s.finishAudit(intent, err)
		s.recordLedger(reqID, principal, "memory_branch_create", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("branch create failed: %w", err)
	}
	s.finishAudit(intent, nil)
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

func (s *Server) handleMemoryContextAt(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.read"); err != nil {
		s.recordLedger(reqID, principal, "memory_context_at", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	project := getString(args, "project")
	if err := policy.RequireReadProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_context_at", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	refName := getString(args, "ref")
	if refName == "" {
		refName = "refs/shared/main"
	}
	if !strings.HasPrefix(refName, "refs/shared/") {
		if err := s.requireOwnedMemoryRef(ctx, principal, project, refName, false); err != nil {
			s.recordLedger(reqID, principal, "memory_context_at", []string{project}, "denied: ref ownership", time.Since(start))
			return nil, fmt.Errorf("unauthorized: %w", err)
		}
	}
	limit := 20
	if value, ok := args["limit"].(float64); ok && value > 0 {
		limit = int(value)
	}
	if limit > 100 {
		limit = 100
	}
	canPinManifest := policy.RequireProject(principal, project) == nil &&
		(policy.RequireScope(principal, "memory.commit") == nil ||
			policy.RequireScope(principal, "memory.write") == nil)
	if s.cfg.RecoveryReadOnly {
		// Manifest pinning is an upstream write; recovery read-only serves
		// the context projection without it.
		canPinManifest = false
	}
	view, err := s.lethe.GetMemoryContext(ctx, project, lethe.MemoryContextRequest{
		RefName:         refName,
		HeadChangesetID: getString(args, "head_changeset_id"),
		Query:           getString(args, "query"),
		Limit:           limit,
		SessionID:       getString(args, "session_id"),
		ActorID:         principal.ID,
		CreateManifest:  canPinManifest,
	})
	if err != nil {
		s.recordLedger(reqID, principal, "memory_context_at", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("memory context failed: %w", err)
	}
	data, _ := json.MarshalIndent(view, "", "  ")
	s.recordLedger(reqID, principal, "memory_context_at", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryChangesetCreate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	if err := s.requireMutationsEnabled(); err != nil {
		return nil, err
	}
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
	// Strict typed validation runs before any persistent or upstream side
	// effect. A malformed request never reaches Lethe and never matches a
	// stored idempotency record.
	typed, err := parseChangesetCreateArgs(args)
	if err != nil {
		metrics.Inc(metrics.StrictParsingRejections)
		s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "rejected: malformed request", time.Since(start))
		return nil, fmt.Errorf("invalid changeset request: %w", err)
	}
	// Ensure ref is not protected (models cannot directly manipulate shared refs)
	refName := typed.Ref
	if refName == "refs/shared/main" || len(refName) > len("refs/shared/") && refName[:len("refs/shared/")] == "refs/shared/" {
		s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "denied: protected ref", time.Since(start))
		return nil, fmt.Errorf("unauthorized: cannot commit directly to protected ref %s", refName)
	}
	if err := s.requireOwnedMemoryRef(ctx, principal, project, refName, false); err != nil {
		s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "denied: ref ownership", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	idempotencyKey := typed.IdempotencyKey
	// Defense in depth: Charon's request-hash idempotency runs before the CAS
	// pre-check so an exact replay returns the original changeset even after
	// the ref head advanced, and a same-key different-request replay is
	// rejected locally before it can reach Lethe. The digest covers the
	// canonical typed request — exactly what is sent upstream.
	existing, err := s.checkIdempotency(ctx, principal.ID, "memory_changeset_create", idempotencyKey, typed)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "success (idempotent replay)", time.Since(start))
		return s.changesetResultFromID(ctx, existing.ResultingResourceID)
	}

	// Parent shaping is pure validation and must happen before the audit
	// intent so a malformed request never leaves an open intent behind.
	parents := typed.ParentIDs
	if len(parents) == 0 {
		parents = []string{typed.ExpectedHead}
	} else if parents[0] != typed.ExpectedHead {
		return nil, fmt.Errorf("first parent must match expected_head")
	}

	intent, err := s.beginAuditIntent(ctx, principal, "memory_changeset_create", project,
		canonical.MustDigest("charon/audit-request/v1", typed),
		project+":"+refName, "commit changeset and advance ref")
	if err != nil {
		return nil, err
	}

	expectedHead := typed.ExpectedHead
	ref, err := s.lethe.GetRef(ctx, project, refName)
	if err != nil {
		s.finishAudit(intent, err)
		return nil, fmt.Errorf("load ref: %w", err)
	}
	if ref == nil {
		s.finishAudit(intent, fmt.Errorf("ref does not exist"))
		return nil, fmt.Errorf("ref does not exist; create it with memory_branch_create")
	}
	// No local expected_head comparison here: Lethe's transactional CAS is
	// authoritative. A local pre-check rejects exact retries after a crash
	// (the ref already advanced to the changeset the first call created)
	// instead of letting Lethe's strict idempotency replay the original
	// result; genuine stale-head conflicts still fail upstream.

	cs, err := s.lethe.CreateChangeset(ctx, lethe.CreateChangesetRequest{
		ProjectID:          project,
		RefName:            refName,
		ParentIDs:          parents,
		AuthorPrincipal:    principal.ID,
		ActorID:            principal.AgentID,
		Message:            typed.Message,
		IdempotencyKey:     idempotencyKey,
		Ops:                toLetheOps(typed.Ops),
		ExpectedHead:       expectedHead,
		AdvanceRef:         true,
		CreateRefIfMissing: false,
	})
	if err != nil {
		s.finishAudit(intent, err)
		s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("changeset create failed: %w", err)
	}

	// Store idempotency; a concurrent same-key race returns the winner. The
	// changeset already landed upstream, so a local bookkeeping failure is an
	// ambiguous outcome — never a recorded failure of a landed mutation.
	existingIdem, err := s.storeIdempotency(ctx, principal.ID, "memory_changeset_create", idempotencyKey, typed, "success", cs.ChangesetID)
	if err != nil {
		s.finishAuditAmbiguous(intent, err)
		return nil, err
	}
	if existingIdem != nil {
		s.finishAudit(intent, nil)
		s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "success (idempotent replay)", time.Since(start))
		return s.changesetResultFromID(ctx, existingIdem.ResultingResourceID)
	}

	s.finishAudit(intent, nil)
	data, _ := json.MarshalIndent(cs, "", "  ")
	s.recordLedger(reqID, principal, "memory_changeset_create", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

// changesetResultFromID renders the stored changeset for an idempotent replay
// so a replayed call returns the same result shape as the original success.
func (s *Server) changesetResultFromID(ctx context.Context, changesetID string) (*mcp.CallToolResult, error) {
	cs, err := s.lethe.GetChangeset(ctx, changesetID)
	if err != nil || cs == nil {
		data, _ := json.MarshalIndent(map[string]interface{}{"changeset_id": changesetID, "existing": true}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
	data, _ := json.MarshalIndent(cs, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryMergePropose(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	if err := s.requireMutationsEnabled(); err != nil {
		return nil, err
	}
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
	typed, err := parseMergeProposeArgs(args)
	if err != nil {
		metrics.Inc(metrics.StrictParsingRejections)
		s.recordLedger(reqID, principal, "memory_merge_propose", []string{project}, "rejected: malformed request", time.Since(start))
		return nil, fmt.Errorf("invalid merge proposal request: %w", err)
	}
	// Merge proposals target protected shared refs. Owned non-protected refs use
	// the normal CAS commit path.
	targetRef := typed.TargetRef
	if targetRef != "refs/shared/main" && !strings.HasPrefix(targetRef, "refs/shared/") {
		return nil, fmt.Errorf("merge proposals must target a protected shared ref")
	}

	headID := typed.HeadChangesetID
	baseID := typed.BaseChangesetID
	idempotencyKey := typed.IdempotencyKey
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
	selectedOps := typed.SelectedOps
	for _, ordinal := range selectedOps {
		if ordinal < 0 || ordinal >= len(head.Ops) {
			return nil, fmt.Errorf("selected operation ordinal %d is out of range", ordinal)
		}
	}

	// Conflict analysis is pure in Lethe: detection never persists. Proposal
	// idempotency resolution happens before any persistent side effect.
	var conflicts []lethe.Conflict
	var conflictIDs []string
	if baseID != target.HeadChangesetID {
		conflicts, err = s.lethe.DetectConflicts(ctx, project, lethe.DetectConflictsRequest{
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

	intent, err := s.beginAuditIntent(ctx, principal, "memory_merge_propose", project,
		canonical.MustDigest("charon/audit-request/v1", typed),
		project+":"+targetRef, "create merge proposal "+headID+" -> "+targetRef)
	if err != nil {
		return nil, err
	}

	// Exactly-once proposal creation: the proposal row, the canonical request
	// digest, and the idempotency record commit atomically. An exact replay
	// returns the original proposal; a same-key different-body replay fails
	// closed.
	prop, replay, err := s.proposalSvc.CreateIdempotent(ctx, proposal.CreateRequest{
		ProjectID: project,
		EventType: "merge_request",
		Kind:      "merge_request",
		Content:   typed.Message,
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
	}, proposal.RequestDigestInput{
		Operation:       "memory_merge_propose",
		Principal:       principal.ID,
		ProjectID:       project,
		TargetRef:       targetRef,
		BaseChangesetID: baseID,
		HeadChangesetID: headID,
		SelectedOps:     selectedOps,
		Message:         typed.Message,
	})
	if err != nil {
		s.finishAudit(intent, err)
		s.recordLedger(reqID, principal, "memory_merge_propose", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("merge propose failed: %w", err)
	}

	// Conflicts persist only as part of the proposal operation, bound to the
	// proposal and idempotent by deterministic identity. On replay this also
	// converges any crash window between creation and persistence: Lethe
	// recomputes deterministic identities, so only conflicts matching the
	// recorded snapshot are (re-)persisted; conflicts against a moved target
	// head are re-detected authoritatively at merge time.
	if len(conflictIDs) > 0 && len(conflicts) > 0 {
		persistSet := conflicts
		if replay {
			snapshot := make(map[string]bool, len(prop.ConflictIDs))
			for _, id := range prop.ConflictIDs {
				snapshot[id] = true
			}
			persistSet = make([]lethe.Conflict, 0, len(prop.ConflictIDs))
			for _, c := range conflicts {
				if snapshot[c.ConflictID] {
					persistSet = append(persistSet, c)
				}
			}
		}
		if len(persistSet) > 0 {
			if err := s.lethe.PersistConflicts(ctx, project, prop.ID, persistSet); err != nil {
				s.finishAudit(intent, err)
				s.recordLedger(reqID, principal, "memory_merge_propose", []string{project}, "error: conflict persistence failed", time.Since(start))
				return nil, fmt.Errorf("persist proposal conflicts: %w", err)
			}
		}
	}
	s.finishAudit(intent, nil)

	data, _ := json.MarshalIndent(map[string]interface{}{
		"proposal_id": prop.ID, "status": prop.Status, "target_ref": targetRef,
		"base_changeset_id": baseID, "head_changeset_id": headID, "conflict_ids": prop.ConflictIDs,
		"existing": replay,
	}, "", "  ")
	s.recordLedger(reqID, principal, "memory_merge_propose", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryMergeReview(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	if err := s.requireMutationsEnabled(); err != nil {
		return nil, err
	}
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
	if p.OriginatingPrincipal == principal.ID {
		s.recordLedger(reqID, principal, "memory_merge_review", []string{p.ProjectID}, "denied: self review", time.Since(start))
		return nil, fmt.Errorf("proposal originator cannot review its own merge")
	}
	target, err := s.lethe.GetRef(ctx, p.ProjectID, p.TargetRef)
	if err != nil || target == nil || !target.Protected {
		return nil, fmt.Errorf("protected target ref is unavailable")
	}
	intent, err := s.beginAuditIntent(ctx, principal, "memory_merge_review", p.ProjectID,
		canonical.MustDigest("charon/audit-request/v1", map[string]string{"proposal_id": proposalID, "verdict": getString(args, "verdict")}),
		proposalID, "attach "+getString(args, "verdict")+" review finding")
	if err != nil {
		return nil, err
	}
	p, err = s.proposalSvc.AddReviewFinding(ctx, proposalID, proposal.ReviewFinding{
		Reviewer:              principal.ID,
		Verdict:               proposal.ReviewVerdict(getString(args, "verdict")),
		Comment:               getString(args, "comment"),
		Severity:              getString(args, "severity"),
		TargetHeadChangesetID: target.HeadChangesetID,
	})
	// A reject verdict is a terminal decision: the proposal closes and its
	// conflicts retire so they cannot block or pollute future merges. Both
	// steps are idempotent and converge on retry: if a previous attempt
	// closed the proposal but failed to reach Lethe (or the finding could
	// not be re-appended), the repeated review still retries retirement
	// instead of stranding open conflicts.
	if getString(args, "verdict") == string(proposal.ReviewVerdictReject) {
		if err != nil {
			// The finding may already be attached from a previous attempt;
			// converge when the proposal is already rejected.
			existing, getErr := s.proposalSvc.Get(ctx, proposalID)
			if getErr != nil || existing.Status != proposal.StatusRejected {
				s.finishAudit(intent, err)
				return nil, err
			}
			p = existing
		} else if p, err = s.proposalSvc.Reject(ctx, proposal.RejectRequest{ProposalID: proposalID, DecidedBy: principal.ID}); err != nil {
			existing, getErr := s.proposalSvc.Get(ctx, proposalID)
			if getErr != nil || existing.Status != proposal.StatusRejected {
				s.finishAudit(intent, err)
				return nil, fmt.Errorf("close rejected proposal: %w", err)
			}
			p = existing
		}
		if err := s.lethe.RetireConflicts(ctx, p.ProjectID, p.ID, "rejected"); err != nil {
			s.finishAuditAmbiguous(intent, err)
			s.recordLedger(reqID, principal, "memory_merge_review", []string{p.ProjectID}, "error: conflict retirement failed: "+sanitizeError(err), time.Since(start))
			return nil, fmt.Errorf("proposal rejected but conflict retirement failed (retry review to converge): %w", err)
		}
	} else if err != nil {
		s.finishAudit(intent, err)
		return nil, err
	}
	s.finishAudit(intent, nil)
	data, _ := json.MarshalIndent(p, "", "  ")
	s.recordLedger(reqID, principal, "memory_merge_review", []string{p.ProjectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleMemoryMerge(ctx context.Context, request mcp.CallToolRequest) (result *mcp.CallToolResult, mergeErr error) {
	start := time.Now()
	if err := s.requireMutationsEnabled(); err != nil {
		return nil, err
	}
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.merge"); err != nil {
		s.recordLedger(reqID, principal, "memory_merge", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	args := request.GetArguments()
	typed, err := parseMergeArgs(args)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_merge", nil, "rejected: malformed request", time.Since(start))
		return nil, fmt.Errorf("invalid merge request: %w", err)
	}
	proposalID := typed.ProposalID
	idempotencyKey := typed.IdempotencyKey
	existing, err := s.checkIdempotency(ctx, principal.ID, "memory_merge", idempotencyKey, typed)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		p, err := s.proposalSvc.Get(ctx, existing.ResultingResourceID)
		if err != nil {
			return nil, err
		}
		if err := policy.RequireProject(principal, p.ProjectID); err != nil {
			return nil, fmt.Errorf("unauthorized: %w", err)
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
	target, err := s.lethe.GetRef(ctx, p.ProjectID, p.TargetRef)
	if err != nil || target == nil || !target.Protected {
		return nil, fmt.Errorf("protected target ref is unavailable")
	}
	_, approved := independentApprovalReviewer(p, target.HeadChangesetID)
	if !approved && p.ChangesetID != "" && target.HeadChangesetID == p.ChangesetID {
		// Crash recovery: the protected ref already advanced to the persisted
		// merge result. Reaching that state required a signed merge advance
		// derived from a bound approval, so accept the recorded independent
		// approval instead of revalidating it against the post-merge head.
		_, approved = latestIndependentApprovalReviewer(p)
	}
	if !approved {
		return nil, fmt.Errorf("merge proposal has no current independent approval for the target head")
	}
	metrics.Inc(metrics.MergeAttempts)
	head, err := s.lethe.GetChangeset(ctx, p.HeadChangesetID)
	if err != nil || head.ProjectID != p.ProjectID {
		return nil, fmt.Errorf("merge head is unavailable or crossed project boundary")
	}

	intent, err := s.beginAuditIntent(ctx, principal, "memory_merge", p.ProjectID,
		canonical.MustDigest("charon/audit-request/v1", typed),
		proposalID, "advance "+p.TargetRef+" to merge result")
	if err != nil {
		return nil, err
	}
	// Every return path after this point records its outcome: named returns
	// let the deferred finish see the final error value. Once the protected
	// ref has moved (landed), a later local bookkeeping error must not record
	// the merge as failed — the true outcome is ambiguous.
	landed := false
	defer func() {
		if mergeErr != nil && landed {
			s.finishAuditAmbiguous(intent, mergeErr)
			return
		}
		s.finishAudit(intent, mergeErr)
	}()

	resultID := p.ChangesetID
	if resultID != "" {
		result, err := s.lethe.GetChangeset(ctx, resultID)
		if err != nil || result.ProjectID != p.ProjectID {
			return nil, fmt.Errorf("persisted merge result is unavailable or crossed project boundary")
		}
		if target.HeadChangesetID != resultID {
			strategy := lethe.StrategyCherryPick
			if resultID == p.HeadChangesetID {
				ancestor, err := s.isChangesetAncestor(ctx, target.HeadChangesetID, resultID, p.ProjectID)
				if err != nil || !ancestor {
					return nil, fmt.Errorf("persisted fast-forward result is stale against current target")
				}
				strategy = lethe.StrategyFastForward
			} else if len(result.ParentIDs) == 0 || result.ParentIDs[0] != target.HeadChangesetID {
				return nil, fmt.Errorf("persisted merge result is stale against current target")
			} else if len(result.ParentIDs) == 2 {
				strategy = lethe.StrategyMergeCommit
			}
			// Re-validate approval against the freshest proposal state
			// immediately before moving the protected ref.
			fresh, freshReviewer, err := s.requireCurrentApproval(ctx, p.ID, target.HeadChangesetID)
			if err != nil {
				return nil, err
			}
			p = fresh
			if err := s.advanceProtectedMerge(ctx, p, target.HeadChangesetID, resultID, freshReviewer, principal.ID, strategy); err != nil {
				return nil, fmt.Errorf("recover protected merge: %w", err)
			}
			landed = true
		} else {
			// The ref already points at the merge result: it landed in a
			// previous attempt even though bookkeeping did not finish.
			landed = true
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
		fresh, freshReviewer, err := s.requireCurrentApproval(ctx, p.ID, target.HeadChangesetID)
		if err != nil {
			return nil, err
		}
		p = fresh
		if err := s.advanceProtectedMerge(ctx, p, target.HeadChangesetID, resultID, freshReviewer, principal.ID, lethe.StrategyCherryPick); err != nil {
			return nil, fmt.Errorf("cherry-pick merge failed: %w", err)
		}
		landed = true
	} else {
		ancestor, err := s.isChangesetAncestor(ctx, p.BaseChangesetID, p.HeadChangesetID, p.ProjectID)
		if err != nil {
			return nil, err
		}
		if target.HeadChangesetID == p.HeadChangesetID {
			// Recovery path when Lethe CAS succeeded but Charon status update did not.
			resultID = p.HeadChangesetID
			landed = true
		} else if target.HeadChangesetID == p.BaseChangesetID && ancestor {
			p, err = s.proposalSvc.SetMergeResult(ctx, p.ID, p.HeadChangesetID)
			if err != nil {
				return nil, err
			}
			fresh, freshReviewer, err := s.requireCurrentApproval(ctx, p.ID, target.HeadChangesetID)
			if err != nil {
				return nil, err
			}
			p = fresh
			if err := s.advanceProtectedMerge(ctx, p, target.HeadChangesetID, p.HeadChangesetID, freshReviewer, principal.ID, lethe.StrategyFastForward); err != nil {
				return nil, fmt.Errorf("fast-forward merge failed: %w", err)
			}
			landed = true
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
			// Persist the rechecked conflicts bound to the proposal (pure
			// detection + explicit proposal-time persistence, idempotent by
			// deterministic identity).
			if len(conflicts) > 0 {
				if err := s.lethe.PersistConflicts(ctx, p.ProjectID, p.ID, conflicts); err != nil {
					return nil, fmt.Errorf("persist merge recheck conflicts: %w", err)
				}
			}
			if blocking {
				return nil, fmt.Errorf("merge has unresolved conflicts: %s", strings.Join(ids, ","))
			}
			// Target-less merge attestation: valid under memory_git/v1 because it
			// carries a human-readable summary plus provenance (proposal and
			// source changeset) — the only shape Lethe accepts without a target.
			mergeOp := lethe.SemanticOp{OpType: "attach_verification", Payload: map[string]interface{}{
				"kind": "reviewed_merge", "summary": "reviewed merge proposal " + p.ID,
				"proposal_id": p.ID, "source_changeset_id": p.HeadChangesetID,
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
			fresh, freshReviewer, err := s.requireCurrentApproval(ctx, p.ID, target.HeadChangesetID)
			if err != nil {
				return nil, err
			}
			p = fresh
			if err := s.advanceProtectedMerge(ctx, p, target.HeadChangesetID, resultID, freshReviewer, principal.ID, lethe.StrategyMergeCommit); err != nil {
				return nil, fmt.Errorf("reviewed merge failed: %w", err)
			}
			landed = true
		}
	}

	existing, err = s.storeIdempotency(ctx, principal.ID, "memory_merge", idempotencyKey, typed, "success", p.ID)
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
	if p.Status == proposal.StatusApproved {
		// The merge landed: proposal-time conflicts are superseded so they can
		// never pollute future accepted-context manifests. Best-effort — the
		// merge itself is durable; stranded rows surface in reconciliation.
		if err := s.lethe.RetireConflicts(ctx, p.ProjectID, p.ID, "superseded"); err != nil {
			s.recordLedger(reqID, principal, "memory_merge", []string{p.ProjectID}, "warning: conflict retirement failed: "+sanitizeError(err), time.Since(start))
		}
	}
	data, _ := json.MarshalIndent(map[string]interface{}{
		"proposal_id": p.ID, "status": p.Status, "resulting_changeset_id": resultID,
	}, "", "  ")
	s.recordLedger(reqID, principal, "memory_merge", []string{p.ProjectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

// advanceProtectedMerge signs a fresh single-use authorization bound to the
// current proposal-state digest and drives Lethe's enforced merge path. The
// strategy declares the merge shape Lethe must independently verify.
func (s *Server) advanceProtectedMerge(ctx context.Context, p *proposal.Proposal, expectedHead, newHead, reviewer, merger, strategy string) error {
	if expectedHead == newHead {
		return nil
	}
	_, err := s.lethe.MergeAdvanceRef(ctx, p.ProjectID, p.TargetRef, lethe.MergeAdvanceRequest{
		ExpectedHead: expectedHead, NewHead: newHead,
		MergeProposalID: p.ID, ReviewerPrincipal: reviewer,
		ProposalDigest: p.StateDigest, MergerPrincipal: merger, Strategy: strategy,
	})
	if err != nil && strings.Contains(err.Error(), "409") {
		metrics.Inc(metrics.MergeCASConflicts)
	}
	return err
}

// requireCurrentApproval re-validates the independent approval against the
// freshest proposal state immediately before a protected-ref advance. The
// merge path mutates proposal state (persisting the merge result and conflict
// snapshots) while concurrent reviewers may append objection findings, so the
// approval computed at handler entry can be stale by the time the ref moves.
// The envelope is signed only when the approval still holds; an objection or
// terminal transition appended in the meantime aborts the merge.
func (s *Server) requireCurrentApproval(ctx context.Context, proposalID, targetHeadChangesetID string) (*proposal.Proposal, string, error) {
	fresh, err := s.proposalSvc.Get(ctx, proposalID)
	if err != nil {
		return nil, "", err
	}
	if fresh.Status != proposal.StatusPending {
		return nil, "", fmt.Errorf("proposal is no longer pending (now %s); merge aborted", fresh.Status)
	}
	reviewer, approved := independentApprovalReviewer(fresh, targetHeadChangesetID)
	if !approved {
		return nil, "", fmt.Errorf("merge approval no longer valid for the target head; a concurrent review changed the outcome")
	}
	return fresh, reviewer, nil
}

func independentApprovalReviewer(p *proposal.Proposal, targetHeadChangesetID string) (string, bool) {
	return approvalReviewer(p, func(f proposal.ReviewFinding) bool {
		return f.TargetDigest == proposal.ReviewTargetDigest(p, targetHeadChangesetID)
	})
}

// latestIndependentApprovalReviewer returns the independent approval recorded
// for a proposal regardless of which target head the review was bound to. It
// is used only for crash recovery when the protected ref already points at
// the persisted merge result — a state reachable only through a signed merge
// advance that itself required a head-bound approval.
func latestIndependentApprovalReviewer(p *proposal.Proposal) (string, bool) {
	return approvalReviewer(p, func(proposal.ReviewFinding) bool { return true })
}

func approvalReviewer(p *proposal.Proposal, bindingMatch func(proposal.ReviewFinding) bool) (string, bool) {
	if p == nil {
		return "", false
	}
	seenReviewers := make(map[string]struct{})
	approvedBy := ""
	for i := len(p.ReviewFindings) - 1; i >= 0; i-- {
		finding := p.ReviewFindings[i]
		if finding.Reviewer == "" || finding.Reviewer == p.OriginatingPrincipal || !bindingMatch(finding) {
			continue
		}
		if _, seen := seenReviewers[finding.Reviewer]; seen {
			continue
		}
		seenReviewers[finding.Reviewer] = struct{}{}
		if finding.Verdict != proposal.ReviewVerdictApprove {
			return "", false
		}
		if approvedBy == "" {
			approvedBy = finding.Reviewer
		}
	}
	return approvedBy, approvedBy != ""
}

func (s *Server) handleMemoryMergeCancel(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	if err := s.requireMutationsEnabled(); err != nil {
		return nil, err
	}
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "memory.propose"); err != nil {
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	proposalID := getString(request.GetArguments(), "proposal_id")
	intent, err := s.beginAuditIntent(ctx, principal, "memory_merge_cancel", "",
		canonical.MustDigest("charon/audit-request/v1", map[string]string{"proposal_id": proposalID}),
		proposalID, "cancel proposal and retire conflicts")
	if err != nil {
		return nil, err
	}
	p, err := s.proposalSvc.Cancel(ctx, proposalID, principal.ID)
	if err != nil {
		// A previous cancel may have succeeded in Charon while conflict
		// retirement failed; allow the retry to converge instead of stranding
		// open conflicts behind an "already decided" error.
		existing, getErr := s.proposalSvc.Get(ctx, proposalID)
		if getErr != nil || existing.Status != proposal.StatusCancelled {
			s.finishAudit(intent, err)
			return nil, err
		}
		p = existing
	}
	// Cancel retires the proposal's active conflicts so they cannot pollute
	// accepted-context manifests. Retirement is idempotent; if Lethe cannot be
	// reached the caller sees the failure and can retry cancel safely.
	if err := s.lethe.RetireConflicts(ctx, p.ProjectID, p.ID, "canceled"); err != nil {
		s.finishAudit(intent, err)
		s.recordLedger(reqID, principal, "memory_merge_cancel", []string{p.ProjectID}, "error: conflict retirement failed: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("proposal canceled but conflict retirement failed (retry cancel to converge): %w", err)
	}
	s.finishAudit(intent, nil)
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

func getIntSlice(args map[string]interface{}, key string) ([]int, error) {
	value, present := args[key]
	if !present || value == nil {
		return nil, nil
	}

	values, ok := value.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%s must be an array of non-negative integers", key)
	}

	out := make([]int, 0, len(values))
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		var ordinal int
		switch number := value.(type) {
		case float64:
			if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number != math.Trunc(number) || number > float64(math.MaxInt) {
				return nil, fmt.Errorf("%s must contain only non-negative integers", key)
			}
			ordinal = int(number)
		case int:
			if number < 0 {
				return nil, fmt.Errorf("%s must contain only non-negative integers", key)
			}
			ordinal = number
		default:
			return nil, fmt.Errorf("%s must contain only non-negative integers", key)
		}
		if _, duplicate := seen[ordinal]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate ordinal %d", key, ordinal)
		}
		seen[ordinal] = struct{}{}
		out = append(out, ordinal)
	}
	return out, nil
}

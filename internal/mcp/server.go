package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/openlethe/charon/internal/auth"
	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/idempotency"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/policy"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

type threadUnavailableError struct {
	cause error
}

func (e *threadUnavailableError) Error() string { return "thread unavailable" }
func (e *threadUnavailableError) Unwrap() error { return e.cause }

func hideThreadAccessError(cause error) error {
	return &threadUnavailableError{cause: cause}
}

func threadAccessCause(err error) error {
	var hidden *threadUnavailableError
	if errors.As(err, &hidden) {
		return hidden.cause
	}
	return err
}

// Server wraps the MCP server with Charon policy.
type Server struct {
	mcpServer   *server.MCPServer
	streamable  *server.StreamableHTTPServer
	lethe       *lethe.Client
	ledger      *ledger.Service
	proposalSvc *proposal.Service
	idempotency *idempotency.Service
	cfg         *config.Config
}

// NewServer creates a Charon MCP server.
func NewServer(cfg *config.Config, letheClient *lethe.Client, ledgerSvc *ledger.Service, proposalSvc *proposal.Service, idempotencySvc *idempotency.Service) *Server {
	s := &Server{
		lethe:       letheClient,
		ledger:      ledgerSvc,
		proposalSvc: proposalSvc,
		idempotency: idempotencySvc,
		cfg:         cfg,
	}

	s.mcpServer = server.NewMCPServer(
		"charon",
		"0.1.0",
		server.WithToolCapabilities(false),
	)

	s.registerTools()
	s.streamable = server.NewStreamableHTTPServer(s.mcpServer)
	return s
}

// ServeHTTP implements http.Handler for the MCP server.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.streamable.ServeHTTP(w, r)
}

func (s *Server) registerTools() {
	if s.cfg != nil && s.cfg.Mode == config.ModeMemoryGit {
		// Charon's frontier-model path is versioned by construction. Do not
		// register compatibility tools that read or write legacy Lethe events.
		s.registerMemoryGitTools()
		return
	}

	// Compatibility/read-only tools
	searchTool := mcp.NewTool("memory_search",
		mcp.WithDescription("Search accepted memory across authorized projects"),
		mcp.WithString("query", mcp.Description("Search query string")),
		mcp.WithArray("projects", mcp.Description("Projects to search (defaults to all authorized)"), mcp.WithStringItems()),
		mcp.WithArray("event_types", mcp.Description("Filter by event types"), mcp.WithStringItems()),
		mcp.WithArray("tags", mcp.Description("Filter by tag (Lethe v0.4 supports at most one)"), mcp.WithStringItems()),
		mcp.WithNumber("limit", mcp.Description("Maximum results to return (default 20, max 100)"), mcp.Max(100)),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	getTool := mcp.NewTool("memory_get",
		mcp.WithDescription("Retrieve a single memory event by ID"),
		mcp.WithString("id", mcp.Required(), mcp.Description("Event ID")),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	recentTool := mcp.NewTool("memory_recent",
		mcp.WithDescription("Retrieve recent accepted memory"),
		mcp.WithArray("projects", mcp.Description("Projects to include (defaults to all authorized)"), mcp.WithStringItems()),
		mcp.WithNumber("limit", mcp.Description("Maximum results (default 20, max 100)"), mcp.Max(100)),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	threadListTool := mcp.NewTool("thread_list",
		mcp.WithDescription("List memory threads for an authorized Lethe session"),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Lethe session ID or session key")),
		mcp.WithString("status", mcp.Description("Optional thread status"), mcp.Enum("open", "resolved", "blocked")),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	threadGetTool := mcp.NewTool("thread_get",
		mcp.WithDescription("Retrieve one memory thread from an authorized project"),
		mcp.WithString("id", mcp.Required(), mcp.Description("Thread ID")),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	threadEventsTool := mcp.NewTool("thread_events",
		mcp.WithDescription("Retrieve the events in a memory thread from an authorized project"),
		mcp.WithString("id", mcp.Required(), mcp.Description("Thread ID")),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	// Proposal tools
	proposeTool := mcp.NewTool("memory_propose",
		mcp.WithDescription("Propose a durable memory entry for approval"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithString("event_type", mcp.Required(), mcp.Description("Event type (e.g., record, log, checkpoint)")),
		mcp.WithString("kind", mcp.Description("Kind of memory (e.g., decision, observation, task)")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Memory content")),
		mcp.WithString("reason", mcp.Description("Reason or context for this memory")),
		mcp.WithNumber("confidence", mcp.Description("Confidence level (0.0-1.0)"), mcp.Min(0), mcp.Max(1)),
		mcp.WithArray("tags", mcp.Description("Tags for categorization"), mcp.WithStringItems()),
		mcp.WithString("idempotency_key", mcp.Required(), mcp.Description("Client-generated unique key for this proposal")),
	)

	checkpointTool := mcp.NewTool("memory_checkpoint",
		mcp.WithDescription("Propose a structured conversation checkpoint"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Summary of the conversation or state")),
		mcp.WithArray("decisions", mcp.Description("Key decisions made"), mcp.WithStringItems()),
		mcp.WithArray("open_questions", mcp.Description("Open questions or uncertainties"), mcp.WithStringItems()),
		mcp.WithArray("next_actions", mcp.Description("Recommended next actions"), mcp.WithStringItems()),
		mcp.WithString("idempotency_key", mcp.Required(), mcp.Description("Client-generated unique key for this checkpoint")),
	)

	listProposalsTool := mcp.NewTool("proposal_list",
		mcp.WithDescription("List pending memory proposals"),
		mcp.WithString("status", mcp.Description("Filter by status (pending, approved, rejected, expired)"), mcp.Enum("pending", "approved", "rejected", "expired")),
		mcp.WithNumber("limit", mcp.Description("Maximum results (default 20, max 100)"), mcp.Max(100)),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	// memory_write (Phase 6)
	writeTool := mcp.NewTool("memory_write",
		mcp.WithDescription("Direct trusted write to Lethe (requires memory.write scope)"),
		mcp.WithString("project", mcp.Required(), mcp.Description("Target project")),
		mcp.WithString("event_type", mcp.Required(), mcp.Description("Event type (e.g., record, log, checkpoint)")),
		mcp.WithString("kind", mcp.Description("Kind of memory (e.g., decision, observation, task)")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Memory content")),
		mcp.WithArray("tags", mcp.Description("Tags for categorization"), mcp.WithStringItems()),
		mcp.WithString("idempotency_key", mcp.Required(), mcp.Description("Client-generated unique key for this write")),
	)

	// Register all tools
	s.mcpServer.AddTool(searchTool, s.handleSearch)
	s.mcpServer.AddTool(getTool, s.handleGet)
	s.mcpServer.AddTool(recentTool, s.handleRecent)
	s.mcpServer.AddTool(threadListTool, s.handleThreadList)
	s.mcpServer.AddTool(threadGetTool, s.handleThreadGet)
	s.mcpServer.AddTool(threadEventsTool, s.handleThreadEvents)
	s.mcpServer.AddTool(proposeTool, s.handlePropose)
	s.mcpServer.AddTool(checkpointTool, s.handleCheckpoint)
	s.mcpServer.AddTool(listProposalsTool, s.handleListProposals)
	s.mcpServer.AddTool(writeTool, s.handleWrite)

	// Memory Git V1 tools
	s.registerMemoryGitTools()
}

// checkIdempotency handles the idempotency check with race-condition protection.
// If the key exists with matching input, returns the existing record.
// If the key exists with different input, returns an error.
func (s *Server) checkIdempotency(ctx context.Context, principalID, operation, key string, args any) (*idempotency.Record, error) {
	check, err := s.idempotency.Check(ctx, principalID, operation, key, args)
	if err != nil {
		return nil, fmt.Errorf("idempotency check failed: %w", err)
	}
	if check.Found {
		if !check.Matching {
			return nil, fmt.Errorf("idempotency key exists with different input")
		}
		return check.Record, nil
	}
	return nil, nil // not found
}

// storeIdempotency stores the idempotency record, handling concurrent races.
// If another request already stored the key, it re-checks and returns the existing record.
func (s *Server) storeIdempotency(ctx context.Context, principalID, operation, key string, args any, status, resourceID string) (*idempotency.Record, error) {
	err := s.idempotency.Store(ctx, principalID, operation, key, args, status, resourceID, "")
	if err == nil {
		return nil, nil // stored successfully
	}
	if err == idempotency.ErrDuplicateKey {
		// Lost the race - another request stored it first. Re-check.
		return s.checkIdempotency(ctx, principalID, operation, key, args)
	}
	return nil, fmt.Errorf("store idempotency failed: %w", err)
}

func (s *Server) handlePropose(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)

	if err := policy.RequireScope(principal, "memory.propose"); err != nil {
		s.recordLedger(reqID, principal, "memory_propose", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	args := request.GetArguments()
	project, _ := args["project"].(string)
	if err := policy.RequireProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_propose", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	idempotencyKey, _ := args["idempotency_key"].(string)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("missing required argument: idempotency_key")
	}

	// Check idempotency
	existing, err := s.checkIdempotency(ctx, principal.ID, "memory_propose", idempotencyKey, args)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// Return existing proposal
		found, err := s.proposalSvc.FindByIdempotency(ctx, principal.ID, idempotencyKey)
		if err != nil {
			return nil, fmt.Errorf("find existing: %w", err)
		}
		data, _ := json.MarshalIndent(map[string]interface{}{"proposal_id": found.ID, "status": found.Status, "existing": true}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	// Build provenance
	prov := proposal.Provenance{
		Principal:     principal.ID,
		Session:       reqID,
		CaptureMethod: "memory_propose",
		AuthorType:    "model",
	}

	var tags []string
	if t, ok := args["tags"].([]interface{}); ok {
		for _, v := range t {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	confidence := 0.0
	if c, ok := args["confidence"].(float64); ok {
		confidence = c
	}

	createReq := proposal.CreateRequest{
		ProjectID:      project,
		EventType:      getString(args, "event_type"),
		Kind:           getString(args, "kind"),
		Content:        getString(args, "content"),
		Reason:         getString(args, "reason"),
		Confidence:     confidence,
		Tags:           tags,
		Provenance:     prov,
		AgentID:        principal.AgentID, // Attribution for external agents
		IdempotencyKey: idempotencyKey,
		RequestID:      reqID,
	}

	p, err := s.proposalSvc.Create(ctx, createReq)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_propose", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("create proposal failed: %w", err)
	}

	// Store idempotency record
	existingIdem, err := s.storeIdempotency(ctx, principal.ID, "memory_propose", idempotencyKey, args, "pending", p.ID)
	if err != nil {
		return nil, err
	}
	if existingIdem != nil {
		// Lost the race - return the winner's result
		data, _ := json.MarshalIndent(map[string]interface{}{"proposal_id": existingIdem.ResultingResourceID, "status": "pending", "existing": true}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	data, _ := json.MarshalIndent(map[string]interface{}{"proposal_id": p.ID, "status": p.Status}, "", "  ")
	s.recordLedger(reqID, principal, "memory_propose", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleCheckpoint(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)

	if err := policy.RequireScope(principal, "memory.propose"); err != nil {
		s.recordLedger(reqID, principal, "memory_checkpoint", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	args := request.GetArguments()
	project, _ := args["project"].(string)
	if err := policy.RequireProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_checkpoint", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	idempotencyKey, _ := args["idempotency_key"].(string)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("missing required argument: idempotency_key")
	}

	// Check idempotency
	existing, err := s.checkIdempotency(ctx, principal.ID, "memory_checkpoint", idempotencyKey, args)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		found, err := s.proposalSvc.FindByIdempotency(ctx, principal.ID, idempotencyKey)
		if err != nil {
			return nil, fmt.Errorf("find existing: %w", err)
		}
		data, _ := json.MarshalIndent(map[string]interface{}{"proposal_id": found.ID, "status": found.Status, "existing": true}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	// Build structured checkpoint content
	checkpoint := map[string]interface{}{
		"summary":        getString(args, "summary"),
		"decisions":      getStringSlice(args, "decisions"),
		"open_questions": getStringSlice(args, "open_questions"),
		"next_actions":   getStringSlice(args, "next_actions"),
	}
	content, _ := json.Marshal(checkpoint)

	prov := proposal.Provenance{
		Principal:     principal.ID,
		Session:       reqID,
		CaptureMethod: "memory_checkpoint",
		AuthorType:    "model",
	}

	createReq := proposal.CreateRequest{
		ProjectID:      project,
		EventType:      "checkpoint",
		Kind:           "checkpoint",
		Content:        string(content),
		Reason:         "Structured conversation checkpoint",
		Confidence:     1.0,
		Tags:           []string{"checkpoint"},
		Provenance:     prov,
		AgentID:        principal.AgentID, // Attribution for external agents
		IdempotencyKey: idempotencyKey,
		RequestID:      reqID,
	}

	p, err := s.proposalSvc.Create(ctx, createReq)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_checkpoint", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("create checkpoint failed: %w", err)
	}

	// Store idempotency record
	existingIdem, err := s.storeIdempotency(ctx, principal.ID, "memory_checkpoint", idempotencyKey, args, "pending", p.ID)
	if err != nil {
		return nil, err
	}
	if existingIdem != nil {
		// Lost the race - return the winner's result
		data, _ := json.MarshalIndent(map[string]interface{}{"proposal_id": existingIdem.ResultingResourceID, "status": "pending", "existing": true}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	data, _ := json.MarshalIndent(map[string]interface{}{"proposal_id": p.ID, "status": p.Status}, "", "  ")
	s.recordLedger(reqID, principal, "memory_checkpoint", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleListProposals(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)

	if err := policy.RequireScope(principal, "proposal.review"); err != nil {
		s.recordLedger(reqID, principal, "proposal_list", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	args := request.GetArguments()
	statusStr := getString(args, "status")
	if statusStr == "" {
		statusStr = string(proposal.StatusPending)
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	proposals, err := s.proposalSvc.List(ctx, proposal.Status(statusStr), limit)
	if err != nil {
		s.recordLedger(reqID, principal, "proposal_list", nil, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("list proposals failed: %w", err)
	}

	// Filter to principal's authorized projects
	filtered := make([]*proposal.Proposal, 0, len(proposals))
	for _, p := range proposals {
		if principal.HasProject(p.ProjectID) {
			filtered = append(filtered, p)
		}
	}

	resp := map[string]interface{}{
		"proposals": filtered,
		"count":     len(filtered),
	}
	data, _ := json.MarshalIndent(resp, "", "  ")
	s.recordLedger(reqID, principal, "proposal_list", nil, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleWrite(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)

	if err := policy.RequireScope(principal, "memory.write"); err != nil {
		s.recordLedger(reqID, principal, "memory_write", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	args := request.GetArguments()
	project, _ := args["project"].(string)
	if err := policy.RequireProject(principal, project); err != nil {
		s.recordLedger(reqID, principal, "memory_write", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	idempotencyKey, _ := args["idempotency_key"].(string)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("missing required argument: idempotency_key")
	}

	// Check idempotency
	existing, err := s.checkIdempotency(ctx, principal.ID, "memory_write", idempotencyKey, args)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		data, _ := json.MarshalIndent(map[string]interface{}{
			"event_id": existing.ResultingResourceID,
			"existing": true,
		}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	var tags []string
	if t, ok := args["tags"].([]interface{}); ok {
		for _, v := range t {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	letheReq := lethe.CreateEventRequest{
		Project: project,
		Type:    getString(args, "event_type"),
		Kind:    getString(args, "kind"),
		Content: getString(args, "content"),
		Tags:    tags,
	}

	// Validate content size
	if len(letheReq.Content) > s.cfg.MaxResultBytes {
		return nil, fmt.Errorf("content exceeds maximum size of %d bytes", s.cfg.MaxResultBytes)
	}

	// Write to Lethe
	event, err := s.lethe.CreateEvent(ctx, letheReq)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_write", []string{project}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("write failed: %w", err)
	}

	// Store idempotency record
	existingIdem, err := s.storeIdempotency(ctx, principal.ID, "memory_write", idempotencyKey, args, "success", event.ID)
	if err != nil {
		return nil, err
	}
	if existingIdem != nil {
		// Lost the race - return the winner's result
		data, _ := json.MarshalIndent(map[string]interface{}{
			"event_id": existingIdem.ResultingResourceID,
			"project":  event.Project,
			"status":   "created",
			"existing": true,
		}, "", "  ")
		s.recordLedger(reqID, principal, "memory_write", []string{project}, "success", time.Since(start))
		return mcp.NewToolResultText(string(data)), nil
	}

	data, _ := json.MarshalIndent(map[string]interface{}{
		"event_id": event.ID,
		"project":  event.Project,
		"status":   "created",
	}, "", "  ")
	s.recordLedger(reqID, principal, "memory_write", []string{project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func getString(args map[string]interface{}, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func getStringSlice(args map[string]interface{}, key string) []string {
	var result []string
	if arr, ok := args[key].([]interface{}); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				result = append(result, s)
			}
		}
	}
	return result
}

// ... rest of existing handlers ...

func (s *Server) handleSearch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)

	if err := policy.RequireScope(principal, "memory.search"); err != nil {
		s.recordLedger(reqID, principal, "memory_search", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	args := request.GetArguments()
	query, _ := args["query"].(string)

	var requestedProjects []string
	if p, ok := args["projects"].([]interface{}); ok {
		for _, v := range p {
			if s, ok := v.(string); ok {
				requestedProjects = append(requestedProjects, s)
			}
		}
	}

	var eventTypes []string
	if t, ok := args["event_types"].([]interface{}); ok {
		for _, v := range t {
			if s, ok := v.(string); ok {
				eventTypes = append(eventTypes, s)
			}
		}
	}

	var tags []string
	if t, ok := args["tags"].([]interface{}); ok {
		for _, v := range t {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	if limit > s.cfg.MaxResultCount {
		limit = s.cfg.MaxResultCount
	}
	if limit <= 0 {
		limit = 20
	}

	projects, err := policy.IntersectProjects(principal, requestedProjects)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_search", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	letheReq := lethe.SearchRequest{
		Query:      query,
		Projects:   projects,
		EventTypes: eventTypes,
		Tags:       tags,
		Limit:      limit,
	}

	result, err := s.lethe.SearchEvents(ctx, letheReq)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_search", projects, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("search failed: %w", err)
	}

	filtered := make([]lethe.Event, 0, len(result.Events))
	for _, ev := range result.Events {
		if principal.HasReadProject(ev.Project) {
			filtered = append(filtered, ev)
		}
	}

	resp := map[string]interface{}{
		"events": filtered,
		"total":  len(filtered),
		"count":  len(filtered),
	}

	data, _ := json.MarshalIndent(resp, "", "  ")
	s.recordLedger(reqID, principal, "memory_search", projects, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleGet(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)

	if err := policy.RequireScope(principal, "memory.read"); err != nil {
		s.recordLedger(reqID, principal, "memory_get", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	args := request.GetArguments()
	eventID, _ := args["id"].(string)
	if eventID == "" {
		return nil, fmt.Errorf("missing required argument: id")
	}

	event, err := s.lethe.GetEvent(ctx, eventID)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_get", nil, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("get failed: %w", err)
	}

	if !principal.HasReadProject(event.Project) {
		s.recordLedger(reqID, principal, "memory_get", []string{event.Project}, "denied: cross-project", time.Since(start))
		return nil, fmt.Errorf("unauthorized: event not in authorized project")
	}

	data, _ := json.MarshalIndent(event, "", "  ")
	s.recordLedger(reqID, principal, "memory_get", []string{event.Project}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleRecent(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)

	if err := policy.RequireScope(principal, "memory.read"); err != nil {
		s.recordLedger(reqID, principal, "memory_recent", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	args := request.GetArguments()

	var requestedProjects []string
	if p, ok := args["projects"].([]interface{}); ok {
		for _, v := range p {
			if s, ok := v.(string); ok {
				requestedProjects = append(requestedProjects, s)
			}
		}
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	if limit > s.cfg.MaxResultCount {
		limit = s.cfg.MaxResultCount
	}
	if limit <= 0 {
		limit = 20
	}

	projects, err := policy.IntersectProjects(principal, requestedProjects)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_recent", nil, "denied: project access", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	letheReq := lethe.RecentRequest{
		Projects: projects,
		Limit:    limit,
	}

	result, err := s.lethe.RecentEvents(ctx, letheReq)
	if err != nil {
		s.recordLedger(reqID, principal, "memory_recent", projects, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("recent failed: %w", err)
	}

	filtered := make([]lethe.Event, 0, len(result.Events))
	for _, ev := range result.Events {
		if principal.HasReadProject(ev.Project) {
			filtered = append(filtered, ev)
		}
	}

	resp := map[string]interface{}{
		"events": filtered,
		"count":  len(filtered),
	}

	data, _ := json.MarshalIndent(resp, "", "  ")
	s.recordLedger(reqID, principal, "memory_recent", projects, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleThreadList(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "thread.read"); err != nil {
		s.recordLedger(reqID, principal, "thread_list", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	args := request.GetArguments()
	sessionID := getString(args, "session_id")
	if sessionID == "" {
		return nil, fmt.Errorf("missing required argument: session_id")
	}
	session, err := s.lethe.GetSession(ctx, sessionID)
	if err != nil {
		s.recordLedger(reqID, principal, "thread_list", nil, "error: "+sanitizeError(err), time.Since(start))
		return nil, hideThreadAccessError(err)
	}
	if err := policy.RequireReadProject(principal, session.ProjectID); err != nil {
		s.recordLedger(reqID, principal, "thread_list", []string{session.ProjectID}, "denied: project access", time.Since(start))
		return nil, hideThreadAccessError(err)
	}

	threads, err := s.lethe.ListThreads(ctx, sessionID, getString(args, "status"))
	if err != nil {
		s.recordLedger(reqID, principal, "thread_list", []string{session.ProjectID}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("list threads failed: %w", err)
	}
	data, _ := json.MarshalIndent(map[string]interface{}{"threads": threads, "count": len(threads)}, "", "  ")
	s.recordLedger(reqID, principal, "thread_list", []string{session.ProjectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) authorizeThread(ctx context.Context, principal auth.Principal, threadID string) (*lethe.Thread, string, error) {
	thread, err := s.lethe.GetThread(ctx, threadID)
	if err != nil {
		return nil, "", hideThreadAccessError(err)
	}
	session, err := s.lethe.GetSession(ctx, thread.SessionID)
	if err != nil {
		return nil, "", hideThreadAccessError(err)
	}
	if err := policy.RequireReadProject(principal, session.ProjectID); err != nil {
		return nil, session.ProjectID, hideThreadAccessError(err)
	}
	return thread, session.ProjectID, nil
}

func (s *Server) handleThreadGet(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "thread.read"); err != nil {
		s.recordLedger(reqID, principal, "thread_get", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	threadID := getString(request.GetArguments(), "id")
	if threadID == "" {
		return nil, fmt.Errorf("missing required argument: id")
	}
	thread, projectID, err := s.authorizeThread(ctx, principal, threadID)
	if err != nil {
		s.recordLedger(reqID, principal, "thread_get", []string{projectID}, "denied or failed: "+sanitizeError(threadAccessCause(err)), time.Since(start))
		return nil, fmt.Errorf("thread access failed: %w", err)
	}
	data, _ := json.MarshalIndent(thread, "", "  ")
	s.recordLedger(reqID, principal, "thread_get", []string{projectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleThreadEvents(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	principal := authFromContext(ctx)
	reqID := requestIDFromContext(ctx)
	if err := policy.RequireScope(principal, "thread.read"); err != nil {
		s.recordLedger(reqID, principal, "thread_events", nil, "denied: missing scope", time.Since(start))
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	threadID := getString(request.GetArguments(), "id")
	if threadID == "" {
		return nil, fmt.Errorf("missing required argument: id")
	}
	_, projectID, err := s.authorizeThread(ctx, principal, threadID)
	if err != nil {
		s.recordLedger(reqID, principal, "thread_events", []string{projectID}, "denied or failed: "+sanitizeError(threadAccessCause(err)), time.Since(start))
		return nil, fmt.Errorf("thread access failed: %w", err)
	}
	thread, events, err := s.lethe.GetThreadEvents(ctx, threadID)
	if err != nil {
		s.recordLedger(reqID, principal, "thread_events", []string{projectID}, "error: "+sanitizeError(err), time.Since(start))
		return nil, fmt.Errorf("get thread events failed: %w", err)
	}
	filtered := make([]lethe.Event, 0, len(events))
	for _, event := range events {
		if principal.HasReadProject(event.Project) {
			filtered = append(filtered, event)
		}
	}
	data, _ := json.MarshalIndent(map[string]interface{}{"thread": thread, "events": filtered, "count": len(filtered)}, "", "  ")
	s.recordLedger(reqID, principal, "thread_events", []string{projectID}, "success", time.Since(start))
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) recordLedger(reqID string, principal auth.Principal, operation string, projects []string, result string, latency time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := s.ledger.Record(ctx, ledger.Entry{
		RequestID:   reqID,
		PrincipalID: principal.ID,
		Operation:   operation,
		Projects:    projects,
		Result:      result,
		Latency:     latency,
	}); err != nil {
		slog.Error("audit ledger write failed",
			"request_id", reqID,
			"principal", principal.ID,
			"operation", operation,
			"error", sanitizeError(err),
		)
	}
}

// sanitizeError redacts potentially sensitive information from error strings
// before they are recorded in the ledger.
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Redact any URLs that might contain credentials
	if strings.Contains(msg, "://") {
		// Simple heuristic: if it looks like a URL with credentials, redact
		msg = "upstream error"
	}
	// Limit length to prevent abuse
	if len(msg) > 256 {
		msg = msg[:256] + "..."
	}
	return msg
}

// Context key types for auth middleware.
type principalKey struct{}
type requestIDKey struct{}

func authFromContext(ctx context.Context) auth.Principal {
	if p, ok := ctx.Value(principalKey{}).(auth.Principal); ok {
		return p
	}
	return auth.Principal{}
}

func requestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithAuth sets principal and request ID in context.
func WithAuth(ctx context.Context, principal auth.Principal, requestID string) context.Context {
	ctx = context.WithValue(ctx, principalKey{}, principal)
	ctx = context.WithValue(ctx, requestIDKey{}, requestID)
	return ctx
}

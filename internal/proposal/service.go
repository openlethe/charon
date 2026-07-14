package proposal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openlethe/charon/internal/store"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

// Status represents a proposal's lifecycle state.
type Status string

const (
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusRejected  Status = "rejected"
	StatusCancelled Status = "cancelled"
	StatusExpired   Status = "expired"
)

// ReviewFinding is an immutable review comment attached to a proposal.
type ReviewFinding struct {
	Reviewer  string    `json:"reviewer"`
	Comment   string    `json:"comment"`
	Severity  string    `json:"severity,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Proposal represents the existing Charon proposal lifecycle. Memory Git
// merge requests use the optional fields at the end; they are not a separate
// proposal system.
type Proposal struct {
	ID                    string
	ProjectID             string
	ProposedEventType     string
	Kind                  string
	Content               string
	Reason                string
	Confidence            float64
	Tags                  []string
	OriginatingPrincipal  string
	OriginatingClient     string
	OriginatingProvider   string
	OriginatingSession    string
	RequestID             string
	IdempotencyKey        string
	Status                Status
	CreatedAt             time.Time
	DecidedAt             *time.Time
	DecidedBy             string
	ResultingLetheEventID string

	ChangesetID     string
	BaseChangesetID string
	HeadChangesetID string
	TargetRef       string
	SelectedOps     []int
	ConflictIDs     []string
	ReviewFindings  []ReviewFinding
}

// Provenance tracks who/what created the proposal.
type Provenance struct {
	Provider      string `json:"provider"`
	Client        string `json:"client"`
	Principal     string `json:"principal_id"`
	Session       string `json:"session_id"`
	CaptureMethod string `json:"capture_method"`
	AuthorType    string `json:"author_type"`
}

// Service manages the single Charon proposal lifecycle.
type Service struct {
	db    *store.DB
	lethe *lethe.Client
}

// NewService creates a proposal service.
func NewService(db *store.DB, letheClient *lethe.Client) *Service {
	return &Service{db: db, lethe: letheClient}
}

// CreateRequest is used to create a memory proposal or Memory Git merge request.
type CreateRequest struct {
	ProjectID      string
	EventType      string
	Kind           string
	Content        string
	Reason         string
	Confidence     float64
	Tags           []string
	Provenance     Provenance
	AgentID        string // Maps to Lethe agent_id for attribution
	IdempotencyKey string
	RequestID      string

	ChangesetID     string
	BaseChangesetID string
	HeadChangesetID string
	TargetRef       string
	SelectedOps     []int
	ConflictIDs     []string
}

const proposalColumns = `
	id, project_id, proposed_event_type, kind, content, reason, confidence,
	tags, originating_principal, originating_client, originating_provider,
	originating_session, request_id, idempotency_key, status, created_at,
	decided_at, decided_by, resulting_lethe_event_id,
	changeset_id, base_changeset_id, head_changeset_id, target_ref,
	selected_ops_json, conflict_ids_json, review_findings_json`

// Create inserts a new proposal into Charon's existing quarantine.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Proposal, error) {
	if req.ProjectID == "" {
		return nil, errors.New("project_id required")
	}
	if req.Kind != "merge_request" && req.Content == "" {
		return nil, errors.New("content required")
	}
	if req.Kind == "merge_request" && (req.HeadChangesetID == "" || req.TargetRef == "") {
		return nil, errors.New("merge_request requires head_changeset_id and target_ref")
	}
	if req.Provenance.Principal == "" {
		return nil, errors.New("originating principal required")
	}
	if req.IdempotencyKey == "" {
		return nil, errors.New("idempotency_key required")
	}

	p := &Proposal{
		ID:                   uuid.Must(uuid.NewV7()).String(),
		ProjectID:            req.ProjectID,
		ProposedEventType:    req.EventType,
		Kind:                 req.Kind,
		Content:              req.Content,
		Reason:               req.Reason,
		Confidence:           req.Confidence,
		Tags:                 nonNilStrings(req.Tags),
		OriginatingPrincipal: req.Provenance.Principal,
		OriginatingClient:    req.Provenance.Client,
		OriginatingProvider:  req.Provenance.Provider,
		OriginatingSession:   req.Provenance.Session,
		RequestID:            req.RequestID,
		IdempotencyKey:       req.IdempotencyKey,
		Status:               StatusPending,
		CreatedAt:            time.Now().UTC(),
		ChangesetID:          req.ChangesetID,
		BaseChangesetID:      req.BaseChangesetID,
		HeadChangesetID:      req.HeadChangesetID,
		TargetRef:            req.TargetRef,
		SelectedOps:          nonNilInts(req.SelectedOps),
		ConflictIDs:          nonNilStrings(req.ConflictIDs),
		ReviewFindings:       []ReviewFinding{},
	}

	tagsJSON, _ := json.Marshal(p.Tags)
	selectedJSON, _ := json.Marshal(p.SelectedOps)
	conflictsJSON, _ := json.Marshal(p.ConflictIDs)
	findingsJSON, _ := json.Marshal(p.ReviewFindings)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO proposals (`+proposalColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.ProjectID, p.ProposedEventType, p.Kind, p.Content, p.Reason,
		p.Confidence, string(tagsJSON), p.OriginatingPrincipal, p.OriginatingClient,
		p.OriginatingProvider, p.OriginatingSession, p.RequestID, p.IdempotencyKey,
		string(p.Status), p.CreatedAt, nullString(p.ChangesetID), nullString(p.BaseChangesetID),
		nullString(p.HeadChangesetID), nullString(p.TargetRef), string(selectedJSON),
		string(conflictsJSON), string(findingsJSON))
	if err != nil {
		return nil, fmt.Errorf("insert proposal: %w", err)
	}
	return p, nil
}

// FindByIdempotency looks up an existing proposal by idempotency key.
func (s *Service) FindByIdempotency(ctx context.Context, principalID, key string) (*Proposal, error) {
	p, err := scanProposal(s.db.QueryRowContext(ctx, `SELECT `+proposalColumns+`
		FROM proposals WHERE originating_principal = ? AND idempotency_key = ?`, principalID, key))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup proposal: %w", err)
	}
	return p, nil
}

// List returns proposals filtered by status.
func (s *Service) List(ctx context.Context, status Status, limit int) ([]*Proposal, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := `SELECT ` + proposalColumns + ` FROM proposals`
	args := []any{}
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, string(status))
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	defer rows.Close()

	var out []*Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, fmt.Errorf("scan proposal: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ApproveRequest contains the decision to approve a legacy memory proposal.
type ApproveRequest struct {
	ProposalID string
	DecidedBy  string
}

// Approve retains the existing memory-proposal behavior. Memory Git merge
// requests are applied through CompleteMerge after the protected ref CAS.
func (s *Service) Approve(ctx context.Context, req ApproveRequest) (*Proposal, error) {
	p, err := s.Get(ctx, req.ProposalID)
	if err != nil {
		return nil, err
	}
	if p.Kind == "merge_request" {
		return nil, errors.New("merge requests must be applied through the Memory Git merge path")
	}
	if p.Status != StatusPending {
		return p, fmt.Errorf("proposal already %s", p.Status)
	}
	// Preserve the existing v0 behavior until canonical event materialization is
	// moved behind the same idempotent service boundary.
	return s.complete(ctx, p, StatusApproved, req.DecidedBy, "placeholder-event-id")
}

// CompleteMerge marks a merge request approved after Lethe has atomically
// advanced the protected ref. Self-approval is rejected here as defense in depth.
func (s *Service) CompleteMerge(ctx context.Context, proposalID, decidedBy, resultingChangesetID string) (*Proposal, error) {
	p, err := s.Get(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if p.Kind != "merge_request" {
		return nil, errors.New("proposal is not a merge request")
	}
	if p.OriginatingPrincipal == decidedBy {
		return nil, errors.New("proposal originator cannot approve its own merge")
	}
	if p.Status != StatusPending {
		return p, fmt.Errorf("proposal already %s", p.Status)
	}
	if resultingChangesetID == "" {
		return nil, errors.New("resulting_changeset_id required")
	}
	if p.ChangesetID != "" && p.ChangesetID != resultingChangesetID {
		return nil, errors.New("resulting changeset does not match persisted merge result")
	}
	return s.complete(ctx, p, StatusApproved, decidedBy, resultingChangesetID)
}

func (s *Service) complete(ctx context.Context, p *Proposal, status Status, decidedBy, resultID string) (*Proposal, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE proposals
		SET status = ?, decided_at = ?, decided_by = ?, resulting_lethe_event_id = ?
		WHERE id = ? AND status = ?`, string(status), now, decidedBy, resultID, p.ID, string(StatusPending))
	if err != nil {
		return nil, fmt.Errorf("complete proposal: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("proposal not found or already decided: %s", p.ID)
	}
	return s.Get(ctx, p.ID)
}

// Reject marks a proposal rejected without changing Lethe history.
func (s *Service) Reject(ctx context.Context, req RejectRequest) (*Proposal, error) {
	return s.transition(ctx, req.ProposalID, StatusRejected, req.DecidedBy)
}

// RejectRequest contains a terminal proposal decision.
type RejectRequest struct {
	ProposalID string
	DecidedBy  string
}

// Cancel preserves the proposal and its source branch while closing review.
func (s *Service) Cancel(ctx context.Context, proposalID, principal string) (*Proposal, error) {
	p, err := s.Get(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if p.OriginatingPrincipal != principal {
		return nil, errors.New("only the proposal originator may cancel it")
	}
	return s.transition(ctx, proposalID, StatusCancelled, principal)
}

// Expire preserves history while closing a stale proposal.
func (s *Service) Expire(ctx context.Context, proposalID, decidedBy string) (*Proposal, error) {
	return s.transition(ctx, proposalID, StatusExpired, decidedBy)
}

func (s *Service) transition(ctx context.Context, id string, status Status, decidedBy string) (*Proposal, error) {
	if id == "" {
		return nil, errors.New("proposal_id required")
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE proposals SET status = ?, decided_at = ?, decided_by = ?
		WHERE id = ? AND status = ?`, string(status), now, decidedBy, id, string(StatusPending))
	if err != nil {
		return nil, fmt.Errorf("transition proposal: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("proposal not found or already decided: %s", id)
	}
	return s.Get(ctx, id)
}

// AddReviewFinding appends an attributed finding to a pending proposal.
func (s *Service) AddReviewFinding(ctx context.Context, proposalID string, finding ReviewFinding) (*Proposal, error) {
	p, err := s.Get(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusPending {
		return nil, fmt.Errorf("proposal already %s", p.Status)
	}
	if finding.Reviewer == "" || finding.Comment == "" {
		return nil, errors.New("reviewer and comment required")
	}
	if finding.CreatedAt.IsZero() {
		finding.CreatedAt = time.Now().UTC()
	}
	p.ReviewFindings = append(p.ReviewFindings, finding)
	b, _ := json.Marshal(p.ReviewFindings)
	if _, err := s.db.ExecContext(ctx, `UPDATE proposals SET review_findings_json = ? WHERE id = ? AND status = ?`,
		string(b), proposalID, string(StatusPending)); err != nil {
		return nil, fmt.Errorf("attach review finding: %w", err)
	}
	return s.Get(ctx, proposalID)
}

// SetConflictIDs replaces the conflict snapshot after a merge-time recheck.
func (s *Service) SetConflictIDs(ctx context.Context, proposalID string, ids []string) (*Proposal, error) {
	p, err := s.Get(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusPending {
		return nil, fmt.Errorf("proposal already %s", p.Status)
	}
	b, _ := json.Marshal(nonNilStrings(ids))
	if _, err := s.db.ExecContext(ctx, `UPDATE proposals SET conflict_ids_json = ? WHERE id = ? AND status = ?`,
		string(b), proposalID, string(StatusPending)); err != nil {
		return nil, fmt.Errorf("update conflict ids: %w", err)
	}
	return s.Get(ctx, proposalID)
}

// SetMergeResult persists the exact changeset that a pending merge will move
// the protected ref to. It closes the retry window between immutable
// changeset creation and the cross-service protected CAS.
func (s *Service) SetMergeResult(ctx context.Context, proposalID, changesetID string) (*Proposal, error) {
	if changesetID == "" {
		return nil, errors.New("changeset_id required")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE proposals SET changeset_id = ?
		WHERE id = ? AND status = ? AND (changeset_id IS NULL OR changeset_id = '' OR changeset_id = ?)`,
		changesetID, proposalID, string(StatusPending), changesetID)
	if err != nil {
		return nil, fmt.Errorf("persist merge result: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("proposal merge result already differs or proposal is not pending: %s", proposalID)
	}
	return s.Get(ctx, proposalID)
}

// Get retrieves a single proposal by ID.
func (s *Service) Get(ctx context.Context, id string) (*Proposal, error) {
	p, err := scanProposal(s.db.QueryRowContext(ctx, `SELECT `+proposalColumns+` FROM proposals WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("proposal not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("load proposal: %w", err)
	}
	return p, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProposal(row rowScanner) (*Proposal, error) {
	var p Proposal
	var tagsJSON, selectedJSON, conflictsJSON, findingsJSON sql.NullString
	var createdAt string
	var decidedAt, decidedBy, resultingID sql.NullString
	var changesetID, baseID, headID, targetRef sql.NullString
	if err := row.Scan(
		&p.ID, &p.ProjectID, &p.ProposedEventType, &p.Kind, &p.Content, &p.Reason,
		&p.Confidence, &tagsJSON, &p.OriginatingPrincipal, &p.OriginatingClient,
		&p.OriginatingProvider, &p.OriginatingSession, &p.RequestID, &p.IdempotencyKey,
		&p.Status, &createdAt, &decidedAt, &decidedBy, &resultingID,
		&changesetID, &baseID, &headID, &targetRef, &selectedJSON, &conflictsJSON, &findingsJSON,
	); err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(createdAt)
	if decidedAt.Valid {
		t := parseTime(decidedAt.String)
		p.DecidedAt = &t
	}
	if decidedBy.Valid {
		p.DecidedBy = decidedBy.String
	}
	if resultingID.Valid {
		p.ResultingLetheEventID = resultingID.String
	}
	if changesetID.Valid {
		p.ChangesetID = changesetID.String
	}
	if baseID.Valid {
		p.BaseChangesetID = baseID.String
	}
	if headID.Valid {
		p.HeadChangesetID = headID.String
	}
	if targetRef.Valid {
		p.TargetRef = targetRef.String
	}
	_ = json.Unmarshal([]byte(tagsJSON.String), &p.Tags)
	_ = json.Unmarshal([]byte(selectedJSON.String), &p.SelectedOps)
	_ = json.Unmarshal([]byte(conflictsJSON.String), &p.ConflictIDs)
	_ = json.Unmarshal([]byte(findingsJSON.String), &p.ReviewFindings)
	p.Tags = nonNilStrings(p.Tags)
	p.SelectedOps = nonNilInts(p.SelectedOps)
	p.ConflictIDs = nonNilStrings(p.ConflictIDs)
	if p.ReviewFindings == nil {
		p.ReviewFindings = []ReviewFinding{}
	}
	return &p, nil
}

func parseTime(v string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}

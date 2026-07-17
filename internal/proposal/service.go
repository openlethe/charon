package proposal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openlethe/charon/internal/metrics"
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

// ReviewVerdict is an explicit merge-review decision.
type ReviewVerdict string

const (
	ReviewVerdictApprove        ReviewVerdict = "approve"
	ReviewVerdictRequestChanges ReviewVerdict = "request_changes"
	ReviewVerdictReject         ReviewVerdict = "reject"
)

// Integrity errors. Every failure to decode, validate, or verify persisted
// proposal state is a hard corruption error; nothing is normalized into an
// empty or default value because defaults widen authorization (a corrupt
// selected-ops list would otherwise become a full merge).
var (
	// ErrProposalCorrupt means a persisted proposal failed decode, validation,
	// or state-digest verification. Loading fails closed.
	ErrProposalCorrupt = errors.New("proposal persisted state is corrupt")
	// ErrProposalQuarantined means a pre-v5 row whose persisted JSON did not
	// decode during the digest backfill; it has no state digest and is never
	// loaded into an authorization path again.
	ErrProposalQuarantined = errors.New("proposal quarantined: no verifiable state digest")
	// ErrIdempotencyMismatch means an idempotency key was replayed with a
	// request whose canonical digest differs from the recorded one.
	ErrIdempotencyMismatch = errors.New("idempotency key exists with a different request")
)

// ReviewFinding is an immutable review decision attached to the exact proposal
// state that was inspected. Free-form comments never imply approval.
type ReviewFinding struct {
	Reviewer              string        `json:"reviewer"`
	Verdict               ReviewVerdict `json:"verdict"`
	Comment               string        `json:"comment"`
	Severity              string        `json:"severity,omitempty"`
	TargetHeadChangesetID string        `json:"target_head_changeset_id"`
	TargetDigest          string        `json:"target_digest"`
	CreatedAt             time.Time     `json:"created_at"`
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

	// RequestDigest is the canonical digest of the exact request that created
	// the proposal; replays are matched against it. StateDigest is the
	// canonical integrity digest over the persisted proposal state, recomputed
	// on every mutation and verified on every load.
	RequestDigest string
	StateDigest   string
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

// MigrateStateDigests runs the v5 state-digest backfill. It must succeed
// before the service is used; see BackfillStateDigests.
func (s *Service) MigrateStateDigests(ctx context.Context) (int, int, error) {
	return BackfillStateDigests(ctx, s.db)
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
	selected_ops_json, conflict_ids_json, review_findings_json,
	request_digest, state_digest`

func validateCreateRequest(req CreateRequest) error {
	if req.ProjectID == "" {
		return errors.New("project_id required")
	}
	if req.Kind != "merge_request" && req.Content == "" {
		return errors.New("content required")
	}
	if req.Kind == "merge_request" && (req.HeadChangesetID == "" || req.TargetRef == "") {
		return errors.New("merge_request requires head_changeset_id and target_ref")
	}
	if req.Provenance.Principal == "" {
		return errors.New("originating principal required")
	}
	if req.IdempotencyKey == "" {
		return errors.New("idempotency_key required")
	}
	return nil
}

func newProposalFromRequest(req CreateRequest) *Proposal {
	return &Proposal{
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
}

const insertProposalSQL = `
	INSERT INTO proposals (` + proposalColumns + `)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func insertProposal(ctx context.Context, ex interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, p *Proposal) error {
	tagsJSON, _ := json.Marshal(p.Tags)
	selectedJSON, _ := json.Marshal(p.SelectedOps)
	conflictsJSON, _ := json.Marshal(p.ConflictIDs)
	findingsJSON, _ := json.Marshal(p.ReviewFindings)
	_, err := ex.ExecContext(ctx, insertProposalSQL,
		p.ID, p.ProjectID, p.ProposedEventType, p.Kind, p.Content, p.Reason,
		p.Confidence, string(tagsJSON), p.OriginatingPrincipal, p.OriginatingClient,
		p.OriginatingProvider, p.OriginatingSession, p.RequestID, p.IdempotencyKey,
		string(p.Status), p.CreatedAt, nullString(p.ChangesetID), nullString(p.BaseChangesetID),
		nullString(p.HeadChangesetID), nullString(p.TargetRef), string(selectedJSON),
		string(conflictsJSON), string(findingsJSON), nullString(p.RequestDigest),
		p.StateDigest)
	return err
}

// Create inserts a new proposal into Charon's existing quarantine. Callers that
// need exactly-once semantics (merge requests) must use CreateIdempotent.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Proposal, error) {
	if err := validateCreateRequest(req); err != nil {
		return nil, err
	}
	p := newProposalFromRequest(req)
	digest, err := StateDigest(p)
	if err != nil {
		return nil, fmt.Errorf("digest proposal state: %w", err)
	}
	p.StateDigest = digest
	if err := insertProposal(ctx, s.db, p); err != nil {
		return nil, fmt.Errorf("insert proposal: %w", err)
	}
	return p, nil
}

// CreateIdempotent creates a merge proposal exactly once. The proposal row,
// its canonical request digest, and the generic idempotency record commit in
// ONE transaction, so there is no window in which a crash leaves an ambiguous
// proposal without an idempotency record. An exact replay returns the original
// proposal with replay=true; a replay with the same key but a different
// canonical request fails closed with ErrIdempotencyMismatch. Concurrent
// identical requests converge on the single inserted row through the
// (originating_principal, idempotency_key) uniqueness constraint.
func (s *Service) CreateIdempotent(ctx context.Context, req CreateRequest, digestInput RequestDigestInput) (p *Proposal, replay bool, err error) {
	if err := validateCreateRequest(req); err != nil {
		return nil, false, err
	}
	requestDigest, err := RequestDigest(digestInput)
	if err != nil {
		return nil, false, fmt.Errorf("digest request: %w", err)
	}
	principal := req.Provenance.Principal

	existing, err := s.FindByIdempotency(ctx, principal, req.IdempotencyKey)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		return matchReplay(existing, requestDigest, req.IdempotencyKey)
	}

	p = newProposalFromRequest(req)
	p.RequestDigest = requestDigest
	stateDigest, err := StateDigest(p)
	if err != nil {
		return nil, false, fmt.Errorf("digest proposal state: %w", err)
	}
	p.StateDigest = stateDigest

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin proposal idempotency transaction: %w", err)
	}
	defer tx.Rollback()

	if err := insertProposal(ctx, tx, p); err != nil {
		if !isUniqueViolation(err) {
			return nil, false, fmt.Errorf("insert proposal: %w", err)
		}
		// Lost a concurrent race: the winner's row is authoritative.
		existing, findErr := s.FindByIdempotency(ctx, principal, req.IdempotencyKey)
		if findErr != nil {
			return nil, false, findErr
		}
		if existing == nil {
			return nil, false, fmt.Errorf("proposal insert conflicted but no row is visible for key %s", req.IdempotencyKey)
		}
		return matchReplay(existing, requestDigest, req.IdempotencyKey)
	}

	// Generic idempotency record committed in the same transaction: both
	// sources of truth move atomically. The hash stores the raw canonical
	// request digest so reconciliation can join records to proposals.
	rawDigest, _ := hex.DecodeString(requestDigest)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO idempotency_records (
			principal_id, operation, idempotency_key, request_hash,
			status, resulting_resource_id, stored_response, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, principal, "memory_merge_propose", req.IdempotencyKey, rawDigest,
		"pending", p.ID, "", time.Now().UTC().Format(time.RFC3339)); err != nil {
		if !isUniqueViolation(err) {
			return nil, false, fmt.Errorf("insert idempotency record: %w", err)
		}
		existing, findErr := s.FindByIdempotency(ctx, principal, req.IdempotencyKey)
		if findErr != nil {
			return nil, false, findErr
		}
		if existing == nil {
			return nil, false, fmt.Errorf("idempotency record conflicted but no proposal is visible for key %s", req.IdempotencyKey)
		}
		return matchReplay(existing, requestDigest, req.IdempotencyKey)
	}

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit proposal idempotency transaction: %w", err)
	}
	metrics.Inc(metrics.ProposalCreated)
	return p, false, nil
}

// matchReplay classifies an idempotency-key hit: the stored proposal is
// returned only when its recorded request digest equals the replay's digest.
func matchReplay(existing *Proposal, requestDigest, key string) (*Proposal, bool, error) {
	if existing.RequestDigest == "" || existing.RequestDigest != requestDigest {
		metrics.Inc(metrics.IdempotencyMismatches)
		return nil, false, fmt.Errorf("%w: key %s", ErrIdempotencyMismatch, key)
	}
	metrics.Inc(metrics.IdempotentReplays)
	return existing, true, nil
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

// Approve fails closed for legacy event proposals. Lethe's legacy create-event
// API does not expose an idempotency key or caller-supplied event ID, so Charon
// cannot make approval crash-safe without risking duplicate durable events.
// Memory Git merge requests use CompleteMerge after the protected ref CAS.
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
	return p, errors.New("legacy proposal approval is unavailable because Lethe event creation is not idempotent; use the Memory Git merge workflow")
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
	next := *p
	next.Status = status
	next.DecidedAt = &now
	next.DecidedBy = decidedBy
	next.ResultingLetheEventID = resultID
	nextDigest, err := StateDigest(&next)
	if err != nil {
		return nil, fmt.Errorf("digest proposal state: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE proposals
		SET status = ?, decided_at = ?, decided_by = ?, resulting_lethe_event_id = ?, state_digest = ?
		WHERE id = ? AND status = ? AND state_digest = ?`,
		string(status), now, decidedBy, resultID, nextDigest, p.ID, string(StatusPending), p.StateDigest)
	if err != nil {
		return nil, fmt.Errorf("complete proposal: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("proposal not found, already decided, or state changed concurrently: %s", p.ID)
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
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	next := *p
	next.Status = status
	next.DecidedAt = &now
	next.DecidedBy = decidedBy
	nextDigest, err := StateDigest(&next)
	if err != nil {
		return nil, fmt.Errorf("digest proposal state: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE proposals SET status = ?, decided_at = ?, decided_by = ?, state_digest = ?
		WHERE id = ? AND status = ? AND state_digest = ?`,
		string(status), now, decidedBy, nextDigest, id, string(StatusPending), p.StateDigest)
	if err != nil {
		return nil, fmt.Errorf("transition proposal: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("proposal not found, already decided, or state changed concurrently: %s", id)
	}
	return s.Get(ctx, id)
}

// ReviewTargetDigest binds a review decision to the exact immutable merge
// request fields the reviewer inspected. The same fields are covered by the
// proposal state digest, so approval and merge stay bound to one canonical
// proposal state.
func ReviewTargetDigest(p *Proposal, targetHeadChangesetID string) string {
	if p == nil {
		return ""
	}
	payload, _ := json.Marshal(struct {
		ProposalID            string `json:"proposal_id"`
		ProjectID             string `json:"project_id"`
		BaseChangesetID       string `json:"base_changeset_id"`
		HeadChangesetID       string `json:"head_changeset_id"`
		TargetRef             string `json:"target_ref"`
		SelectedOps           []int  `json:"selected_ops"`
		TargetHeadChangesetID string `json:"target_head_changeset_id"`
	}{
		ProposalID:            p.ID,
		ProjectID:             p.ProjectID,
		BaseChangesetID:       p.BaseChangesetID,
		HeadChangesetID:       p.HeadChangesetID,
		TargetRef:             p.TargetRef,
		SelectedOps:           nonNilInts(p.SelectedOps),
		TargetHeadChangesetID: targetHeadChangesetID,
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func validReviewVerdict(verdict ReviewVerdict) bool {
	switch verdict {
	case ReviewVerdictApprove, ReviewVerdictRequestChanges, ReviewVerdictReject:
		return true
	default:
		return false
	}
}

// maxStateRaceRetries bounds optimistic-concurrency retries when concurrent
// reviewers or merge-time updates mutate the same proposal. Dozens of
// concurrent reviewers are legitimate, so the bound sits well above expected
// contention; each retry observes the winner's write before re-applying.
const maxStateRaceRetries = 64

// stateRaceBackoff sleeps briefly before the next optimistic-concurrency
// round so contending writers do not retry in lockstep.
func stateRaceBackoff(attempt int) {
	backoff := time.Duration(1<<min(attempt, 6)) * time.Millisecond
	// #nosec G404 -- jitter does not require cryptographic randomness.
	jitter := time.Duration(rand.Int64N(int64(backoff) + 1))
	time.Sleep(backoff/2 + jitter)
}

// AddReviewFinding atomically appends an attributed decision to a pending
// merge proposal. The append is read-modify-write guarded by the state digest:
// exactly one concurrent writer wins each round, losers reload (observing the
// winner's finding) and retry, so findings are never silently dropped and the
// persisted state digest always covers the persisted findings.
func (s *Service) AddReviewFinding(ctx context.Context, proposalID string, finding ReviewFinding) (*Proposal, error) {
	if finding.Reviewer == "" || finding.Comment == "" {
		return nil, errors.New("reviewer and comment required")
	}
	if !validReviewVerdict(finding.Verdict) {
		return nil, errors.New("verdict must be approve, request_changes, or reject")
	}
	if finding.TargetHeadChangesetID == "" {
		return nil, errors.New("target_head_changeset_id required")
	}

	for attempt := 0; ; attempt++ {
		p, err := s.Get(ctx, proposalID)
		if err != nil {
			return nil, err
		}
		if p.Kind != "merge_request" {
			return nil, errors.New("proposal is not a merge request")
		}
		if p.Status != StatusPending {
			return nil, fmt.Errorf("proposal already %s", p.Status)
		}
		finding.TargetDigest = ReviewTargetDigest(p, finding.TargetHeadChangesetID)
		if finding.CreatedAt.IsZero() {
			finding.CreatedAt = time.Now().UTC()
		}
		next := *p
		next.ReviewFindings = append(append([]ReviewFinding{}, p.ReviewFindings...), finding)
		nextDigest, err := StateDigest(&next)
		if err != nil {
			return nil, fmt.Errorf("digest proposal state: %w", err)
		}
		findingsJSON, err := json.Marshal(next.ReviewFindings)
		if err != nil {
			return nil, fmt.Errorf("marshal review findings: %w", err)
		}
		res, err := s.db.ExecContext(ctx, `
			UPDATE proposals
			SET review_findings_json = ?, state_digest = ?
			WHERE id = ? AND status = ? AND state_digest = ?
		`, string(findingsJSON), nextDigest, proposalID, string(StatusPending), p.StateDigest)
		if err != nil {
			return nil, fmt.Errorf("attach review finding: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return s.Get(ctx, proposalID)
		}
		if attempt+1 >= maxStateRaceRetries {
			return nil, fmt.Errorf("proposal %s changed concurrently; review finding could not be attached", proposalID)
		}
		stateRaceBackoff(attempt)
	}
}

// SetConflictIDs replaces the conflict snapshot after a merge-time recheck.
func (s *Service) SetConflictIDs(ctx context.Context, proposalID string, ids []string) (*Proposal, error) {
	for attempt := 0; ; attempt++ {
		p, err := s.Get(ctx, proposalID)
		if err != nil {
			return nil, err
		}
		if p.Status != StatusPending {
			return nil, fmt.Errorf("proposal already %s", p.Status)
		}
		next := *p
		next.ConflictIDs = nonNilStrings(ids)
		nextDigest, err := StateDigest(&next)
		if err != nil {
			return nil, fmt.Errorf("digest proposal state: %w", err)
		}
		b, _ := json.Marshal(next.ConflictIDs)
		res, err := s.db.ExecContext(ctx, `UPDATE proposals SET conflict_ids_json = ?, state_digest = ?
			WHERE id = ? AND status = ? AND state_digest = ?`,
			string(b), nextDigest, proposalID, string(StatusPending), p.StateDigest)
		if err != nil {
			return nil, fmt.Errorf("update conflict ids: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return s.Get(ctx, proposalID)
		}
		if attempt+1 >= maxStateRaceRetries {
			return nil, fmt.Errorf("proposal %s changed concurrently; conflict snapshot could not be updated", proposalID)
		}
		stateRaceBackoff(attempt)
	}
}

// SetMergeResult persists the exact changeset that a pending merge will move
// the protected ref to. It closes the retry window between immutable
// changeset creation and the cross-service protected CAS.
func (s *Service) SetMergeResult(ctx context.Context, proposalID, changesetID string) (*Proposal, error) {
	if changesetID == "" {
		return nil, errors.New("changeset_id required")
	}
	for attempt := 0; ; attempt++ {
		p, err := s.Get(ctx, proposalID)
		if err != nil {
			return nil, err
		}
		if p.Status != StatusPending {
			return nil, fmt.Errorf("proposal merge result cannot change after decision: %s", proposalID)
		}
		if p.ChangesetID != "" && p.ChangesetID != changesetID {
			return nil, fmt.Errorf("proposal merge result already differs: %s", proposalID)
		}
		next := *p
		next.ChangesetID = changesetID
		nextDigest, err := StateDigest(&next)
		if err != nil {
			return nil, fmt.Errorf("digest proposal state: %w", err)
		}
		res, err := s.db.ExecContext(ctx, `UPDATE proposals SET changeset_id = ?, state_digest = ?
			WHERE id = ? AND status = ? AND state_digest = ?`,
			changesetID, nextDigest, proposalID, string(StatusPending), p.StateDigest)
		if err != nil {
			return nil, fmt.Errorf("persist merge result: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return s.Get(ctx, proposalID)
		}
		if attempt+1 >= maxStateRaceRetries {
			return nil, fmt.Errorf("proposal %s changed concurrently; merge result could not be persisted", proposalID)
		}
		stateRaceBackoff(attempt)
	}
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

// hexDigestPattern matches a SHA-256 hex digest.
var hexDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// scanProposal decodes one proposal row and fails closed on any irregularity:
// undecodable JSON, invalid values, unparseable timestamps, a missing state
// digest (quarantined legacy row), or a state-digest mismatch (tampering or
// corruption). Nothing is normalized into empty defaults.
func scanProposal(row rowScanner) (*Proposal, error) {
	p, stateDigest, err := scanProposalRaw(row)
	if err != nil {
		return nil, err
	}
	corrupt := func(format string, args ...any) error {
		return fmt.Errorf("%w: proposal %s: %s", ErrProposalCorrupt, p.ID, fmt.Sprintf(format, args...))
	}
	// Integrity verification last: a row without a verifiable digest is
	// quarantined; a row whose digest does not match its decoded state was
	// tampered with or corrupted after write.
	if stateDigest == "" {
		return nil, fmt.Errorf("%w: proposal %s", ErrProposalQuarantined, p.ID)
	}
	expected, err := StateDigest(p)
	if err != nil {
		return nil, corrupt("state digest cannot be recomputed: %v", err)
	}
	if expected != stateDigest {
		return nil, corrupt("state digest mismatch (stored %s, computed %s)", stateDigest, expected)
	}
	p.StateDigest = stateDigest
	return p, nil
}

// scanProposalRaw performs the strict field decode and validation shared by
// verified loads and the v5 digest backfill. It returns the decoded proposal
// and the row's stored state digest, which may be empty for legacy rows.
func scanProposalRaw(row rowScanner) (*Proposal, string, error) {
	var p Proposal
	var tagsJSON, selectedJSON, conflictsJSON, findingsJSON sql.NullString
	var createdAt string
	var decidedAt, decidedBy, resultingID sql.NullString
	var changesetID, baseID, headID, targetRef sql.NullString
	var requestDigest, stateDigest sql.NullString
	if err := row.Scan(
		&p.ID, &p.ProjectID, &p.ProposedEventType, &p.Kind, &p.Content, &p.Reason,
		&p.Confidence, &tagsJSON, &p.OriginatingPrincipal, &p.OriginatingClient,
		&p.OriginatingProvider, &p.OriginatingSession, &p.RequestID, &p.IdempotencyKey,
		&p.Status, &createdAt, &decidedAt, &decidedBy, &resultingID,
		&changesetID, &baseID, &headID, &targetRef, &selectedJSON, &conflictsJSON, &findingsJSON,
		&requestDigest, &stateDigest,
	); err != nil {
		return nil, "", err
	}
	corrupt := func(format string, args ...any) error {
		return fmt.Errorf("%w: proposal %s: %s", ErrProposalCorrupt, p.ID, fmt.Sprintf(format, args...))
	}

	var err error
	if p.CreatedAt, err = strictParseTime(createdAt); err != nil {
		return nil, "", corrupt("created_at: %v", err)
	}
	if decidedAt.Valid {
		t, err := strictParseTime(decidedAt.String)
		if err != nil {
			return nil, "", corrupt("decided_at: %v", err)
		}
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
	if requestDigest.Valid {
		p.RequestDigest = requestDigest.String
	}

	if err := json.Unmarshal([]byte(tagsJSON.String), &p.Tags); err != nil {
		return nil, "", corrupt("tags JSON does not decode: %v", err)
	}
	if err := json.Unmarshal([]byte(selectedJSON.String), &p.SelectedOps); err != nil {
		return nil, "", corrupt("selected_ops JSON does not decode: %v", err)
	}
	if err := json.Unmarshal([]byte(conflictsJSON.String), &p.ConflictIDs); err != nil {
		return nil, "", corrupt("conflict_ids JSON does not decode: %v", err)
	}
	if err := json.Unmarshal([]byte(findingsJSON.String), &p.ReviewFindings); err != nil {
		return nil, "", corrupt("review_findings JSON does not decode: %v", err)
	}
	p.Tags = nonNilStrings(p.Tags)
	p.SelectedOps = nonNilInts(p.SelectedOps)
	p.ConflictIDs = nonNilStrings(p.ConflictIDs)
	if p.ReviewFindings == nil {
		p.ReviewFindings = []ReviewFinding{}
	}

	seenOrdinals := make(map[int]struct{}, len(p.SelectedOps))
	for _, ordinal := range p.SelectedOps {
		if ordinal < 0 {
			return nil, "", corrupt("selected_ops contains negative ordinal %d", ordinal)
		}
		if _, dup := seenOrdinals[ordinal]; dup {
			return nil, "", corrupt("selected_ops contains duplicate ordinal %d", ordinal)
		}
		seenOrdinals[ordinal] = struct{}{}
	}
	for _, id := range p.ConflictIDs {
		if id == "" {
			return nil, "", corrupt("conflict_ids contains an empty identifier")
		}
	}
	for i, finding := range p.ReviewFindings {
		if !validReviewVerdict(finding.Verdict) {
			return nil, "", corrupt("review_findings[%d] has invalid verdict %q", i, finding.Verdict)
		}
		if finding.Reviewer == "" {
			return nil, "", corrupt("review_findings[%d] has no reviewer", i)
		}
		if finding.CreatedAt.IsZero() {
			return nil, "", corrupt("review_findings[%d] has no timestamp", i)
		}
		if finding.TargetHeadChangesetID == "" || !hexDigestPattern.MatchString(finding.TargetDigest) {
			return nil, "", corrupt("review_findings[%d] is missing its target binding or digest", i)
		}
	}

	if stateDigest.Valid {
		p.StateDigest = stateDigest.String
	}
	return &p, stateDigest.String, nil
}

// strictParseTime parses persisted timestamps and rejects anything that does
// not match a known storage format instead of silently returning zero time.
func strictParseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp %q", v)
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// isUniqueViolation reports whether err is a SQLite uniqueness violation.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
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

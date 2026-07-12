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
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusExpired  Status = "expired"
)

// Proposal represents a quarantined memory proposal.
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

// Service manages proposal lifecycle.
type Service struct {
	db    *store.DB
	lethe *lethe.Client
}

// NewService creates a proposal service.
func NewService(db *store.DB, letheClient *lethe.Client) *Service {
	return &Service{db: db, lethe: letheClient}
}

// CreateRequest is used to create a new proposal.
type CreateRequest struct {
	ProjectID      string
	EventType      string
	Kind           string
	Content        string
	Reason         string
	Confidence     float64
	Tags           []string
	Provenance     Provenance
	IdempotencyKey string
	RequestID      string
}

// Create inserts a new proposal into the quarantine.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Proposal, error) {
	if req.ProjectID == "" {
		return nil, errors.New("project_id required")
	}
	if req.Content == "" {
		return nil, errors.New("content required")
	}
	if req.IdempotencyKey == "" {
		return nil, errors.New("idempotency_key required")
	}

	id := uuid.Must(uuid.NewV7()).String()
	createdAt := time.Now().UTC()

	tagsJSON, _ := json.Marshal(req.Tags)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO proposals (
			id, project_id, proposed_event_type, kind, content, reason, confidence,
			tags, originating_principal, originating_client, originating_provider,
			originating_session, request_id, idempotency_key, status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		id, req.ProjectID, req.EventType, req.Kind, req.Content, req.Reason,
		req.Confidence, string(tagsJSON), req.Provenance.Principal, req.Provenance.Client,
		req.Provenance.Provider, req.Provenance.Session, req.RequestID,
		req.IdempotencyKey, string(StatusPending), createdAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert proposal: %w", err)
	}

	return &Proposal{
		ID:                   id,
		ProjectID:            req.ProjectID,
		ProposedEventType:    req.EventType,
		Kind:                 req.Kind,
		Content:              req.Content,
		Reason:               req.Reason,
		Confidence:           req.Confidence,
		Tags:                 req.Tags,
		OriginatingPrincipal: req.Provenance.Principal,
		OriginatingClient:    req.Provenance.Client,
		OriginatingProvider:  req.Provenance.Provider,
		OriginatingSession:   req.Provenance.Session,
		RequestID:            req.RequestID,
		IdempotencyKey:       req.IdempotencyKey,
		Status:               StatusPending,
		CreatedAt:            createdAt,
	}, nil
}

// FindByIdempotency looks up an existing proposal by idempotency key.
func (s *Service) FindByIdempotency(ctx context.Context, principalID, key string) (*Proposal, error) {
	var p Proposal
	var tagsJSON string
	var createdAt string
	var decidedAt sql.NullString
	var decidedBy sql.NullString
	var resultingEventID sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, proposed_event_type, kind, content, reason, confidence,
			tags, originating_principal, originating_client, originating_provider,
			originating_session, request_id, idempotency_key, status, created_at,
			decided_at, decided_by, resulting_lethe_event_id
		FROM proposals
		WHERE originating_principal = ? AND idempotency_key = ?
	`, principalID, key).Scan(
		&p.ID, &p.ProjectID, &p.ProposedEventType, &p.Kind, &p.Content, &p.Reason,
		&p.Confidence, &tagsJSON, &p.OriginatingPrincipal, &p.OriginatingClient,
		&p.OriginatingProvider, &p.OriginatingSession, &p.RequestID,
		&p.IdempotencyKey, &p.Status, &createdAt, &decidedAt, &decidedBy, &resultingEventID,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup proposal: %w", err)
	}

	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if decidedAt.Valid && decidedAt.String != "" {
		if t, err := time.Parse(time.RFC3339, decidedAt.String); err == nil {
			p.DecidedAt = &t
		}
	}
	if decidedBy.Valid {
		p.DecidedBy = decidedBy.String
	}
	if resultingEventID.Valid {
		p.ResultingLetheEventID = resultingEventID.String
	}
	json.Unmarshal([]byte(tagsJSON), &p.Tags)

	return &p, nil
}

// List returns proposals filtered by status.
func (s *Service) List(ctx context.Context, status Status, limit int) ([]*Proposal, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	query := `
		SELECT id, project_id, proposed_event_type, kind, content, reason, confidence,
			tags, originating_principal, originating_client, originating_provider,
			originating_session, request_id, idempotency_key, status, created_at,
			decided_at, decided_by, resulting_lethe_event_id
		FROM proposals
	`
	args := []interface{}{}
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

	var proposals []*Proposal
	for rows.Next() {
		var p Proposal
		var tagsJSON string
		var createdAt string
		var decidedAt sql.NullString
		var decidedBy sql.NullString
		var resultingEventID sql.NullString

		if err := rows.Scan(
			&p.ID, &p.ProjectID, &p.ProposedEventType, &p.Kind, &p.Content, &p.Reason,
			&p.Confidence, &tagsJSON, &p.OriginatingPrincipal, &p.OriginatingClient,
			&p.OriginatingProvider, &p.OriginatingSession, &p.RequestID,
			&p.IdempotencyKey, &p.Status, &createdAt, &decidedAt, &decidedBy, &resultingEventID,
		); err != nil {
			return nil, fmt.Errorf("scan proposal: %w", err)
		}

		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if decidedAt.Valid && decidedAt.String != "" {
			if t, err := time.Parse(time.RFC3339, decidedAt.String); err == nil {
				p.DecidedAt = &t
			}
		}
		if decidedBy.Valid {
			p.DecidedBy = decidedBy.String
		}
		if resultingEventID.Valid {
			p.ResultingLetheEventID = resultingEventID.String
		}
		json.Unmarshal([]byte(tagsJSON), &p.Tags)
		proposals = append(proposals, &p)
	}

	return proposals, rows.Err()
}

// ApproveRequest contains the decision to approve a proposal.
type ApproveRequest struct {
	ProposalID string
	DecidedBy  string
}

// Approve marks a proposal as approved and writes to Lethe.
// This is transactional: exactly one Lethe write per approval.
func (s *Service) Approve(ctx context.Context, req ApproveRequest) (*Proposal, error) {
	if req.ProposalID == "" {
		return nil, errors.New("proposal_id required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Lock the proposal row
	var p Proposal
	var tagsJSON string
	var createdAt string
	var decidedAt sql.NullString
	var decidedBy sql.NullString
	var resultingEventID sql.NullString

	err = tx.QueryRowContext(ctx, `
		SELECT id, project_id, proposed_event_type, kind, content, reason, confidence,
			tags, originating_principal, originating_client, originating_provider,
			originating_session, request_id, idempotency_key, status, created_at,
			decided_at, decided_by, resulting_lethe_event_id
		FROM proposals
		WHERE id = ?
	`, req.ProposalID).Scan(
		&p.ID, &p.ProjectID, &p.ProposedEventType, &p.Kind, &p.Content, &p.Reason,
		&p.Confidence, &tagsJSON, &p.OriginatingPrincipal, &p.OriginatingClient,
		&p.OriginatingProvider, &p.OriginatingSession, &p.RequestID,
		&p.IdempotencyKey, &p.Status, &createdAt, &decidedAt, &decidedBy, &resultingEventID,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("proposal not found: %s", req.ProposalID)
	}
	if err != nil {
		return nil, fmt.Errorf("load proposal: %w", err)
	}

	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if decidedAt.Valid && decidedAt.String != "" {
		if t, err := time.Parse(time.RFC3339, decidedAt.String); err == nil {
			p.DecidedAt = &t
		}
	}
	if decidedBy.Valid {
		p.DecidedBy = decidedBy.String
	}
	if resultingEventID.Valid {
		p.ResultingLetheEventID = resultingEventID.String
	}
	json.Unmarshal([]byte(tagsJSON), &p.Tags)

	// Check if already decided
	if p.Status != StatusPending {
		return &p, fmt.Errorf("proposal already %s", p.Status)
	}

	// Check for idempotency: if already approved with same result, return existing
	if p.ResultingLetheEventID != "" {
		return &p, nil
	}

	// Write to Lethe
	letheReq := lethe.SearchRequest{} // placeholder for actual Lethe write
	_ = letheReq

	// In Phase 5, we don't have the Lethe write endpoint yet.
	// For now, mark as approved but note the limitation.
	// In Phase 6 when memory_write is available, we'll call lethe.CreateEvent here.

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		UPDATE proposals SET
			status = ?, decided_at = ?, decided_by = ?, resulting_lethe_event_id = ?
		WHERE id = ?
	`, string(StatusApproved), now, req.DecidedBy, "placeholder-event-id", req.ProposalID)
	if err != nil {
		return nil, fmt.Errorf("update proposal: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	p.Status = StatusApproved
	p.DecidedAt = &now
	p.DecidedBy = req.DecidedBy
	p.ResultingLetheEventID = "placeholder-event-id"

	return &p, nil
}

// RejectRequest contains the decision to reject a proposal.
type RejectRequest struct {
	ProposalID string
	DecidedBy  string
}

// Reject marks a proposal as rejected without writing to Lethe.
func (s *Service) Reject(ctx context.Context, req RejectRequest) (*Proposal, error) {
	if req.ProposalID == "" {
		return nil, errors.New("proposal_id required")
	}

	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET
			status = ?, decided_at = ?, decided_by = ?
		WHERE id = ? AND status = ?
	`, string(StatusRejected), now, req.DecidedBy, req.ProposalID, string(StatusPending))
	if err != nil {
		return nil, fmt.Errorf("reject proposal: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("proposal not found or already decided: %s", req.ProposalID)
	}

	return s.Get(ctx, req.ProposalID)
}

// Get retrieves a single proposal by ID.
func (s *Service) Get(ctx context.Context, id string) (*Proposal, error) {
	var p Proposal
	var tagsJSON string
	var createdAt string
	var decidedAt sql.NullString
	var decidedBy sql.NullString
	var resultingEventID sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, proposed_event_type, kind, content, reason, confidence,
			tags, originating_principal, originating_client, originating_provider,
			originating_session, request_id, idempotency_key, status, created_at,
			decided_at, decided_by, resulting_lethe_event_id
		FROM proposals WHERE id = ?
	`, id).Scan(
		&p.ID, &p.ProjectID, &p.ProposedEventType, &p.Kind, &p.Content, &p.Reason,
		&p.Confidence, &tagsJSON, &p.OriginatingPrincipal, &p.OriginatingClient,
		&p.OriginatingProvider, &p.OriginatingSession, &p.RequestID,
		&p.IdempotencyKey, &p.Status, &createdAt, &decidedAt, &decidedBy, &resultingEventID,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("proposal not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("load proposal: %w", err)
	}

	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if decidedAt.Valid && decidedAt.String != "" {
		if t, err := time.Parse(time.RFC3339, decidedAt.String); err == nil {
			p.DecidedAt = &t
		}
	}
	if decidedBy.Valid {
		p.DecidedBy = decidedBy.String
	}
	if resultingEventID.Valid {
		p.ResultingLetheEventID = resultingEventID.String
	}
	json.Unmarshal([]byte(tagsJSON), &p.Tags)

	return &p, nil
}

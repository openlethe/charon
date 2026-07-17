// Package audit implements the mandatory audit outbox: every security-relevant
// mutation records a durable intent before its external side effect and a
// completion, failure, or ambiguous outcome after. Mutations fail closed when
// the intent cannot persist; ambiguous outcomes stay visible until reconciled.
package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openlethe/charon/internal/store"
)

// IntentStatus is the lifecycle of one audited mutation.
type IntentStatus string

const (
	StatusIntent    IntentStatus = "intent"
	StatusCompleted IntentStatus = "completed"
	StatusFailed    IntentStatus = "failed"
	StatusAmbiguous IntentStatus = "ambiguous"
)

// Intent describes one security-relevant mutation before it happens.
type Intent struct {
	IntentID           string
	RequestID          string
	PrincipalID        string
	Operation          string
	ProjectID          string
	RequestDigest      string
	TargetResource     string
	IntendedTransition string
}

// Service persists audit intents and outcomes.
type Service struct {
	db      *store.DB
	ownerID string
}

// NewService creates an audit outbox service.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// RegisterInstance records this process as a live audit-intent owner. Charon
// deployments can run sibling gateways against one database, so restart
// reconciliation distinguishes live owners from dead ones by heartbeat.
// Returns this instance's owner ID; call Heartbeat periodically afterwards.
func (s *Service) RegisterInstance(ctx context.Context) (string, error) {
	if s.ownerID != "" {
		return s.ownerID, nil
	}
	s.ownerID = uuid.Must(uuid.NewV7()).String()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO charon_instances (instance_id, started_at, last_heartbeat)
		VALUES (?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET last_heartbeat = excluded.last_heartbeat
	`, s.ownerID, now, now)
	if err != nil {
		return "", fmt.Errorf("register audit instance: %w", err)
	}
	return s.ownerID, nil
}

// Heartbeat proves this process is alive so reconciliation never retires its
// in-flight intents. Best-effort: a failed heartbeat only matters if it keeps
// failing past the staleness TTL.
func (s *Service) Heartbeat(ctx context.Context) error {
	if s.ownerID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE charon_instances SET last_heartbeat = ? WHERE instance_id = ?
	`, time.Now().UTC().Format(time.RFC3339Nano), s.ownerID)
	return err
}

// heartbeatInterval is how often live instances should heartbeat; intents
// whose owner has not heartbeated within intentStaleAfter are reconcilable.
const (
	HeartbeatInterval = 15 * time.Second
	IntentStaleAfter  = 60 * time.Second
)

// BeginIntent durably records the intent to mutate. A failure means audit
// persistence is unavailable and the caller MUST abort the mutation — this is
// the fail-closed gate.
func (s *Service) BeginIntent(ctx context.Context, intent Intent) (*Intent, error) {
	if intent.PrincipalID == "" || intent.Operation == "" {
		return nil, errors.New("audit intent requires principal and operation")
	}
	if intent.IntentID == "" {
		intent.IntentID = uuid.Must(uuid.NewV7()).String()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_outbox (
			intent_id, request_id, principal_id, operation, project_id,
			request_digest, target_resource, intended_transition, status, created_at, owner_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'intent', ?, ?)
	`, intent.IntentID, intent.RequestID, intent.PrincipalID, intent.Operation,
		nullEmpty(intent.ProjectID), nullEmpty(intent.RequestDigest),
		nullEmpty(intent.TargetResource), nullEmpty(intent.IntendedTransition),
		time.Now().UTC().Format(time.RFC3339Nano), nullEmpty(s.ownerID))
	if err != nil {
		return nil, fmt.Errorf("persist audit intent: %w", err)
	}
	return &intent, nil
}

// Complete records a successful outcome.
func (s *Service) Complete(ctx context.Context, intentID, detail string) error {
	return s.finish(ctx, intentID, StatusCompleted, detail)
}

// Fail records a definitively failed outcome (the side effect did not happen).
func (s *Service) Fail(ctx context.Context, intentID, detail string) error {
	return s.finish(ctx, intentID, StatusFailed, detail)
}

// Ambiguous records an outcome that cannot be determined (for example the
// upstream call timed out after possibly committing). Ambiguous intents are
// the reconciliation queue.
func (s *Service) Ambiguous(ctx context.Context, intentID, detail string) error {
	return s.finish(ctx, intentID, StatusAmbiguous, detail)
}

func (s *Service) finish(ctx context.Context, intentID string, status IntentStatus, detail string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE audit_outbox SET status = ?, detail = ?, completed_at = ?
		WHERE intent_id = ? AND status = 'intent'
	`, string(status), nullEmpty(detail), time.Now().UTC().Format(time.RFC3339Nano), intentID)
	if err != nil {
		return fmt.Errorf("persist audit outcome: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("audit intent %s not found or already finished", intentID)
	}
	return nil
}

// ReconcileAfterRestart marks intents from DEAD owners as ambiguous — the
// owning process died between intent and completion, so the true outcome is
// unknown and must be investigated. Intents from live siblings (heartbeat
// fresher than staleAfter) are never touched; pre-ownership (NULL owner)
// intents come from processes that predate leases and are always reconciled.
// Returns the number reconciled.
func (s *Service) ReconcileAfterRestart(ctx context.Context, staleAfter time.Duration) (int, error) {
	if staleAfter <= 0 {
		staleAfter = IntentStaleAfter
	}
	cutoff := time.Now().UTC().Add(-staleAfter).Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
		UPDATE audit_outbox SET status = 'ambiguous',
			detail = COALESCE(detail, '') || 'reconciled after restart: outcome unknown'
		WHERE status = 'intent' AND (
			owner_id IS NULL OR owner_id = '' OR
			owner_id NOT IN (SELECT instance_id FROM charon_instances WHERE last_heartbeat > ?)
		)
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("reconcile audit outbox: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Backlog counts intents needing attention (ambiguous outcomes).
func (s *Service) Backlog(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_outbox WHERE status IN ('intent', 'ambiguous')
	`).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Entry is one outbox row for operator inspection.
type Entry struct {
	IntentID           string
	RequestID          string
	PrincipalID        string
	Operation          string
	ProjectID          string
	RequestDigest      string
	TargetResource     string
	IntendedTransition string
	Status             string
	Detail             string
	CreatedAt          time.Time
	CompletedAt        *time.Time
}

// List returns outbox entries, optionally filtered by status, newest first.
func (s *Service) List(ctx context.Context, status string, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := `
		SELECT intent_id, request_id, principal_id, operation,
			COALESCE(project_id, ''), COALESCE(request_digest, ''),
			COALESCE(target_resource, ''), COALESCE(intended_transition, ''),
			status, COALESCE(detail, ''), created_at, completed_at
		FROM audit_outbox`
	args := []any{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		var createdAt string
		var completedAt sql.NullString
		if err := rows.Scan(
			&e.IntentID, &e.RequestID, &e.PrincipalID, &e.Operation,
			&e.ProjectID, &e.RequestDigest, &e.TargetResource, &e.IntendedTransition,
			&e.Status, &e.Detail, &createdAt, &completedAt,
		); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if completedAt.Valid {
			if t, err := time.Parse(time.RFC3339Nano, completedAt.String); err == nil {
				e.CompletedAt = &t
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

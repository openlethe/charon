package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openlethe/charon/internal/store"
)

// Entry is a metadata-only ledger record.
type Entry struct {
	Sequence     int64
	CreatedAt    time.Time
	RequestID    string
	PrincipalID  string
	Operation    string
	Projects     []string
	Result       string
	Latency      time.Duration
	PreviousHash []byte
	EntryHash    []byte
}

// Service manages ledger entries.
type Service struct {
	db *store.DB
}

// NewService creates a ledger service.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// Record creates a new ledger entry with hash chaining.
func (s *Service) Record(ctx context.Context, entry Entry) error {
	now := time.Now().UTC()

	// Fetch previous hash
	var prevHash []byte
	row := s.db.QueryRowContext(ctx, "SELECT entry_hash FROM ledger_entries ORDER BY sequence DESC LIMIT 1")
	_ = row.Scan(&prevHash) // Ignore error for first entry

	// Build entry hash from metadata only (no content)
	h := sha256.New()
	h.Write([]byte(entry.RequestID))
	h.Write([]byte(entry.PrincipalID))
	h.Write([]byte(entry.Operation))
	projJSON, _ := json.Marshal(entry.Projects)
	h.Write(projJSON)
	h.Write([]byte(entry.Result))
	h.Write([]byte(fmt.Sprintf("%d", entry.Latency.Milliseconds())))
	h.Write([]byte(now.Format(time.RFC3339Nano)))
	if prevHash != nil {
		h.Write(prevHash)
	}
	entryHash := h.Sum(nil)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ledger_entries (
			created_at, request_id, principal_id, operation,
			authorized_projects_json, result, latency_ms,
			previous_hash, entry_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		now.Format(time.RFC3339Nano),
		entry.RequestID,
		entry.PrincipalID,
		entry.Operation,
		string(projJSON),
		entry.Result,
		entry.Latency.Milliseconds(),
		prevHash,
		entryHash,
	)
	if err != nil {
		return fmt.Errorf("insert ledger: %w", err)
	}
	return nil
}

// Tail returns the most recent ledger entries.
func (s *Service) Tail(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, created_at, request_id, principal_id, operation,
			authorized_projects_json, result, latency_ms, previous_hash, entry_hash
		FROM ledger_entries
		ORDER BY sequence DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query ledger: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var createdAt string
		var projJSON string
		var latencyMs int64

		if err := rows.Scan(
			&e.Sequence, &createdAt, &e.RequestID, &e.PrincipalID,
			&e.Operation, &projJSON, &e.Result, &latencyMs,
			&e.PreviousHash, &e.EntryHash,
		); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}

		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		e.Latency = time.Duration(latencyMs) * time.Millisecond
		json.Unmarshal([]byte(projJSON), &e.Projects)
		entries = append(entries, e)
	}

	return entries, rows.Err()
}

package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
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

// Service manages ledger entries. The mutex avoids needless lock contention
// within one process; BEGIN IMMEDIATE serializes the hash-chain read and append
// across every Charon process using the same SQLite database.
type Service struct {
	db *store.DB
	mu sync.Mutex
}

// NewService creates a ledger service.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// Record creates a new ledger entry with a serialized, transactional hash link.
func (s *Service) Record(ctx context.Context, entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire ledger connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin ledger append: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var prevHash []byte
	err = conn.QueryRowContext(ctx, "SELECT entry_hash FROM ledger_entries ORDER BY sequence DESC LIMIT 1").Scan(&prevHash)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("load previous ledger hash: %w", err)
	}
	if err == sql.ErrNoRows {
		prevHash = nil
	}

	now := time.Now().UTC()
	projectsJSON, err := json.Marshal(entry.Projects)
	if err != nil {
		return fmt.Errorf("marshal ledger projects: %w", err)
	}
	entryHash := digestEntry(entry, now, projectsJSON, prevHash)

	_, err = conn.ExecContext(ctx, `
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
		string(projectsJSON),
		entry.Result,
		entry.Latency.Milliseconds(),
		prevHash,
		entryHash,
	)
	if err != nil {
		return fmt.Errorf("insert ledger: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit ledger append: %w", err)
	}
	committed = true
	return nil
}

func digestEntry(entry Entry, createdAt time.Time, projectsJSON, previousHash []byte) []byte {
	payload, _ := json.Marshal(struct {
		RequestID    string `json:"request_id"`
		PrincipalID  string `json:"principal_id"`
		Operation    string `json:"operation"`
		ProjectsJSON []byte `json:"projects_json"`
		Result       string `json:"result"`
		LatencyMS    int64  `json:"latency_ms"`
		CreatedAt    string `json:"created_at"`
		PreviousHash []byte `json:"previous_hash"`
	}{
		RequestID:    entry.RequestID,
		PrincipalID:  entry.PrincipalID,
		Operation:    entry.Operation,
		ProjectsJSON: projectsJSON,
		Result:       entry.Result,
		LatencyMS:    entry.Latency.Milliseconds(),
		CreatedAt:    createdAt.UTC().Format(time.RFC3339Nano),
		PreviousHash: previousHash,
	})
	digest := sha256.Sum256(payload)
	return digest[:]
}

// legacyDigestEntry preserves verification for ledger rows written before the
// structured framing format was introduced. New rows always use digestEntry.
func legacyDigestEntry(entry Entry, createdAt time.Time, projectsJSON, previousHash []byte) []byte {
	h := sha256.New()
	h.Write([]byte(entry.RequestID))
	h.Write([]byte(entry.PrincipalID))
	h.Write([]byte(entry.Operation))
	h.Write(projectsJSON)
	h.Write([]byte(entry.Result))
	h.Write([]byte(fmt.Sprintf("%d", entry.Latency.Milliseconds())))
	h.Write([]byte(createdAt.UTC().Format(time.RFC3339Nano)))
	h.Write(previousHash)
	return h.Sum(nil)
}

// Verify checks every stored hash and predecessor link in sequence order.
func (s *Service) Verify(ctx context.Context) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, created_at, request_id, principal_id, operation,
			authorized_projects_json, result, latency_ms, previous_hash, entry_hash
		FROM ledger_entries
		ORDER BY sequence ASC
	`)
	if err != nil {
		return 0, fmt.Errorf("query ledger for verification: %w", err)
	}
	defer rows.Close()

	var count int64
	var previous []byte
	for rows.Next() {
		var entry Entry
		var createdAt string
		var projectsJSON []byte
		var latencyMS int64
		if err := rows.Scan(
			&entry.Sequence, &createdAt, &entry.RequestID, &entry.PrincipalID,
			&entry.Operation, &projectsJSON, &entry.Result, &latencyMS,
			&entry.PreviousHash, &entry.EntryHash,
		); err != nil {
			return count, fmt.Errorf("scan ledger sequence: %w", err)
		}
		parsedTime, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return count, fmt.Errorf("ledger sequence %d has invalid timestamp: %w", entry.Sequence, err)
		}
		if !bytes.Equal(entry.PreviousHash, previous) {
			return count, fmt.Errorf("ledger sequence %d has an invalid predecessor", entry.Sequence)
		}
		entry.Latency = time.Duration(latencyMS) * time.Millisecond
		expected := digestEntry(entry, parsedTime, projectsJSON, previous)
		legacyExpected := legacyDigestEntry(entry, parsedTime, projectsJSON, previous)
		if !bytes.Equal(entry.EntryHash, expected) && !bytes.Equal(entry.EntryHash, legacyExpected) {
			return count, fmt.Errorf("ledger sequence %d has an invalid entry hash", entry.Sequence)
		}
		previous = append([]byte(nil), entry.EntryHash...)
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("iterate ledger verification: %w", err)
	}
	return count, nil
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
		var entry Entry
		var createdAt string
		var projectsJSON string
		var latencyMS int64
		if err := rows.Scan(
			&entry.Sequence, &createdAt, &entry.RequestID, &entry.PrincipalID,
			&entry.Operation, &projectsJSON, &entry.Result, &latencyMS,
			&entry.PreviousHash, &entry.EntryHash,
		); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		entry.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		entry.Latency = time.Duration(latencyMS) * time.Millisecond
		_ = json.Unmarshal([]byte(projectsJSON), &entry.Projects)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

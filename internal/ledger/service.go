package ledger

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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
	KeyID        string
	EntryMAC     []byte
}

// Service manages ledger entries. The mutex avoids needless lock contention
// within one process; BEGIN IMMEDIATE serializes the hash-chain read and append
// across every Charon process using the same SQLite database.
type Service struct {
	db         *store.DB
	mu         sync.Mutex
	keys       map[string][]byte
	currentKID string
	anchorPath string
}

// NewService creates a ledger service.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// SetIntegrityKey configures the purpose-specific HMAC key that signs every
// new ledger entry. With it, an attacker who tampers with the database must
// also possess the key to recompute valid MACs; the unkeyed hash chain alone
// offers no such resistance. keyID identifies the key for rotation.
func (s *Service) SetIntegrityKey(key []byte, keyID string) {
	s.SetIntegrityKeys(map[string][]byte{keyID: key}, keyID)
}

// SetIntegrityKeys configures the full key map for rotation: entries name
// their key ID and every configured key verifies, while new entries are
// signed only with currentKeyID.
func (s *Service) SetIntegrityKeys(keys map[string][]byte, currentKeyID string) {
	s.keys = keys
	s.currentKID = currentKeyID
}

// signingKey returns the active signing key.
func (s *Service) signingKey() ([]byte, string) {
	if len(s.keys) == 0 {
		return nil, ""
	}
	if key, ok := s.keys[s.currentKID]; ok {
		return key, s.currentKID
	}
	return nil, ""
}

// SetAnchorPath configures an append-only JSONL file that records each
// committed tail (sequence, entry hash, MAC, key ID). A full-chain replacement
// of the database cannot forge the anchor, so periodic verification against it
// detects wholesale rollback. The anchor must live outside the database.
func (s *Service) SetAnchorPath(path string) {
	s.anchorPath = path
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
	var entryMAC []byte
	var keyID any
	if key, kid := s.signingKey(); len(key) > 0 {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(entryHash)
		entryMAC = mac.Sum(nil)
		keyID = kid
	}

	_, err = conn.ExecContext(ctx, `
		INSERT INTO ledger_entries (
			created_at, request_id, principal_id, operation,
			authorized_projects_json, result, latency_ms,
			previous_hash, entry_hash, key_id, entry_mac
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		keyID,
		entryMAC,
	)
	if err != nil {
		return fmt.Errorf("insert ledger: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit ledger append: %w", err)
	}
	committed = true

	if s.anchorPath != "" {
		if err := s.appendAnchor(now, entryHash, entryMAC); err != nil {
			// The ledger row is durable; a missing anchor line weakens rollback
			// detection until the next successful append and must be visible.
			return fmt.Errorf("ledger entry committed but external anchor append failed: %w", err)
		}
	}
	return nil
}

// appendAnchor appends the committed tail to the external anchor file.
func (s *Service) appendAnchor(createdAt time.Time, entryHash, entryMAC []byte) error {
	line, err := json.Marshal(map[string]any{
		"created_at": createdAt.UTC().Format(time.RFC3339Nano),
		"entry_hash": hex.EncodeToString(entryHash),
		"entry_mac":  hex.EncodeToString(entryMAC),
		"key_id":     s.currentKID,
	})
	if err != nil {
		return err
	}
	// #nosec G304 -- operator-configured anchor path.
	f, err := os.OpenFile(s.anchorPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, byte('\n'))); err != nil {
		return err
	}
	return f.Sync()
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
			authorized_projects_json, result, latency_ms, previous_hash, entry_hash,
			COALESCE(key_id, ''), entry_mac
		FROM ledger_entries
		ORDER BY sequence ASC
	`)
	if err != nil {
		return 0, fmt.Errorf("query ledger for verification: %w", err)
	}
	defer rows.Close()

	var count int64
	var previous []byte
	var seenMAC bool
	for rows.Next() {
		var entry Entry
		var createdAt string
		var projectsJSON []byte
		var latencyMS int64
		if err := rows.Scan(
			&entry.Sequence, &createdAt, &entry.RequestID, &entry.PrincipalID,
			&entry.Operation, &projectsJSON, &entry.Result, &latencyMS,
			&entry.PreviousHash, &entry.EntryHash, &entry.KeyID, &entry.EntryMAC,
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
		// Keyed MAC verification: rows written after v6 carry one and it must
		// verify under the key its key ID names. Rows before MAC support verify
		// by hash only — but once any MACed row exists, a later row missing MAC
		// material was stripped or forged, and fails closed.
		if len(entry.EntryMAC) == 0 && seenMAC {
			return count, fmt.Errorf("ledger sequence %d is missing MAC material after MACed entries exist (stripped or forged)", entry.Sequence)
		}
		if len(entry.EntryMAC) > 0 && len(s.keys) == 0 {
			// A MACed entry claims keyed protection; without the key it cannot
			// be verified, and silently skipping it would forge confidence.
			return count, fmt.Errorf("ledger sequence %d is MAC-signed but no ledger integrity key is configured", entry.Sequence)
		}
		if len(entry.EntryMAC) > 0 {
			key, ok := s.keys[entry.KeyID]
			if !ok {
				return count, fmt.Errorf("ledger sequence %d uses unknown key id %q", entry.Sequence, entry.KeyID)
			}
			mac := hmac.New(sha256.New, key)
			_, _ = mac.Write(entry.EntryHash)
			if !hmac.Equal(entry.EntryMAC, mac.Sum(nil)) {
				return count, fmt.Errorf("ledger sequence %d has an invalid entry MAC", entry.Sequence)
			}
			seenMAC = true
		}
		previous = append([]byte(nil), entry.EntryHash...)
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("iterate ledger verification: %w", err)
	}
	return count, nil
}

// AnchorLine is one record in the external anchor file.
type AnchorLine struct {
	CreatedAt string `json:"created_at"`
	EntryHash string `json:"entry_hash"`
	EntryMAC  string `json:"entry_mac"`
	KeyID     string `json:"key_id"`
}

// VerifyAnchor proves the ledger chain was not replaced wholesale by comparing
// the database tail against the external anchor. The newest anchor line must
// match the newest ledger entry; verification then walks the matching suffix
// and returns its length. A gap from a previously reported anchor-write
// failure shortens the suffix without masking a replacement: the tail check
// always runs. Keyed MACs on entries additionally prove no single row was
// modified.
func (s *Service) VerifyAnchor(ctx context.Context) (int, error) {
	if s.anchorPath == "" {
		return 0, nil
	}
	// #nosec G304 -- operator-configured anchor path.
	data, err := os.ReadFile(s.anchorPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read ledger anchor: %w", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	var anchorLines []AnchorLine
	for i, raw := range lines {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var line AnchorLine
		if err := json.Unmarshal(raw, &line); err != nil {
			return 0, fmt.Errorf("anchor line %d does not decode: %w", i+1, err)
		}
		anchorLines = append(anchorLines, line)
	}
	if len(anchorLines) == 0 {
		return 0, nil
	}

	// Load the newest len(anchorLines) ledger entries, oldest first.
	rows, err := s.db.QueryContext(ctx, `
		SELECT entry_hash, COALESCE(entry_mac, X'') FROM ledger_entries
		ORDER BY sequence DESC LIMIT ?
	`, len(anchorLines))
	if err != nil {
		return 0, fmt.Errorf("load ledger tail for anchor verification: %w", err)
	}
	defer rows.Close()
	type tailEntry struct {
		hash []byte
		mac  []byte
	}
	var tail []tailEntry
	for rows.Next() {
		var e tailEntry
		if err := rows.Scan(&e.hash, &e.mac); err != nil {
			return 0, err
		}
		tail = append(tail, e)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(tail) == 0 {
		if len(anchorLines) > 0 {
			return 0, fmt.Errorf("anchor records exist but the ledger is empty (chain replaced)")
		}
		return 0, nil
	}

	// Newest anchor line must equal the newest ledger entry.
	last := anchorLines[len(anchorLines)-1]
	lastHash, _ := hex.DecodeString(last.EntryHash)
	lastMAC, _ := hex.DecodeString(last.EntryMAC)
	if !bytes.Equal(tail[0].hash, lastHash) || !bytes.Equal(tail[0].mac, lastMAC) {
		return 0, fmt.Errorf("newest ledger entry does not match the external anchor (chain replaced or anchor tampered)")
	}

	// Count the contiguous matching suffix.
	matched := 1
	for i := 1; i < len(tail) && i < len(anchorLines); i++ {
		line := anchorLines[len(anchorLines)-1-i]
		hash, _ := hex.DecodeString(line.EntryHash)
		mac, _ := hex.DecodeString(line.EntryMAC)
		if !bytes.Equal(tail[i].hash, hash) || !bytes.Equal(tail[i].mac, mac) {
			break
		}
		matched++
	}
	return matched, nil
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

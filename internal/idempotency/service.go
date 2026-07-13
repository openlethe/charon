package idempotency

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openlethe/charon/internal/store"
)

// Record tracks an idempotent operation.
type Record struct {
	PrincipalID         string
	Operation           string
	IdempotencyKey      string
	RequestHash         string
	Status              string
	ResultingResourceID string
	StoredResponse      string
	CreatedAt           time.Time
}

// Service manages idempotency records.
type Service struct {
	db *store.DB
}

// NewService creates an idempotency service.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// CheckResult is returned when checking idempotency.
type CheckResult struct {
	Found    bool
	Matching bool // true if request hash matches
	Record   *Record
}

// Check looks up an existing idempotency record.
func (s *Service) Check(ctx context.Context, principalID, operation, key string, requestBody interface{}) (*CheckResult, error) {
	var r Record
	var reqHash []byte
	var createdAt string

	err := s.db.QueryRowContext(ctx, `
		SELECT principal_id, operation, idempotency_key, request_hash,
			status, resulting_resource_id, stored_response, created_at
		FROM idempotency_records
		WHERE principal_id = ? AND operation = ? AND idempotency_key = ?
	`, principalID, operation, key).Scan(
		&r.PrincipalID, &r.Operation, &r.IdempotencyKey, &reqHash,
		&r.Status, &r.ResultingResourceID, &r.StoredResponse, &createdAt,
	)
	if err == sql.ErrNoRows {
		return &CheckResult{Found: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup idempotency: %w", err)
	}

	r.RequestHash = hex.EncodeToString(reqHash)
	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	// Compute hash of current request
	expectedHash := hashRequestRaw(requestBody)

	// Backward compatibility: accept both hex-encoded and raw hashes.
	// Legacy rows stored the 64-byte hex string; new rows store raw 32-byte digest.
	storedHex := string(reqHash)
	decodedHash, decodeErr := hex.DecodeString(storedHex)
	if decodeErr == nil && len(decodedHash) == sha256.Size {
		// Stored value is valid hex of raw hash
		return &CheckResult{
			Found:    true,
			Matching: string(decodedHash) == string(expectedHash),
			Record:   &r,
		}, nil
	}

	// Fallback: compare raw bytes directly (new format)
	return &CheckResult{
		Found:    true,
		Matching: string(reqHash) == string(expectedHash),
		Record:   &r,
	}, nil
}

// Store saves an idempotency record after successful operation.
// Returns ErrDuplicateKey if the key already exists with a different hash.
func (s *Service) Store(ctx context.Context, principalID, operation, key string, requestBody interface{}, status, resourceID, response string) error {
	reqHash := hashRequestRaw(requestBody)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO idempotency_records (
			principal_id, operation, idempotency_key, request_hash,
			status, resulting_resource_id, stored_response, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		principalID, operation, key, reqHash,
		status, resourceID, response, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateKey
		}
		return fmt.Errorf("store idempotency: %w", err)
	}
	return nil
}

func hashRequestRaw(body interface{}) []byte {
	data, _ := json.Marshal(body)
	h := sha256.Sum256(data)
	return h[:]
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// SQLite unique constraint violation: error code 2067 (SQLITE_CONSTRAINT_UNIQUE)
	// The error message typically contains "UNIQUE constraint failed"
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func hashRequest(body interface{}) string {
	data, _ := json.Marshal(body)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Errors returned by idempotency checks.
var (
	ErrMismatchedKey = errors.New("idempotency key exists with different input")
	ErrDuplicateKey  = errors.New("idempotency key already exists")
)

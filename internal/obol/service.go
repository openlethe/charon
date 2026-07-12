package obol

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openlethe/charon/internal/store"
)

// ErrInvalidObol is returned when an obol is malformed or unverified.
var ErrInvalidObol = errors.New("invalid obol")

// Service manages obol lifecycle.
type Service struct {
	db      *store.DB
	hmacKey []byte
}

// NewService creates an obol service.
func NewService(db *store.DB, hmacKey string) *Service {
	return &Service{
		db:      db,
		hmacKey: []byte(hmacKey),
	}
}

// Obol represents a minted credential.
type Obol struct {
	ID          string
	PrincipalID string
	Audience    string
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	FullToken   string `json:"-"` // Only populated at mint time
}

// Mint creates a new obol for a principal.
func (s *Service) Mint(ctx context.Context, principalID, audience string, expiresIn time.Duration) (*Obol, error) {
	if principalID == "" {
		return nil, errors.New("principal_id required")
	}
	if audience == "" {
		return nil, errors.New("audience required")
	}

	// Verify principal exists
	var exists bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM principals WHERE id = ?)", principalID).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("lookup principal: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("principal not found: %s", principalID)
	}

	id := uuid.Must(uuid.NewV7()).String()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}

	secretHex := hex.EncodeToString(secret)
	fullToken := fmt.Sprintf("obol_%s_%s", id, secretHex)

	// Compute digest
	mac := hmac.New(sha256.New, s.hmacKey)
	mac.Write([]byte(id))
	mac.Write([]byte(secretHex))
	digest := mac.Sum(nil)

	var expiresAt *time.Time
	if expiresIn > 0 {
		t := time.Now().UTC().Add(expiresIn)
		expiresAt = &t
	}

	createdAt := time.Now().UTC()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO obols (id, principal_id, secret_digest, audience, expires_at, revoked_at, created_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, principalID, digest, audience, nilIfNil(expiresAt), nil, createdAt, nil)
	if err != nil {
		return nil, fmt.Errorf("insert obol: %w", err)
	}

	return &Obol{
		ID:          id,
		PrincipalID: principalID,
		Audience:    audience,
		ExpiresAt:   expiresAt,
		CreatedAt:   createdAt,
		FullToken:   fullToken,
	}, nil
}

// Verify checks an obol token and returns the principal ID if valid.
func (s *Service) Verify(ctx context.Context, token, audience string) (string, error) {
	parts := strings.Split(token, "_")
	if len(parts) != 3 || parts[0] != "obol" {
		return "", ErrInvalidObol
	}
	id := parts[1]
	secretHex := parts[2]

	if len(secretHex) != 64 { // 32 bytes hex = 64 chars
		return "", ErrInvalidObol
	}

	// Compute expected digest
	mac := hmac.New(sha256.New, s.hmacKey)
	mac.Write([]byte(id))
	mac.Write([]byte(secretHex))
	expectedDigest := mac.Sum(nil)

	// Fetch from DB - use string scanning for timestamps (modernc.org/sqlite compatibility)
	var storedDigest []byte
	var principalID string
	var expiresAtStr sql.NullString
	var revokedAtStr sql.NullString
	var tokenAudience string

	err := s.db.QueryRowContext(ctx, `
		SELECT principal_id, secret_digest, audience, expires_at, revoked_at
		FROM obols WHERE id = ?
	`, id).Scan(
		&principalID, &storedDigest, &tokenAudience, &expiresAtStr, &revokedAtStr,
	)
	if err == sql.ErrNoRows {
		return "", ErrInvalidObol
	}
	if err != nil {
		return "", fmt.Errorf("lookup obol: %w", err)
	}

	// Constant-time comparison
	if subtle.ConstantTimeCompare(storedDigest, expectedDigest) != 1 {
		return "", ErrInvalidObol
	}

	if revokedAtStr.Valid && revokedAtStr.String != "" {
		return "", ErrInvalidObol
	}

	if expiresAtStr.Valid && expiresAtStr.String != "" {
		expiresAt, err := time.Parse(time.RFC3339, expiresAtStr.String)
		if err == nil && time.Now().UTC().After(expiresAt) {
			return "", ErrInvalidObol
		}
	}

	if tokenAudience != audience {
		return "", ErrInvalidObol
	}

	// Update last_used
	now := time.Now().UTC()
	_, _ = s.db.ExecContext(ctx, "UPDATE obols SET last_used_at = ? WHERE id = ?", now, id)

	return principalID, nil
}

// Revoke marks an obol as revoked.
func (s *Service) Revoke(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE obols SET revoked_at = ? WHERE id = ?", time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("revoke obol: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("obol not found: %s", id)
	}
	return nil
}

// List returns all obols for a principal.
func (s *Service) List(ctx context.Context, principalID string) ([]*Obol, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, principal_id, audience, expires_at, revoked_at, created_at, last_used_at
		FROM obols WHERE principal_id = ?
		ORDER BY created_at DESC
	`, principalID)
	if err != nil {
		return nil, fmt.Errorf("list obols: %w", err)
	}
	defer rows.Close()

	var obols []*Obol
	for rows.Next() {
		var o Obol
		var expiresAtStr sql.NullString
		var revokedAtStr sql.NullString
		var lastUsedAtStr sql.NullString

		if err := rows.Scan(&o.ID, &o.PrincipalID, &o.Audience,
			&expiresAtStr, &revokedAtStr, &o.CreatedAt, &lastUsedAtStr); err != nil {
			return nil, fmt.Errorf("scan obol: %w", err)
		}

		if expiresAtStr.Valid && expiresAtStr.String != "" {
			if t, err := time.Parse(time.RFC3339, expiresAtStr.String); err == nil {
				o.ExpiresAt = &t
			}
		}
		if revokedAtStr.Valid && revokedAtStr.String != "" {
			if t, err := time.Parse(time.RFC3339, revokedAtStr.String); err == nil {
				o.RevokedAt = &t
			}
		}
		if lastUsedAtStr.Valid && lastUsedAtStr.String != "" {
			if t, err := time.Parse(time.RFC3339, lastUsedAtStr.String); err == nil {
				o.LastUsedAt = &t
			}
		}
		obols = append(obols, &o)
	}

	return obols, rows.Err()
}

func nilIfNil(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return *t
}

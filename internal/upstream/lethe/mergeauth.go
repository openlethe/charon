package lethe

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openlethe/charon/internal/canonical"
)

// Protected-merge authorization envelope, version memory-git-merge/v2. The
// canonical bytes of this struct — exactly as declared — are what Charon
// signs and what Lethe verifies. Field order and naming are
// signature-critical and must stay identical to Lethe's copy; append new
// fields at the end, never reorder.
type MergeAuthorizationEnvelope struct {
	Version           string `json:"version"`
	ProjectID         string `json:"project_id"`
	RefName           string `json:"ref_name"`
	ExpectedHead      string `json:"expected_head"`
	NewHead           string `json:"new_head"`
	ProposalID        string `json:"proposal_id"`
	ProposalDigest    string `json:"proposal_digest"`
	ReviewerPrincipal string `json:"reviewer_principal"`
	MergerPrincipal   string `json:"merger_principal"`
	Strategy          string `json:"strategy"`
	IssuedAt          string `json:"issued_at"`
	ExpiresAt         string `json:"expires_at"`
	Nonce             string `json:"nonce"`
	KeyID             string `json:"key_id"`
}

// MergeAuthorizationVersion is the envelope version both services enforce.
const MergeAuthorizationVersion = "memory-git-merge/v2"

// Merge strategies Lethe independently enforces.
const (
	StrategyFastForward = "fast_forward"
	StrategyMergeCommit = "merge_commit"
	StrategyCherryPick  = "cherry_pick"
)

// DefaultMergeAuthorizationTTL is the default short authorization lifetime.
const DefaultMergeAuthorizationTTL = 2 * time.Minute

// SetMergeSigningKeyID sets the key ID naming which configured key signs
// envelopes; Lethe keeps all configured IDs verifiable during rotation.
func (c *Client) SetMergeSigningKeyID(keyID string) {
	c.mergeKeyID = keyID
}

// SetMergeAuthorizationTTL sets the envelope lifetime. Values outside
// (0, 15m] fall back to the default so an authorization is never long-lived.
func (c *Client) SetMergeAuthorizationTTL(ttl time.Duration) {
	if ttl <= 0 || ttl > 15*time.Minute {
		ttl = DefaultMergeAuthorizationTTL
	}
	c.mergeAuthTTL = ttl
}

// signMergeAuthorizationV2 builds and signs the single-use envelope for one
// protected merge. A fresh cryptographic nonce makes every authorization
// single-use at Lethe; the short expiry and issued-at bound replay windows;
// the proposal-state digest binds the authorization to the exact reviewed
// proposal state. Signature and key material never appear in errors or logs.
func (c *Client) signMergeAuthorizationV2(project, refName, expectedHead, newHead, proposalID, proposalDigest, reviewer, merger, strategy string) (string, error) {
	if len(c.mergeSigningKey) < 32 {
		return "", errors.New("merge signing key is not configured")
	}
	if proposalID == "" || proposalDigest == "" || reviewer == "" || merger == "" {
		return "", errors.New("proposal binding, reviewer, and merger are required for merge authorization")
	}
	ttl := c.mergeAuthTTL
	if ttl <= 0 {
		ttl = DefaultMergeAuthorizationTTL
	}
	now := time.Now().UTC()
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate authorization nonce: %w", err)
	}
	envelope := MergeAuthorizationEnvelope{
		Version:           MergeAuthorizationVersion,
		ProjectID:         project,
		RefName:           refName,
		ExpectedHead:      expectedHead,
		NewHead:           newHead,
		ProposalID:        proposalID,
		ProposalDigest:    proposalDigest,
		ReviewerPrincipal: reviewer,
		MergerPrincipal:   merger,
		Strategy:          strategy,
		IssuedAt:          now.Format(time.RFC3339Nano),
		ExpiresAt:         now.Add(ttl).Format(time.RFC3339Nano),
		Nonce:             hex.EncodeToString(nonce),
		KeyID:             c.mergeKeyID,
	}
	canonicalBytes, err := canonical.JSON(envelope)
	if err != nil {
		return "", fmt.Errorf("encode merge authorization: %w", err)
	}
	mac := hmac.New(sha256.New, c.mergeSigningKey)
	_, _ = mac.Write(canonicalBytes)
	signed, err := json.Marshal(map[string]any{
		"envelope":  envelope,
		"signature": hex.EncodeToString(mac.Sum(nil)),
	})
	if err != nil {
		return "", fmt.Errorf("encode signed authorization: %w", err)
	}
	return string(signed), nil
}

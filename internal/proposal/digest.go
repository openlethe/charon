package proposal

import (
	"github.com/openlethe/charon/internal/canonical"
)

// Digest domains. Changing either is a schema-versioned event: existing
// persisted digests would no longer match recomputed values.
const (
	// RequestDigestDomain covers the canonical merge-proposal request used for
	// exactly-once creation. It answers "is this retry the same request?".
	RequestDigestDomain = "charon/proposal-request/v1"
	// StateDigestDomain covers the canonical persisted proposal state. It is
	// recomputed on every mutation and verified on every load.
	StateDigestDomain = "charon/proposal-state/v1"
	// StateSchemaVersion marks the fields the state digest binds.
	StateSchemaVersion = "proposal-state/v1"
)

// RequestDigestInput binds every behaviorally relevant field of a merge
// proposal request. Two requests are the same request exactly when their
// digests match; a same-key different-body retry never collides.
type RequestDigestInput struct {
	SchemaVersion   string `json:"schema_version"`
	Operation       string `json:"operation"`
	Principal       string `json:"principal"`
	ProjectID       string `json:"project_id"`
	TargetRef       string `json:"target_ref"`
	BaseChangesetID string `json:"base_changeset_id"`
	HeadChangesetID string `json:"head_changeset_id"`
	SelectedOps     []int  `json:"selected_ops"`
	Message         string `json:"message"`
}

// RequestDigest computes the canonical request digest for a merge proposal.
func RequestDigest(in RequestDigestInput) (string, error) {
	if in.SchemaVersion == "" {
		in.SchemaVersion = "merge-proposal/v1"
	}
	if in.SelectedOps == nil {
		in.SelectedOps = []int{}
	}
	return canonical.Digest(RequestDigestDomain, in)
}

// stateDigestFields is the canonical persisted proposal state. Field order is
// digest-significant; append new fields, never reorder.
type stateDigestFields struct {
	SchemaVersion         string          `json:"schema_version"`
	ProposalID            string          `json:"proposal_id"`
	ProjectID             string          `json:"project_id"`
	Kind                  string          `json:"kind"`
	ProposedEventType     string          `json:"proposed_event_type"`
	Content               string          `json:"content"`
	Reason                string          `json:"reason"`
	Confidence            float64         `json:"confidence"`
	Tags                  []string        `json:"tags"`
	OriginatingPrincipal  string          `json:"originating_principal"`
	OriginatingClient     string          `json:"originating_client"`
	OriginatingProvider   string          `json:"originating_provider"`
	OriginatingSession    string          `json:"originating_session"`
	RequestID             string          `json:"request_id"`
	IdempotencyKey        string          `json:"idempotency_key"`
	Status                string          `json:"status"`
	DecidedBy             string          `json:"decided_by"`
	ResultingLetheEventID string          `json:"resulting_lethe_event_id"`
	ChangesetID           string          `json:"changeset_id"`
	BaseChangesetID       string          `json:"base_changeset_id"`
	HeadChangesetID       string          `json:"head_changeset_id"`
	TargetRef             string          `json:"target_ref"`
	SelectedOps           []int           `json:"selected_ops"`
	ConflictIDs           []string        `json:"conflict_ids"`
	ReviewFindings        []ReviewFinding `json:"review_findings"`
}

// StateDigest computes the canonical integrity digest over the exact proposal
// state that authorization decisions and merges rely on. Review approval binds
// to this state through ReviewTargetDigest over the same immutable fields.
func StateDigest(p *Proposal) (string, error) {
	return canonical.Digest(StateDigestDomain, stateDigestFields{
		SchemaVersion:         StateSchemaVersion,
		ProposalID:            p.ID,
		ProjectID:             p.ProjectID,
		Kind:                  p.Kind,
		ProposedEventType:     p.ProposedEventType,
		Content:               p.Content,
		Reason:                p.Reason,
		Confidence:            p.Confidence,
		Tags:                  nonNilStrings(p.Tags),
		OriginatingPrincipal:  p.OriginatingPrincipal,
		OriginatingClient:     p.OriginatingClient,
		OriginatingProvider:   p.OriginatingProvider,
		OriginatingSession:    p.OriginatingSession,
		RequestID:             p.RequestID,
		IdempotencyKey:        p.IdempotencyKey,
		Status:                string(p.Status),
		DecidedBy:             p.DecidedBy,
		ResultingLetheEventID: p.ResultingLetheEventID,
		ChangesetID:           p.ChangesetID,
		BaseChangesetID:       p.BaseChangesetID,
		HeadChangesetID:       p.HeadChangesetID,
		TargetRef:             p.TargetRef,
		SelectedOps:           nonNilInts(p.SelectedOps),
		ConflictIDs:           nonNilStrings(p.ConflictIDs),
		ReviewFindings:        nonNilFindings(p.ReviewFindings),
	})
}

func nonNilFindings(v []ReviewFinding) []ReviewFinding {
	if v == nil {
		return []ReviewFinding{}
	}
	return v
}

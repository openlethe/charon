package lethe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// --- Memory Git V1 upstream methods ---

type LegacyRootResponse struct {
	Changeset *Event `json:"changeset"`
	Ref       *Ref   `json:"ref"`
}

type Ref struct {
	ProjectID          string `json:"project_id"`
	RefName            string `json:"ref_name"`
	HeadChangesetID    string `json:"head_changeset_id"`
	Protected          bool   `json:"protected"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	CreatedByPrincipal string `json:"created_by_principal,omitempty"`
}

type Changeset struct {
	ChangesetID       string                   `json:"changeset_id"`
	SchemaVersion     string                   `json:"schema_version"`
	ProjectID         string                   `json:"project_id"`
	RefName           string                   `json:"ref_name"`
	ParentIDs         []string                 `json:"parent_ids"`
	AuthorPrincipal   string                   `json:"author_principal"`
	PersonaID         string                   `json:"persona_id,omitempty"`
	ActorID           string                   `json:"actor_id,omitempty"`
	Surface           string                   `json:"surface,omitempty"`
	Model             string                   `json:"model,omitempty"`
	Environment       string                   `json:"environment,omitempty"`
	SessionID         string                   `json:"session_id,omitempty"`
	TopicID           string                   `json:"topic_id,omitempty"`
	ContextManifestID string                   `json:"context_manifest_id,omitempty"`
	Message           string                   `json:"message"`
	CreatedAt         string                   `json:"created_at"`
	IdempotencyKey    string                   `json:"idempotency_key"`
	Ops               []SemanticOp             `json:"ops,omitempty"`
	Evidence          []map[string]interface{} `json:"evidence,omitempty"`
	Verification      []map[string]interface{} `json:"verification,omitempty"`
	IntegrityDigest   string                   `json:"integrity_digest"`
}

type SemanticOp struct {
	Ordinal          int                    `json:"ordinal"`
	OpType           string                 `json:"op_type"`
	TargetEventID    string                 `json:"target_event_id,omitempty"`
	ResultingEventID string                 `json:"resulting_event_id,omitempty"`
	Payload          map[string]interface{} `json:"payload,omitempty"`
}

type CreateChangesetRequest struct {
	ProjectID          string                   `json:"project_id"`
	RefName            string                   `json:"ref_name"`
	ParentIDs          []string                 `json:"parent_ids,omitempty"`
	AuthorPrincipal    string                   `json:"author_principal"`
	PersonaID          string                   `json:"persona_id,omitempty"`
	ActorID            string                   `json:"actor_id,omitempty"`
	Surface            string                   `json:"surface,omitempty"`
	Model              string                   `json:"model,omitempty"`
	Environment        string                   `json:"environment,omitempty"`
	SessionID          string                   `json:"session_id,omitempty"`
	TopicID            string                   `json:"topic_id,omitempty"`
	ContextManifestID  string                   `json:"context_manifest_id,omitempty"`
	Message            string                   `json:"message"`
	IdempotencyKey     string                   `json:"idempotency_key"`
	Ops                []SemanticOp             `json:"ops"`
	Evidence           []map[string]interface{} `json:"evidence,omitempty"`
	Verification       []map[string]interface{} `json:"verification,omitempty"`
	ExpectedHead       string                   `json:"expected_head,omitempty"`
	AdvanceRef         bool                     `json:"advance_ref,omitempty"`
	CreateRefIfMissing bool                     `json:"create_ref_if_missing,omitempty"`
	Protected          bool                     `json:"protected,omitempty"`
}

type CreateBranchRequest struct {
	RefName         string `json:"ref_name"`
	HeadChangesetID string `json:"head_changeset_id"`
	Principal       string `json:"principal"`
	Protected       bool   `json:"protected,omitempty"`
}

type CASAdvanceRequest struct {
	RefName      string `json:"ref_name,omitempty"`
	ExpectedHead string `json:"expected_head"`
	NewHead      string `json:"new_head"`
}

type MergeAdvanceRequest struct {
	RefName           string `json:"ref_name,omitempty"`
	ExpectedHead      string `json:"expected_head"`
	NewHead           string `json:"new_head"`
	MergeProposalID   string `json:"merge_proposal_id"`
	ReviewerPrincipal string `json:"reviewer_principal"`
	Authorization     string `json:"merge_authorization"`
}

type DetectConflictsRequest struct {
	BaseChangesetID  string `json:"base_changeset_id"`
	LeftChangesetID  string `json:"left_changeset_id"`
	RightChangesetID string `json:"right_changeset_id"`
}

type Conflict struct {
	ConflictID       string                 `json:"conflict_id"`
	ProjectID        string                 `json:"project_id"`
	BaseChangesetID  string                 `json:"base_changeset_id,omitempty"`
	LeftChangesetID  string                 `json:"left_changeset_id"`
	RightChangesetID string                 `json:"right_changeset_id"`
	ConflictType     string                 `json:"conflict_type"`
	Severity         string                 `json:"severity"`
	Summary          string                 `json:"summary"`
	Details          map[string]interface{} `json:"details,omitempty"`
	Status           string                 `json:"status"`
}

type DiffRequest struct {
	BaseChangesetID string `json:"base_changeset_id,omitempty"`
}

type DiffResponse struct {
	ProjectID           string         `json:"project_id"`
	BaseChangesetID     string         `json:"base_changeset_id"`
	TargetChangesetID   string         `json:"target_changeset_id"`
	MemoriesAdded       []DiffItem     `json:"memories_added"`
	Corrections         []DiffItem     `json:"corrections_proposed"`
	Superseded          []DiffItem     `json:"records_superseded"`
	RelationshipsAdded  []DiffItem     `json:"relationships_added"`
	DecisionsChanged    []DiffItem     `json:"decisions_changed"`
	TasksFlagsChanged   []DiffItem     `json:"tasks_or_flags_changed"`
	Duplicates          []DiffItem     `json:"duplicates_detected"`
	VisibilityAffected  []DiffItem     `json:"permissions_or_visibility_affected"`
	EvidenceChanged     []DiffItem     `json:"evidence_added_or_removed"`
	UnresolvedConflicts []string       `json:"unresolved_conflicts"`
	KindNotes           []DiffKindNote `json:"kind_notes,omitempty"`
}

type DiffItem struct {
	OpOrdinal        int                    `json:"op_ordinal"`
	OpType           string                 `json:"op_type"`
	ChangesetID      string                 `json:"changeset_id"`
	TargetEventID    string                 `json:"target_event_id,omitempty"`
	ResultingEventID string                 `json:"resulting_event_id,omitempty"`
	Summary          string                 `json:"summary"`
	Kind             string                 `json:"kind,omitempty"`
	Payload          map[string]interface{} `json:"payload,omitempty"`
}

type DiffKindNote struct {
	LeftEventID  string `json:"left_event_id,omitempty"`
	RightEventID string `json:"right_event_id,omitempty"`
	Kind         string `json:"kind"`
	Reason       string `json:"reason"`
}

type Manifest struct {
	ManifestID           string            `json:"manifest_id"`
	Direction            string            `json:"direction"`
	ProjectID            string            `json:"project_id"`
	RefName              string            `json:"ref_name"`
	HeadChangesetID      string            `json:"head_changeset_id"`
	BaseChangesetID      string            `json:"base_changeset_id,omitempty"`
	ResultingChangesetID string            `json:"resulting_changeset_id,omitempty"`
	ProposedTargetRef    string            `json:"proposed_target_ref,omitempty"`
	MergeProposalID      string            `json:"merge_proposal_id,omitempty"`
	SelectedMemoryIDs    []string          `json:"selected_memory_ids"`
	InclusionReasons     map[string]string `json:"inclusion_reasons,omitempty"`
	ExclusionReasons     map[string]string `json:"exclusion_reasons,omitempty"`
	UnresolvedConflicts  []string          `json:"unresolved_conflicts,omitempty"`
	SessionID            string            `json:"session_id,omitempty"`
	ActorID              string            `json:"actor_id,omitempty"`
	CreatedAt            string            `json:"created_at"`
}

// EnsureLegacyRoot ensures the project has a legacy root changeset and refs/shared/main.
func (c *Client) EnsureLegacyRoot(ctx context.Context, projectID string) (*LegacyRootResponse, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/api/memory/%s/legacy-root", projectID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var result LegacyRootResponse
	if err := c.decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode legacy root: %w", err)
	}
	return &result, nil
}

// CreateChangeset commits a new changeset to Lethe.
func (c *Client) CreateChangeset(ctx context.Context, req CreateChangesetRequest) (*Changeset, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/api/memory/changesets", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var cs Changeset
	if err := c.decodeJSON(resp, &cs); err != nil {
		return nil, fmt.Errorf("decode changeset: %w", err)
	}
	return &cs, nil
}

// GetChangeset loads a changeset by ID.
func (c *Client) GetChangeset(ctx context.Context, id string) (*Changeset, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/api/memory/changesets/%s", id), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var cs Changeset
	if err := c.decodeJSON(resp, &cs); err != nil {
		return nil, fmt.Errorf("decode changeset: %w", err)
	}
	return &cs, nil
}

// ListChangesets returns the log for a project ref.
func (c *Client) ListChangesets(ctx context.Context, projectID, refName string, limit int) ([]Changeset, error) {
	path := fmt.Sprintf("/api/memory/%s/changesets?ref=%s&limit=%d", url.PathEscape(projectID), url.QueryEscape(refName), limit)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var result struct {
		Changesets []Changeset `json:"changesets"`
	}
	if err := c.decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode changesets: %w", err)
	}
	return result.Changesets, nil
}

// CreateBranch creates a new memory ref.
func (c *Client) CreateBranch(ctx context.Context, projectID string, req CreateBranchRequest) (*Ref, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/api/memory/%s/branches", projectID), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var ref Ref
	if err := c.decodeJSON(resp, &ref); err != nil {
		return nil, fmt.Errorf("decode ref: %w", err)
	}
	return &ref, nil
}

// GetRef returns a single ref.
func (c *Client) GetRef(ctx context.Context, projectID, refName string) (*Ref, error) {
	refs, err := c.ListRefs(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range refs {
		if refs[i].RefName == refName {
			return &refs[i], nil
		}
	}
	return nil, nil
}

// ListRefs returns all refs for a project.
func (c *Client) ListRefs(ctx context.Context, projectID string) ([]Ref, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/api/memory/%s/refs", projectID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var refs []Ref
	if err := c.decodeJSON(resp, &refs); err != nil {
		return nil, fmt.Errorf("decode refs: %w", err)
	}
	return refs, nil
}

// CASAdvanceRef advances a ref with compare-and-swap.
func (c *Client) CASAdvanceRef(ctx context.Context, projectID, refName string, req CASAdvanceRequest) (*Ref, error) {
	req.RefName = refName
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/api/memory/%s/refs/advance", url.PathEscape(projectID)), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var ref Ref
	if err := c.decodeJSON(resp, &ref); err != nil {
		return nil, fmt.Errorf("decode ref: %w", err)
	}
	return &ref, nil
}

// MergeAdvanceRef advances a protected ref through Lethe's explicit merge path.
func (c *Client) MergeAdvanceRef(ctx context.Context, projectID, refName string, req MergeAdvanceRequest) (*Ref, error) {
	req.RefName = refName
	signature, err := c.signMergeAuthorization(projectID, refName, req.ExpectedHead, req.NewHead, req.MergeProposalID, req.ReviewerPrincipal)
	if err != nil {
		return nil, err
	}
	req.Authorization = signature
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/api/memory/%s/refs/merge", url.PathEscape(projectID)), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var ref Ref
	if err := c.decodeJSON(resp, &ref); err != nil {
		return nil, fmt.Errorf("decode ref: %w", err)
	}
	return &ref, nil
}

// DetectConflicts asks Lethe to compute and persist explicit merge conflicts.
func (c *Client) DetectConflicts(ctx context.Context, projectID string, req DetectConflictsRequest) ([]Conflict, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/api/memory/%s/conflicts/detect", url.PathEscape(projectID)), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var result struct {
		Conflicts []Conflict `json:"conflicts"`
	}
	if err := c.decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode conflicts: %w", err)
	}
	return result.Conflicts, nil
}

// DiffChangeset computes a semantic diff.
func (c *Client) DiffChangeset(ctx context.Context, changesetID string, req DiffRequest) (*DiffResponse, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/api/memory/changesets/%s/diff", changesetID), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var result DiffResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode diff: %w", err)
	}
	return &result, nil
}

// CreateManifest stores a context manifest pin.
func (c *Client) CreateManifest(ctx context.Context, m Manifest) (*Manifest, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/api/memory/manifests", m)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var result Manifest
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return &result, nil
}

// GetManifest loads a manifest by ID.
func (c *Client) GetManifest(ctx context.Context, id string) (*Manifest, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/api/memory/manifests/%s", id), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return &m, nil
}

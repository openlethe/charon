package mcp

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/openlethe/charon/internal/upstream/lethe"
)

// Strict typed request structures for Memory Git write operations. Generic-map
// parsing previously skipped malformed operations and mistyped fields
// silently, so the committed operation set could differ from what the caller
// submitted. These parsers reject the entire request on any malformed
// element, reject unknown fields, and preserve operation count and order
// exactly. The validated struct is the single canonical representation used
// for the idempotency digest, the upstream Lethe serialization, and audit
// metadata.

// validSemanticOpTypes mirrors lethe's models.ValidSemanticOp. Lethe remains
// the authoritative validator; this list lets Charon fail before any upstream
// side effect can occur. Keep in sync with lethe/internal/models.
var validSemanticOpTypes = map[string]bool{
	"add_memory":          true,
	"add_relationship":    true,
	"correct_memory":      true,
	"supersede_memory":    true,
	"mark_duplicate":      true,
	"propose_deprecation": true,
	"attach_evidence":     true,
	"attach_verification": true,
}

// typedSemanticOp is one strictly validated changeset operation.
type typedSemanticOp struct {
	Ordinal          *int
	OpType           string
	TargetEventID    string
	ResultingEventID string
	Payload          map[string]any
}

// Payload size caps, mirroring lethe/internal/db/memory_git_validation.go.
// Charon fails before any upstream side effect; Lethe remains the
// authoritative enforcer with identical bounds. Keep in sync.
const (
	// maxOpPayloadBytes bounds the canonical JSON encoding of one op's
	// payload (Go marshals map keys in sorted order, so this is exact).
	maxOpPayloadBytes = 64 << 10
	// maxContentBytes bounds free-text memory content.
	maxContentBytes = 16 << 10
	// maxSummaryBytes bounds human-readable summaries, reasons, and notes.
	maxSummaryBytes = 4 << 10
	// maxFieldBytes bounds every other payload string and the op-level ID
	// channels (identifiers, kinds, provenance references).
	maxFieldBytes = 1024
	// maxTagBytes bounds a single tag entry.
	maxTagBytes = 128
	// maxTags bounds the tags array of one op.
	maxTags = 64
)

// attestationProvenanceKeys are the merge/review provenance markers. They are
// legitimate on every op because cherry-pick construction annotates copied ops
// (e.g. a cherry-picked add_memory records cherrypicked_from).
var attestationProvenanceKeys = []string{
	"reviewer", "proposal_id", "source_changeset_id",
	"rejected_from", "cherrypicked_from", "left_branch", "right_branch",
}

// commonPayloadKeys have defined semantics for every op type: diff summaries,
// boundary and scope-flow metadata, and merge provenance.
var commonPayloadKeys = append([]string{
	"summary", "project_id", "topic_id", "actor_id", "from_visibility", "to_visibility",
}, attestationProvenanceKeys...)

// memoryContentKeys are the projector overlay fields plus the fact/decision
// metadata the conflict detector compares.
var memoryContentKeys = []string{
	"content", "kind", "event_type", "scope", "visibility", "tags", "confidence",
	"fact_key", "subject", "valid_from", "valid_to", "protected", "approval",
}

// attestationPayloadKeys are the evidence-record fields an attach op may
// carry in addition to the provenance markers.
var attestationPayloadKeys = []string{"kind", "source", "note", "status"}

// allowedPayloadKeys mirrors Lethe's closed payload key contract per op type.
// Unknown keys are rejected by name before any upstream call.
var allowedPayloadKeys = map[string]map[string]bool{
	"add_memory":     keySet(commonPayloadKeys, memoryContentKeys, []string{"memory_id", "event_id"}),
	"correct_memory": keySet(commonPayloadKeys, memoryContentKeys, []string{"target_trust", "source_trust"}),
	"supersede_memory": keySet(commonPayloadKeys, memoryContentKeys,
		[]string{"target_trust", "source_trust", "memory_id", "event_id"}),
	"mark_duplicate":      keySet(commonPayloadKeys, []string{"duplicate_id", "duplicate_of"}),
	"add_relationship":    keySet(commonPayloadKeys, []string{"kind", "from_memory_id", "to_memory_id"}),
	"propose_deprecation": keySet(commonPayloadKeys, []string{"reason"}),
	"attach_evidence":     keySet(commonPayloadKeys, attestationPayloadKeys),
	"attach_verification": keySet(commonPayloadKeys, attestationPayloadKeys),
}

func keySet(groups ...[]string) map[string]bool {
	set := map[string]bool{}
	for _, group := range groups {
		for _, key := range group {
			set[key] = true
		}
	}
	return set
}

// payloadStringLimit returns the size cap for a free-text payload field.
func payloadStringLimit(key string) int {
	switch key {
	case "content":
		return maxContentBytes
	case "summary", "reason", "note":
		return maxSummaryBytes
	default:
		return maxFieldBytes
	}
}

// idsDisagree reports whether two identifier channels for the same role are
// both set but name different identities. Equal values via two channels are
// fine: the projector's fallback order simply normalizes them.
func idsDisagree(a, b string) bool {
	return a != "" && b != "" && a != b
}

// typedChangesetCreate is the canonical memory_changeset_create request.
type typedChangesetCreate struct {
	Project        string            `json:"project"`
	Ref            string            `json:"ref"`
	Message        string            `json:"message"`
	ParentIDs      []string          `json:"parent_ids"`
	Ops            []typedSemanticOp `json:"ops"`
	ExpectedHead   string            `json:"expected_head"`
	IdempotencyKey string            `json:"idempotency_key"`
}

// typedMergePropose is the canonical memory_merge_propose request.
type typedMergePropose struct {
	Project         string `json:"project"`
	TargetRef       string `json:"target_ref"`
	HeadChangesetID string `json:"head_changeset_id"`
	BaseChangesetID string `json:"base_changeset_id"`
	Message         string `json:"message"`
	SelectedOps     []int  `json:"selected_ops"`
	IdempotencyKey  string `json:"idempotency_key"`
}

// typedMerge is the canonical memory_merge request.
type typedMerge struct {
	ProposalID     string `json:"proposal_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func strictString(args map[string]any, key string, required bool) (string, error) {
	value, present := args[key]
	if !present || value == nil {
		if required {
			return "", fmt.Errorf("missing required argument: %s", key)
		}
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if required && text == "" {
		return "", fmt.Errorf("missing required argument: %s", key)
	}
	return text, nil
}

func rejectUnknownKeys(args map[string]any, known ...string) error {
	allowed := make(map[string]bool, len(known))
	for _, key := range known {
		allowed[key] = true
	}
	for key := range args {
		if !allowed[key] {
			return fmt.Errorf("unknown argument: %s", key)
		}
	}
	return nil
}

// strictNonEmptyID validates an optional identifier field: when present it
// must be a non-empty string; absent or null means unset.
func strictNonEmptyID(args map[string]any, key string) (string, error) {
	value, present := args[key]
	if !present || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if text == "" {
		return "", fmt.Errorf("%s must not be empty when present", key)
	}
	return text, nil
}

func parseSemanticOps(raw any) ([]typedSemanticOp, error) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("ops must be an array of operation objects")
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("ops must contain at least one operation")
	}
	ops := make([]typedSemanticOp, 0, len(arr))
	for index, element := range arr {
		obj, ok := element.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ops[%d] must be an object", index)
		}
		for key := range obj {
			switch key {
			case "op_type", "ordinal", "target_event_id", "resulting_event_id", "payload":
			default:
				return nil, fmt.Errorf("ops[%d] has unknown field %q", index, key)
			}
		}
		var op typedSemanticOp
		opType, err := strictString(obj, "op_type", true)
		if err != nil {
			return nil, fmt.Errorf("ops[%d]: %w", index, err)
		}
		if !validSemanticOpTypes[opType] {
			return nil, fmt.Errorf("ops[%d]: unknown op_type %q", index, opType)
		}
		op.OpType = opType
		if rawOrdinal, present := obj["ordinal"]; present && rawOrdinal != nil {
			number, ok := rawOrdinal.(float64)
			if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number != math.Trunc(number) || number > float64(math.MaxInt) {
				return nil, fmt.Errorf("ops[%d]: ordinal must be a non-negative integer", index)
			}
			ordinal := int(number)
			if ordinal != index {
				return nil, fmt.Errorf("ops[%d]: ordinal %d does not match its position", index, ordinal)
			}
			op.Ordinal = &ordinal
		}
		if op.TargetEventID, err = strictNonEmptyID(obj, "target_event_id"); err != nil {
			return nil, fmt.Errorf("ops[%d]: %w", index, err)
		}
		if op.ResultingEventID, err = strictNonEmptyID(obj, "resulting_event_id"); err != nil {
			return nil, fmt.Errorf("ops[%d]: %w", index, err)
		}
		if rawPayload, present := obj["payload"]; present && rawPayload != nil {
			payload, ok := rawPayload.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("ops[%d]: payload must be an object", index)
			}
			op.Payload = payload
		}
		if op.Payload == nil {
			op.Payload = map[string]any{}
		}
		if err := validateOpStructure(index, op); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// validateOpStructure enforces the operation-specific invariants Charon can
// prove without memory state. Lethe re-validates everything against the
// parent state; these checks stop malformed requests before any upstream side
// effect and never silently drop caller input. Size caps, the closed payload
// key sets, and the ambiguous-identifier rules mirror Lethe's stateless
// validation exactly.
func validateOpStructure(index int, op typedSemanticOp) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("ops[%d] (%s): %s", index, op.OpType, fmt.Sprintf(format, args...))
	}
	encoded, err := json.Marshal(op.Payload)
	if err != nil {
		return fail("payload is not JSON-encodable: %v", err)
	}
	if len(encoded) > maxOpPayloadBytes {
		return fail("payload is %d bytes, exceeding the %d-byte per-op cap", len(encoded), maxOpPayloadBytes)
	}
	if len(op.TargetEventID) > maxFieldBytes {
		return fail("target_event_id exceeds the %d-byte identifier cap", maxFieldBytes)
	}
	if len(op.ResultingEventID) > maxFieldBytes {
		return fail("resulting_event_id exceeds the %d-byte identifier cap", maxFieldBytes)
	}
	if allowed, ok := allowedPayloadKeys[op.OpType]; ok {
		for key, value := range op.Payload {
			if !allowed[key] {
				return fail("payload field %q is not supported for %s", key, op.OpType)
			}
			if text, isString := value.(string); isString {
				if limit := payloadStringLimit(key); len(text) > limit {
					return fail("payload.%s exceeds the %d-byte cap", key, limit)
				}
			}
		}
		if tags, present := op.Payload["tags"]; present {
			if list, isList := tags.([]any); isList {
				if len(list) > maxTags {
					return fail("payload.tags has %d entries, exceeding the %d-entry cap", len(list), maxTags)
				}
				for _, tag := range list {
					if text, isString := tag.(string); isString && len(text) > maxTagBytes {
						return fail("payload.tags entry exceeds the %d-byte cap", maxTagBytes)
					}
				}
			}
		}
	}
	payloadString := func(key string) string {
		text, _ := op.Payload[key].(string)
		return text
	}
	switch op.OpType {
	case "add_memory":
		if payloadString("content") == "" {
			return fail("payload.content is required and must be a non-empty string")
		}
		// The resulting identity may arrive over three channels; they must
		// never disagree about which memory this op creates.
		if idsDisagree(op.ResultingEventID, payloadString("memory_id")) ||
			idsDisagree(op.ResultingEventID, payloadString("event_id")) ||
			idsDisagree(payloadString("memory_id"), payloadString("event_id")) {
			return fail("ambiguous resulting identity: resulting_event_id, payload.memory_id, and payload.event_id must agree when more than one is set")
		}
	case "correct_memory":
		if op.TargetEventID == "" {
			return fail("target_event_id is required")
		}
		if len(op.Payload) == 0 {
			return fail("payload must contain at least one correction field")
		}
	case "supersede_memory":
		if op.TargetEventID == "" {
			return fail("target_event_id is required")
		}
		if op.ResultingEventID != "" && op.ResultingEventID == op.TargetEventID {
			return fail("resulting_event_id must differ from target_event_id (no self-supersede)")
		}
		// The replacement identity may arrive over three channels; they must
		// never disagree about which memory replaces the target.
		if idsDisagree(op.ResultingEventID, payloadString("memory_id")) ||
			idsDisagree(op.ResultingEventID, payloadString("event_id")) ||
			idsDisagree(payloadString("memory_id"), payloadString("event_id")) {
			return fail("ambiguous replacement identity: resulting_event_id, payload.memory_id, and payload.event_id must agree when more than one is set")
		}
	case "mark_duplicate":
		// The duplicate and canonical identities each have two channels; a
		// disagreement would make the recorded lineage ambiguous.
		if idsDisagree(op.TargetEventID, payloadString("duplicate_id")) {
			return fail("ambiguous duplicate identity: target_event_id and payload.duplicate_id must agree when both are set")
		}
		if idsDisagree(op.ResultingEventID, payloadString("duplicate_of")) {
			return fail("ambiguous canonical identity: resulting_event_id and payload.duplicate_of must agree when both are set")
		}
		duplicate := op.TargetEventID
		if duplicate == "" {
			duplicate = payloadString("duplicate_id")
		}
		canonical := op.ResultingEventID
		if canonical == "" {
			canonical = payloadString("duplicate_of")
		}
		if duplicate == "" || canonical == "" {
			return fail("duplicate and canonical targets are both required")
		}
		if duplicate == canonical {
			return fail("duplicate and canonical targets must be distinct")
		}
	case "add_relationship":
		if idsDisagree(op.TargetEventID, payloadString("from_memory_id")) {
			return fail("ambiguous from identity: target_event_id and payload.from_memory_id must agree when both are set")
		}
		if idsDisagree(op.ResultingEventID, payloadString("to_memory_id")) {
			return fail("ambiguous to identity: resulting_event_id and payload.to_memory_id must agree when both are set")
		}
		from := op.TargetEventID
		if from == "" {
			from = payloadString("from_memory_id")
		}
		to := op.ResultingEventID
		if to == "" {
			to = payloadString("to_memory_id")
		}
		if from == "" || to == "" {
			return fail("from and to memory IDs are both required")
		}
		if from == to {
			return fail("self-relations are not allowed")
		}
		if payloadString("kind") == "" {
			return fail("payload.kind is required and must be a non-empty string")
		}
	case "propose_deprecation":
		if op.TargetEventID == "" {
			return fail("target_event_id is required")
		}
		if payloadString("reason") == "" {
			return fail("payload.reason is required and must be a non-empty string")
		}
	case "attach_evidence", "attach_verification":
		if len(op.Payload) == 0 {
			return fail("payload must be a non-empty object")
		}
		if op.TargetEventID == "" {
			// Target-less form is a merge/review attestation marker, matching
			// Lethe: it needs a human-readable summary and at least one
			// provenance field.
			if payloadString("summary") == "" {
				return fail("target-less attestation requires payload.summary")
			}
			provenance := false
			for _, key := range attestationProvenanceKeys {
				if payloadString(key) != "" {
					provenance = true
					break
				}
			}
			if !provenance {
				return fail("target-less attestation requires a provenance field (one of: %s)", strings.Join(attestationProvenanceKeys, ", "))
			}
		}
	}
	return nil
}

// parseChangesetCreateArgs strictly validates memory_changeset_create
// arguments. Any malformed element rejects the whole request.
func parseChangesetCreateArgs(args map[string]any) (*typedChangesetCreate, error) {
	if err := rejectUnknownKeys(args, "project", "ref", "message", "parent_ids", "ops", "expected_head", "idempotency_key"); err != nil {
		return nil, err
	}
	var out typedChangesetCreate
	var err error
	if out.Project, err = strictString(args, "project", true); err != nil {
		return nil, err
	}
	if out.Ref, err = strictString(args, "ref", true); err != nil {
		return nil, err
	}
	if out.Message, err = strictString(args, "message", true); err != nil {
		return nil, err
	}
	if out.ExpectedHead, err = strictString(args, "expected_head", true); err != nil {
		return nil, err
	}
	if out.IdempotencyKey, err = strictString(args, "idempotency_key", true); err != nil {
		return nil, err
	}
	if rawParents, present := args["parent_ids"]; present && rawParents != nil {
		arr, ok := rawParents.([]any)
		if !ok {
			return nil, fmt.Errorf("parent_ids must be an array of changeset ID strings")
		}
		for index, element := range arr {
			parent, ok := element.(string)
			if !ok || parent == "" {
				return nil, fmt.Errorf("parent_ids[%d] must be a non-empty string", index)
			}
			out.ParentIDs = append(out.ParentIDs, parent)
		}
	}
	ops, err := parseSemanticOps(args["ops"])
	if err != nil {
		return nil, err
	}
	out.Ops = ops
	return &out, nil
}

// parseMergeProposeArgs strictly validates memory_merge_propose arguments.
func parseMergeProposeArgs(args map[string]any) (*typedMergePropose, error) {
	if err := rejectUnknownKeys(args, "project", "target_ref", "head_changeset_id", "base_changeset_id", "message", "selected_ops", "idempotency_key"); err != nil {
		return nil, err
	}
	var out typedMergePropose
	var err error
	if out.Project, err = strictString(args, "project", true); err != nil {
		return nil, err
	}
	if out.TargetRef, err = strictString(args, "target_ref", true); err != nil {
		return nil, err
	}
	if out.HeadChangesetID, err = strictString(args, "head_changeset_id", true); err != nil {
		return nil, err
	}
	if out.BaseChangesetID, err = strictString(args, "base_changeset_id", false); err != nil {
		return nil, err
	}
	if out.Message, err = strictString(args, "message", false); err != nil {
		return nil, err
	}
	if out.IdempotencyKey, err = strictString(args, "idempotency_key", true); err != nil {
		return nil, err
	}
	selected, err := getIntSlice(args, "selected_ops")
	if err != nil {
		return nil, err
	}
	out.SelectedOps = selected
	return &out, nil
}

// parseMergeArgs strictly validates memory_merge arguments.
func parseMergeArgs(args map[string]any) (*typedMerge, error) {
	if err := rejectUnknownKeys(args, "proposal_id", "idempotency_key"); err != nil {
		return nil, err
	}
	var out typedMerge
	var err error
	if out.ProposalID, err = strictString(args, "proposal_id", true); err != nil {
		return nil, err
	}
	if out.IdempotencyKey, err = strictString(args, "idempotency_key", true); err != nil {
		return nil, err
	}
	return &out, nil
}

// toLetheOps converts validated operations to the upstream representation,
// preserving count and order exactly.
func toLetheOps(ops []typedSemanticOp) []lethe.SemanticOp {
	out := make([]lethe.SemanticOp, 0, len(ops))
	for i, op := range ops {
		converted := lethe.SemanticOp{
			Ordinal:          i,
			OpType:           op.OpType,
			TargetEventID:    op.TargetEventID,
			ResultingEventID: op.ResultingEventID,
			Payload:          op.Payload,
		}
		out = append(out, converted)
	}
	return out
}

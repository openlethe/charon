package mcp

import (
	"strings"
	"testing"

	"github.com/openlethe/charon/internal/canonical"
)

func validChangesetArgs() map[string]any {
	return map[string]any{
		"project":         "project-1",
		"ref":             "refs/agents/agent-1/main",
		"message":         "commit",
		"expected_head":   "head-1",
		"idempotency_key": "key-1",
		"ops": []any{
			map[string]any{"op_type": "add_memory", "payload": map[string]any{"content": "hello"}},
		},
	}
}

func TestParseChangesetCreateAcceptsValid(t *testing.T) {
	typed, err := parseChangesetCreateArgs(validChangesetArgs())
	if err != nil {
		t.Fatal(err)
	}
	if typed.Project != "project-1" || len(typed.Ops) != 1 {
		t.Fatalf("typed = %#v", typed)
	}
}

func TestParseChangesetCreateRejectsMalformed(t *testing.T) {
	cases := map[string]func(args map[string]any){
		"unknown top-level field": func(a map[string]any) { a["bogus"] = 1 },
		"non-object op element":   func(a map[string]any) { a["ops"] = []any{"not-an-object"} },
		"missing op_type":         func(a map[string]any) { a["ops"] = []any{map[string]any{"payload": map[string]any{}}} },
		"unknown op_type": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "delete_everything"}}
		},
		"non-object payload": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "add_memory", "payload": "text"}}
		},
		"unknown op field": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "add_memory", "payload": map[string]any{"content": "x"}, "surprise": true}}
		},
		"malformed target id": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "correct_memory", "target_event_id": 42, "payload": map[string]any{"content": "x"}}}
		},
		"empty target id": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "correct_memory", "target_event_id": "", "payload": map[string]any{"content": "x"}}}
		},
		"fractional ordinal": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "add_memory", "ordinal": 0.5, "payload": map[string]any{"content": "x"}}}
		},
		"negative ordinal": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "add_memory", "ordinal": -1.0, "payload": map[string]any{"content": "x"}}}
		},
		"out-of-position ordinal": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "add_memory", "ordinal": 3.0, "payload": map[string]any{"content": "x"}}}
		},
		"invalid parent element": func(a map[string]any) { a["parent_ids"] = []any{42} },
		"empty parent element":   func(a map[string]any) { a["parent_ids"] = []any{""} },
		"non-array parents":      func(a map[string]any) { a["parent_ids"] = "head-1" },
		"empty ops":              func(a map[string]any) { a["ops"] = []any{} },
		"ops not array":          func(a map[string]any) { a["ops"] = "add_memory" },
		"wrong typed message":    func(a map[string]any) { a["message"] = 7 },
		"missing idempotency key": func(a map[string]any) {
			delete(a, "idempotency_key")
		},
		"add_memory without content": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "add_memory", "payload": map[string]any{}}}
		},
		"correct without target": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "correct_memory", "payload": map[string]any{"content": "x"}}}
		},
		"self supersede": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "supersede_memory", "target_event_id": "m1", "resulting_event_id": "m1"}}
		},
		"duplicate same targets": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "mark_duplicate", "target_event_id": "m1", "payload": map[string]any{"duplicate_of": "m1"}}}
		},
		"self relationship": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "add_relationship", "target_event_id": "m1", "resulting_event_id": "m1", "payload": map[string]any{"kind": "supports"}}}
		},
		"relationship without kind": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "add_relationship", "target_event_id": "m1", "resulting_event_id": "m2"}}
		},
		"deprecation without reason": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "propose_deprecation", "target_event_id": "m1"}}
		},
		"evidence without payload": func(a map[string]any) {
			a["ops"] = []any{map[string]any{"op_type": "attach_evidence", "target_event_id": "m1"}}
		},
	}
	for name, mutate := range cases {
		args := validChangesetArgs()
		mutate(args)
		if _, err := parseChangesetCreateArgs(args); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestParseChangesetCreateMixedValidInvalidRejectsAll(t *testing.T) {
	args := validChangesetArgs()
	args["ops"] = []any{
		map[string]any{"op_type": "add_memory", "payload": map[string]any{"content": "fine"}},
		map[string]any{"op_type": "add_memory"}, // malformed: no content
	}
	if _, err := parseChangesetCreateArgs(args); err == nil {
		t.Fatal("mixed valid/invalid ops must reject the whole request")
	}
}

func TestTypedOpsPreserveCountAndOrder(t *testing.T) {
	args := validChangesetArgs()
	args["ops"] = []any{
		map[string]any{"op_type": "add_memory", "payload": map[string]any{"content": "first"}},
		map[string]any{"op_type": "add_memory", "payload": map[string]any{"content": "second"}},
		map[string]any{"op_type": "attach_evidence", "target_event_id": "m1", "payload": map[string]any{"source": "ci"}},
	}
	typed, err := parseChangesetCreateArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	out := toLetheOps(typed.Ops)
	if len(out) != 3 {
		t.Fatalf("committed count = %d, want 3", len(out))
	}
	for i, want := range []string{"add_memory", "add_memory", "attach_evidence"} {
		if out[i].OpType != want || out[i].Ordinal != i {
			t.Fatalf("op %d = %s (ordinal %d), want %s", i, out[i].OpType, out[i].Ordinal, want)
		}
	}
}

func TestParseMergeProposeStrict(t *testing.T) {
	valid := map[string]any{
		"project":           "project-1",
		"target_ref":        "refs/shared/main",
		"head_changeset_id": "head-1",
		"idempotency_key":   "key-1",
	}
	if _, err := parseMergeProposeArgs(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown field":      func(a map[string]any) { a["extra"] = 1 },
		"fractional ordinal": func(a map[string]any) { a["selected_ops"] = []any{1.5} },
		"negative ordinal":   func(a map[string]any) { a["selected_ops"] = []any{-1.0} },
		"duplicate ordinal":  func(a map[string]any) { a["selected_ops"] = []any{1.0, 1.0} },
		"wrong typed head":   func(a map[string]any) { a["head_changeset_id"] = 42 },
	} {
		args := map[string]any{
			"project": "project-1", "target_ref": "refs/shared/main",
			"head_changeset_id": "head-1", "idempotency_key": "key-1",
		}
		mutate(args)
		if _, err := parseMergeProposeArgs(args); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestParseMergeStrict(t *testing.T) {
	if _, err := parseMergeArgs(map[string]any{"proposal_id": "p", "idempotency_key": "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := parseMergeArgs(map[string]any{"proposal_id": "p", "idempotency_key": "k", "admin_override": true}); err == nil {
		t.Fatal("unknown field must be rejected")
	}
	if _, err := parseMergeArgs(map[string]any{"proposal_id": "p"}); err == nil {
		t.Fatal("missing idempotency_key must be rejected")
	}
}

// The canonical typed representation is what the idempotency digest covers:
// same request → same digest, any behavioral change → different digest.
func TestTypedRepresentationDigestStable(t *testing.T) {
	first, err := parseChangesetCreateArgs(validChangesetArgs())
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseChangesetCreateArgs(validChangesetArgs())
	if err != nil {
		t.Fatal(err)
	}
	h1, h2 := hashForTest(t, first), hashForTest(t, second)
	if h1 != h2 {
		t.Fatal("identical typed requests must hash identically")
	}

	changed, err := parseChangesetCreateArgs(validChangesetArgs())
	if err != nil {
		t.Fatal(err)
	}
	changed.Message = "different message"
	if hashForTest(t, changed) == h1 {
		t.Fatal("behavioral change must change the digest")
	}
}

func hashForTest(t *testing.T, v any) string {
	t.Helper()
	data, err := canonical.JSON(v)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// The mirrored Lethe contract: size caps, closed payload key sets, and
// ambiguous-identifier rules reject before any upstream side effect.
func TestValidateOpStructureMirroredContractRules(t *testing.T) {
	reject := map[string]typedSemanticOp{
		"payload over 64 KiB cap": {
			OpType: "supersede_memory", TargetEventID: "m1",
			Payload: map[string]any{"approval": map[string]any{"blob": strings.Repeat("x", 70<<10)}},
		},
		"content over 16 KiB cap": {
			OpType:  "add_memory",
			Payload: map[string]any{"content": strings.Repeat("x", 17<<10)},
		},
		"string field over 1 KiB cap": {
			OpType:  "add_memory",
			Payload: map[string]any{"content": "ok", "scope": strings.Repeat("s", 2048)},
		},
		"too many tags": {
			OpType:  "add_memory",
			Payload: map[string]any{"content": "ok", "tags": make([]any, 65)},
		},
		"tag over 128-byte cap": {
			OpType:  "add_memory",
			Payload: map[string]any{"content": "ok", "tags": []any{strings.Repeat("t", 200)}},
		},
		"unknown payload key": {
			OpType:  "add_memory",
			Payload: map[string]any{"content": "ok", "bogus": 1},
		},
		"ambiguous resulting identity": {
			OpType:           "add_memory",
			ResultingEventID: "r1",
			Payload:          map[string]any{"content": "ok", "memory_id": "r2"},
		},
		"ambiguous replacement identity": {
			OpType:           "supersede_memory",
			TargetEventID:    "m1",
			ResultingEventID: "r1",
			Payload:          map[string]any{"memory_id": "r2"},
		},
		"ambiguous duplicate identity": {
			OpType:        "mark_duplicate",
			TargetEventID: "d1",
			Payload:       map[string]any{"duplicate_id": "d2", "duplicate_of": "c1"},
		},
		"ambiguous relationship endpoint": {
			OpType:        "add_relationship",
			TargetEventID: "f1",
			Payload:       map[string]any{"from_memory_id": "f2", "to_memory_id": "t1", "kind": "supports"},
		},
		"target-less attestation without summary": {
			OpType:  "attach_evidence",
			Payload: map[string]any{"proposal_id": "p1"},
		},
		"target-less attestation without provenance": {
			OpType:  "attach_verification",
			Payload: map[string]any{"summary": "reviewed merge"},
		},
	}
	for name, op := range reject {
		if err := validateOpStructure(0, op); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}

	accept := map[string]typedSemanticOp{
		"target-less attestation with summary and provenance": {
			OpType:  "attach_verification",
			Payload: map[string]any{"summary": "reviewed merge proposal p1", "proposal_id": "p1", "kind": "reviewed_merge"},
		},
		"identity repeated on two channels agrees": {
			OpType:           "add_memory",
			ResultingEventID: "r1",
			Payload:          map[string]any{"content": "ok", "memory_id": "r1"},
		},
		"targeted attachment": {
			OpType:        "attach_evidence",
			TargetEventID: "m1",
			Payload:       map[string]any{"note": "ci run green"},
		},
	}
	for name, op := range accept {
		if err := validateOpStructure(0, op); err != nil {
			t.Fatalf("%s: expected acceptance, got %v", name, err)
		}
	}
}

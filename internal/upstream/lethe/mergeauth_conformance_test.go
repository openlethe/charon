package lethe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlethe/charon/internal/canonical"
)

// mergeConformanceMock independently re-implements Lethe's envelope
// verification: decode, key-ID lookup, HMAC over canonical bytes, field
// binding, expiry, single-use nonce consumption.
type mergeConformanceMock struct {
	t      *testing.T
	key    string
	nonces sync.Map
}

func (m *mergeConformanceMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/memory/project/refs/merge" {
		http.NotFound(w, r)
		return
	}
	var req struct {
		RefName           string `json:"ref_name"`
		ExpectedHead      string `json:"expected_head"`
		NewHead           string `json:"new_head"`
		MergeProposalID   string `json:"merge_proposal_id"`
		ReviewerPrincipal string `json:"reviewer_principal"`
		Authorization     string `json:"merge_authorization"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		m.t.Fatalf("decode: %v", err)
	}
	var signed struct {
		Envelope  MergeAuthorizationEnvelope `json:"envelope"`
		Signature string                     `json:"signature"`
	}
	if err := json.Unmarshal([]byte(req.Authorization), &signed); err != nil {
		http.Error(w, "malformed authorization", http.StatusBadRequest)
		return
	}
	env := &signed.Envelope
	if env.Version != MergeAuthorizationVersion {
		http.Error(w, "bad version", http.StatusBadRequest)
		return
	}
	if env.KeyID != "" {
		http.Error(w, "unknown key id", http.StatusForbidden)
		return
	}
	signature, err := hex.DecodeString(signed.Signature)
	if err != nil {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	canonicalBytes, err := canonical.JSON(env)
	if err != nil {
		m.t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(m.key))
	_, _ = mac.Write(canonicalBytes)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		http.Error(w, "signature invalid", http.StatusForbidden)
		return
	}
	if env.ProjectID != "project" || env.RefName != req.RefName ||
		env.ExpectedHead != req.ExpectedHead || env.NewHead != req.NewHead ||
		env.ProposalID != req.MergeProposalID || env.ReviewerPrincipal != req.ReviewerPrincipal {
		http.Error(w, "field mismatch", http.StatusBadRequest)
		return
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, env.ExpiresAt)
	if err != nil || time.Now().After(expiresAt) {
		http.Error(w, "expired", http.StatusBadRequest)
		return
	}
	if _, loaded := m.nonces.LoadOrStore(env.Nonce, true); loaded {
		http.Error(w, "nonce replay", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"project_id":"project","ref_name":%q,"head_changeset_id":%q,"protected":true}`, req.RefName, req.NewHead)
}

// The envelope Charon signs must verify against Lethe's algorithm, and the
// nonce must be single-use even across separate client calls.
func TestMergeAuthorizationCrossServiceConformance(t *testing.T) {
	mock := &mergeConformanceMock{t: t, key: "0123456789abcdef0123456789abcdef"}
	server := httptest.NewServer(mock)
	defer server.Close()

	client := NewClient(server.URL, "api-key", 2*time.Second, 1<<20)
	client.SetMergeSigningKey(mock.key)
	digest := strings.Repeat("a", 64)

	ref, err := client.MergeAdvanceRef(t.Context(), "project", "refs/shared/main", MergeAdvanceRequest{
		ExpectedHead: "head-1", NewHead: "head-2",
		MergeProposalID: "proposal-1", ReviewerPrincipal: "reviewer-1",
		ProposalDigest: digest, MergerPrincipal: "merger-1", Strategy: StrategyFastForward,
	})
	if err != nil {
		t.Fatalf("first authorized merge rejected by verifier: %v", err)
	}
	if ref.HeadChangesetID != "head-2" {
		t.Fatalf("head = %s", ref.HeadChangesetID)
	}

	// A second call signs a fresh envelope (fresh nonce) and is accepted.
	if _, err := client.MergeAdvanceRef(t.Context(), "project", "refs/shared/main", MergeAdvanceRequest{
		ExpectedHead: "head-2", NewHead: "head-3",
		MergeProposalID: "proposal-1", ReviewerPrincipal: "reviewer-1",
		ProposalDigest: digest, MergerPrincipal: "merger-1", Strategy: StrategyFastForward,
	}); err != nil {
		t.Fatalf("second merge with fresh nonce rejected: %v", err)
	}

	// Replaying one captured authorization verbatim is rejected.
	authorization, err := client.signMergeAuthorizationV2("project", "refs/shared/main", "head-3", "head-4", "proposal-1", digest, "reviewer-1", "merger-1", StrategyFastForward)
	if err != nil {
		t.Fatal(err)
	}
	post := func() int {
		resp, err := client.httpClient.Post(server.URL+"/api/memory/project/refs/merge", "application/json",
			strings.NewReader(fmt.Sprintf(`{"ref_name":"refs/shared/main","expected_head":"head-3","new_head":"head-4","merge_proposal_id":"proposal-1","reviewer_principal":"reviewer-1","merge_authorization":%s}`, jsonString(t, authorization))))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(); code != http.StatusOK {
		t.Fatalf("first use of envelope status = %d, want 200", code)
	}
	if code := post(); code != http.StatusConflict {
		t.Fatalf("replayed authorization status = %d, want 409", code)
	}
}

func jsonString(t *testing.T, v string) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

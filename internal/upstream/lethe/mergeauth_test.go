package lethe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openlethe/charon/internal/canonical"
)

func newSigningClient() *Client {
	c := NewClient("http://localhost:0", "key", time.Second, 1<<20)
	c.SetMergeSigningKey("0123456789abcdef0123456789abcdef")
	return c
}

func decodeSigned(t *testing.T, authorization string) (MergeAuthorizationEnvelope, string) {
	t.Helper()
	var signed struct {
		Envelope  MergeAuthorizationEnvelope `json:"envelope"`
		Signature string                     `json:"signature"`
	}
	if err := json.Unmarshal([]byte(authorization), &signed); err != nil {
		t.Fatal(err)
	}
	return signed.Envelope, signed.Signature
}

func TestSignV2EnvelopeVerifiesWithSharedCanonicalForm(t *testing.T) {
	c := newSigningClient()
	digest := strings.Repeat("a", 64)
	authorization, err := c.signMergeAuthorizationV2("project", "refs/shared/main", "expected", "new", "proposal", digest, "reviewer", "merger", StrategyFastForward)
	if err != nil {
		t.Fatal(err)
	}
	env, signature := decodeSigned(t, authorization)

	if env.Version != MergeAuthorizationVersion {
		t.Fatalf("version = %s", env.Version)
	}
	if env.ProjectID != "project" || env.ExpectedHead != "expected" || env.NewHead != "new" ||
		env.ProposalID != "proposal" || env.ProposalDigest != digest ||
		env.ReviewerPrincipal != "reviewer" || env.MergerPrincipal != "merger" || env.Strategy != StrategyFastForward {
		t.Fatalf("envelope fields = %#v", env)
	}

	// Verification algorithm identical to Lethe's: HMAC over canonical bytes.
	canonicalBytes, err := canonical.JSON(env)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("0123456789abcdef0123456789abcdef"))
	_, _ = mac.Write(canonicalBytes)
	if hex.EncodeToString(mac.Sum(nil)) != signature {
		t.Fatal("signature does not verify against shared canonical form")
	}

	// Short expiry by default.
	issued, _ := time.Parse(time.RFC3339Nano, env.IssuedAt)
	expires, _ := time.Parse(time.RFC3339Nano, env.ExpiresAt)
	if ttl := expires.Sub(issued); ttl != DefaultMergeAuthorizationTTL {
		t.Fatalf("ttl = %s, want %s", ttl, DefaultMergeAuthorizationTTL)
	}
}

func TestSignV2EnvelopeUniqueNoncePerCall(t *testing.T) {
	c := newSigningClient()
	digest := strings.Repeat("a", 64)
	nonces := map[string]bool{}
	for i := 0; i < 32; i++ {
		authorization, err := c.signMergeAuthorizationV2("project", "refs/shared/main", "expected", "new", "proposal", digest, "reviewer", "merger", StrategyFastForward)
		if err != nil {
			t.Fatal(err)
		}
		env, _ := decodeSigned(t, authorization)
		if nonces[env.Nonce] {
			t.Fatalf("nonce %s reused", env.Nonce)
		}
		nonces[env.Nonce] = true
		if len(env.Nonce) != 32 {
			t.Fatalf("nonce form: %q", env.Nonce)
		}
	}
}

func TestSignV2EnvelopeKeyIDAndTTLConfig(t *testing.T) {
	c := newSigningClient()
	c.SetMergeSigningKeyID("key-2026-07")
	c.SetMergeAuthorizationTTL(5 * time.Minute)
	authorization, err := c.signMergeAuthorizationV2("p", "r", "e", "n", "prop", strings.Repeat("a", 64), "rev", "mer", StrategyMergeCommit)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := decodeSigned(t, authorization)
	if env.KeyID != "key-2026-07" {
		t.Fatalf("key id = %q", env.KeyID)
	}
	issued, _ := time.Parse(time.RFC3339Nano, env.IssuedAt)
	expires, _ := time.Parse(time.RFC3339Nano, env.ExpiresAt)
	if expires.Sub(issued) != 5*time.Minute {
		t.Fatalf("ttl = %s", expires.Sub(issued))
	}

	// Out-of-range TTL falls back to the short default, never long-lived.
	c.SetMergeAuthorizationTTL(time.Hour)
	authorization, err = c.signMergeAuthorizationV2("p", "r", "e", "n", "prop", strings.Repeat("a", 64), "rev", "mer", StrategyMergeCommit)
	if err != nil {
		t.Fatal(err)
	}
	env, _ = decodeSigned(t, authorization)
	issued, _ = time.Parse(time.RFC3339Nano, env.IssuedAt)
	expires, _ = time.Parse(time.RFC3339Nano, env.ExpiresAt)
	if expires.Sub(issued) != DefaultMergeAuthorizationTTL {
		t.Fatalf("over-long ttl not clamped: %s", expires.Sub(issued))
	}
}

func TestSignV2RequiresBindingFields(t *testing.T) {
	c := newSigningClient()
	if _, err := c.signMergeAuthorizationV2("p", "r", "e", "n", "", "digest", "rev", "mer", StrategyFastForward); err == nil {
		t.Fatal("missing proposal accepted")
	}
	if _, err := c.signMergeAuthorizationV2("p", "r", "e", "n", "prop", "", "rev", "mer", StrategyFastForward); err == nil {
		t.Fatal("missing digest accepted")
	}
	if _, err := c.signMergeAuthorizationV2("p", "r", "e", "n", "prop", "digest", "", "mer", StrategyFastForward); err == nil {
		t.Fatal("missing reviewer accepted")
	}
	if _, err := c.signMergeAuthorizationV2("p", "r", "e", "n", "prop", "digest", "rev", "", StrategyFastForward); err == nil {
		t.Fatal("missing merger accepted")
	}
	c2 := NewClient("http://localhost:0", "key", time.Second, 1<<20)
	if _, err := c2.signMergeAuthorizationV2("p", "r", "e", "n", "prop", "digest", "rev", "mer", StrategyFastForward); err == nil {
		t.Fatal("unsigned client produced an authorization")
	}
}

package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testS256Challenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// TestConcurrentMapAccess verifies that concurrent access to auth codes
// and tokens does not trigger a race condition.
func TestConcurrentMapAccess(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			verifier := "verifier-" + strconv.Itoa(i)
			code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", testS256Challenge(verifier), "S256")
			if err != nil {
				t.Errorf("GenerateAuthCode failed: %v", err)
				return
			}
			_, err = s.ExchangeAuthCode(code, "chatgpt-mcp", "https://example.com/callback", verifier)
			if err != nil {
				t.Errorf("ExchangeAuthCode failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

// TestPublicClientCredentialRejection verifies that public clients cannot
// use the client_credentials grant.
func TestPublicClientCredentialRejection(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	h := NewHandlers(s, "", []string{"https://example.com/callback"}, "pairing-secret-at-least-32-characters")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := strings.NewReader("grant_type=client_credentials&client_id=chatgpt-mcp")
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for public client_credentials, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "unauthorized_client") {
		t.Errorf("expected unauthorized_client error, got %s", rr.Body.String())
	}
}

func TestPublicClientRequiresS256AtServiceBoundary(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", "verifier", "plain"); err == nil {
		t.Fatal("public client was allowed to use plain PKCE")
	}
	if _, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", "", ""); err == nil {
		t.Fatal("public client was allowed to omit PKCE")
	}
}

// TestRedirectURIMismatch verifies that authorization code exchange fails
// when the redirect_uri does not match the one stored during authorization.
func TestRedirectURIMismatch(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", "challenge", "S256")
	if err != nil {
		t.Fatalf("GenerateAuthCode failed: %v", err)
	}

	_, err = s.ExchangeAuthCode(code, "chatgpt-mcp", "https://evil.com/callback", "verifier123456789012345678901234567890123456789012345678901234567890")
	if err == nil {
		t.Fatal("expected error for redirect_uri mismatch")
	}
	if !strings.Contains(err.Error(), "redirect_uri mismatch") {
		t.Errorf("expected redirect_uri mismatch error, got: %v", err)
	}
}

// TestRedirectURIMatch verifies that authorization code exchange succeeds
// when the redirect_uri matches the one stored during authorization.
func TestRedirectURIMatch(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	verifier := "redirect-match-verifier"
	code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", testS256Challenge(verifier), "S256")
	if err != nil {
		t.Fatalf("GenerateAuthCode failed: %v", err)
	}

	resp, err := s.ExchangeAuthCode(code, "chatgpt-mcp", "https://example.com/callback", verifier)
	if err != nil {
		t.Fatalf("ExchangeAuthCode failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("expected access token")
	}
}

func TestPairedAuthCodeStripsMutationScopes(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatal(err)
	}
	s.SetScopeLookup(func(string) ([]string, error) {
		return []string{"memory.read", "memory.branch", "memory.commit", "memory.propose"}, nil
	})
	verifier := "safe-scope-verifier"
	code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", testS256Challenge(verifier), "S256")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.ExchangeAuthCode(code, "chatgpt-mcp", "https://example.com/callback", verifier)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ValidateAccessToken(resp.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, scope := range claims.Scopes {
		got[scope] = true
	}
	if got["memory.branch"] || got["memory.commit"] {
		t.Fatalf("paired token retained mutation scopes: %#v", claims.Scopes)
	}
	if !got["memory.read"] || !got["memory.propose"] {
		t.Fatalf("safe scopes missing: %#v", claims.Scopes)
	}
}

func TestTrustedAuthorAuthCodeRetainsOnlyMemoryGitAuthorScopes(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatal(err)
	}
	s.SetAllowAuthorScopes(true)
	s.SetScopeLookup(func(string) ([]string, error) {
		return []string{
			"memory.read", "memory.search", "thread.read", "memory.propose",
			"memory.branch", "memory.commit",
			"memory.review", "memory.merge", "memory.write", "proposal.review",
		}, nil
	})
	verifier := "trusted-author-verifier"
	code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", testS256Challenge(verifier), "S256")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.ExchangeAuthCode(code, "chatgpt-mcp", "https://example.com/callback", verifier)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.ValidateAccessToken(resp.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, scope := range claims.Scopes {
		got[scope] = true
	}
	for _, scope := range []string{"memory.read", "memory.search", "thread.read", "memory.propose", "memory.branch", "memory.commit"} {
		if !got[scope] {
			t.Fatalf("trusted author token missing %s: %#v", scope, claims.Scopes)
		}
	}
	for _, scope := range []string{"memory.review", "memory.merge", "memory.write", "proposal.review"} {
		if got[scope] {
			t.Fatalf("trusted author token retained elevated scope %s: %#v", scope, claims.Scopes)
		}
	}
}

// TestConfidentialClientCredentials verifies that confidential clients can
// use the client_credentials grant.
func TestConfidentialClientWithoutSecretFailsClosed(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatal(err)
	}
	s.RegisterClient(&Client{ID: "broken-confidential", Public: false})
	h := NewHandlers(s, "", []string{"https://example.com/callback"}, "pairing-secret-at-least-32-characters")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := strings.NewReader("grant_type=client_credentials&client_id=broken-confidential")
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "invalid_client") {
		t.Fatalf("empty-secret confidential client was not rejected: %d %s", rr.Code, rr.Body.String())
	}
}

func TestConfidentialClientCredentials(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	s.RegisterClient(&Client{
		ID:     "confidential-client",
		Secret: "secret123",
		Public: false,
	})
	h := NewHandlers(s, "", []string{"https://example.com/callback"}, "pairing-secret-at-least-32-characters")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := strings.NewReader("grant_type=client_credentials&client_id=confidential-client&client_secret=secret123")
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for confidential client_credentials, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "access_token") {
		t.Errorf("expected access_token in response, got %s", rr.Body.String())
	}

	elevated := strings.NewReader("grant_type=client_credentials&client_id=confidential-client&client_secret=secret123&scope=memory.write")
	elevatedReq := httptest.NewRequest(http.MethodPost, "/oauth/token", elevated)
	elevatedReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	elevatedResult := httptest.NewRecorder()
	mux.ServeHTTP(elevatedResult, elevatedReq)
	if elevatedResult.Code != http.StatusBadRequest || !strings.Contains(elevatedResult.Body.String(), "invalid_scope") {
		t.Fatalf("elevated client scope was not rejected: %d %s", elevatedResult.Code, elevatedResult.Body.String())
	}
}

// TestClientIDMismatch verifies that authorization code exchange fails
// when the client_id does not match the one stored during authorization.
func TestClientIDMismatch(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	verifier := "client-mismatch-verifier"
	code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", testS256Challenge(verifier), "S256")
	if err != nil {
		t.Fatalf("GenerateAuthCode failed: %v", err)
	}

	_, err = s.ExchangeAuthCode(code, "other-client", "https://example.com/callback", verifier)
	if err == nil {
		t.Fatal("expected error for client_id mismatch")
	}
	if !strings.Contains(err.Error(), "client_id mismatch") {
		t.Errorf("expected client_id mismatch error, got: %v", err)
	}
}

// TestAuthCodeExpiry verifies that expired authorization codes are rejected.
func TestAuthCodeExpiry(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	verifier := "expiry-verifier"
	code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", testS256Challenge(verifier), "S256")
	if err != nil {
		t.Fatalf("GenerateAuthCode failed: %v", err)
	}

	// Manually expire the code
	s.mu.Lock()
	s.authCodes[code].ExpiresAt = time.Now().Add(-time.Minute)
	s.mu.Unlock()

	_, err = s.ExchangeAuthCode(code, "chatgpt-mcp", "https://example.com/callback", verifier)
	if err == nil {
		t.Fatal("expected error for expired code")
	}
	if !strings.Contains(err.Error(), "invalid authorization code") {
		t.Errorf("expected invalid authorization code error, got: %v", err)
	}
}

// TestOAuthMetadata verifies that the OAuth metadata endpoints include client_id
// and that the metadata URL is correctly constructed.
func TestOAuthMetadata(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	// Verify metadata URL
	metadataURL := s.OAuthMetadataURL()
	expected := "https://example.com/.well-known/oauth-protected-resource"
	if metadataURL != expected {
		t.Errorf("OAuthMetadataURL() = %q, want %q", metadataURL, expected)
	}

	// Verify client ID
	if s.OAuthClientID() != "chatgpt-mcp" {
		t.Errorf("OAuthClientID() = %q, want %q", s.OAuthClientID(), "chatgpt-mcp")
	}

	// Verify protected resource metadata includes client_id
	prMeta := s.ProtectedResourceMetadata()
	if prMeta["client_id"] != "chatgpt-mcp" {
		t.Errorf("ProtectedResourceMetadata client_id = %v, want chatgpt-mcp", prMeta["client_id"])
	}
	if prMeta["resource"] != "https://example.com/mcp" {
		t.Errorf("ProtectedResourceMetadata resource = %v, want https://example.com/mcp", prMeta["resource"])
	}

	// Verify authorization server metadata includes client_id
	asMeta := s.AuthorizationServerMetadata()
	if asMeta["client_id"] != "chatgpt-mcp" {
		t.Errorf("AuthorizationServerMetadata client_id = %v, want chatgpt-mcp", asMeta["client_id"])
	}
}

// TestOAuthMetadataNoClientID verifies metadata without client_id configured.
func TestOAuthMetadataNoClientID(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	prMeta := s.ProtectedResourceMetadata()
	if _, ok := prMeta["client_id"]; ok {
		t.Error("ProtectedResourceMetadata should not include client_id when empty")
	}

	asMeta := s.AuthorizationServerMetadata()
	if _, ok := asMeta["client_id"]; ok {
		t.Error("AuthorizationServerMetadata should not include client_id when empty")
	}
}

// TestHMACKeyLength verifies that short HMAC keys are rejected.
func TestHMACKeyLength(t *testing.T) {
	_, err := NewService([]byte("short"), "https://example.com", "chatgpt-mcp")
	if err == nil {
		t.Fatal("expected error for short HMAC key")
	}
	if !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Errorf("expected HMAC key length error, got: %v", err)
	}
}

// TestConfiguredClientID verifies that a non-default client ID is registered.
func TestConfiguredClientID(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "custom-client")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	// Both default and custom client should be registered
	if s.GetClient("chatgpt-mcp") == nil {
		t.Error("default chatgpt-mcp client should still be registered")
	}
	if s.GetClient("custom-client") == nil {
		t.Error("configured custom-client should be registered")
	}
}

// TestUnknownClientRejected verifies that the paired principal flow is
// limited to the explicitly configured client.
func TestUnknownClientRejected(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	h := NewHandlers(s, "", []string{"https://example.com/callback"}, "pairing-secret-at-least-32-characters")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Request auth with an unknown client ID
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?response_type=code&client_id=chatgpt-generated-id&redirect_uri=https://example.com/callback&state=xyz&code_challenge=challenge", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if s.GetClient("chatgpt-generated-id") != nil {
		t.Error("unknown client must not be registered")
	}
}

func TestOAuthMetadataAdvertisesOnlySupportedPublicFlow(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(s.AuthorizationServerMetadata())
	if err != nil {
		t.Fatal(err)
	}
	metadata := string(encoded)
	for _, forbidden := range []string{"client_credentials", "client_secret_basic", "memory.review", "memory.merge", "memory.write", "proposal.review"} {
		if strings.Contains(metadata, forbidden) {
			t.Fatalf("metadata advertised unsupported capability %q: %s", forbidden, metadata)
		}
	}
	for _, required := range []string{"authorization_code", "S256", "memory.read", "memory.branch", "memory.commit"} {
		if !strings.Contains(metadata, required) {
			t.Fatalf("metadata missing supported capability %q: %s", required, metadata)
		}
	}
}

func TestAuthorizationRequiresPairingSecret(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatal(err)
	}
	s.SetScopeLookup(func(string) ([]string, error) { return []string{"memory.read"}, nil })
	const pairingSecret = "pairing-secret-at-least-32-characters"
	h := NewHandlers(s, "principal-1", []string{"https://example.com/callback"}, pairingSecret)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {"chatgpt-mcp"},
		"redirect_uri":          {"https://example.com/callback"},
		"state":                 {"state-1"},
		"code_challenge":        {"challenge"},
		"code_challenge_method": {"S256"},
	}
	unsupported := url.Values{}
	for key, values := range params {
		unsupported[key] = append([]string(nil), values...)
	}
	unsupported.Set("response_type", "token")
	unsupportedRequest := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+unsupported.Encode(), nil)
	unsupportedResult := httptest.NewRecorder()
	mux.ServeHTTP(unsupportedResult, unsupportedRequest)
	if unsupportedResult.Code != http.StatusBadRequest || !strings.Contains(unsupportedResult.Body.String(), "unsupported response_type") {
		t.Fatalf("unsupported response_type was not rejected: %d %s", unsupportedResult.Code, unsupportedResult.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, get)
	pageBody := page.Body.String()
	csp := page.Header().Get("Content-Security-Policy")
	if page.Header().Get("Cache-Control") != "no-store" || !strings.Contains(csp, "img-src data:") || !strings.Contains(csp, "script-src 'nonce-") || !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "form-action 'self' https://example.com") {
		t.Fatalf("pairing page security headers = cache:%q csp:%q", page.Header().Get("Cache-Control"), csp)
	}
	for _, required := range []string{
		"Authorization key",
		"chatgpt-mcp",
		"https://example.com/callback",
		"Only continue when these details match the connector you are configuring.",
		"--bg: #f5f0e8",
		"The primary Compose profile prints a fresh key once",
		"submission-feedback",
		"Authorizing…",
		"<script nonce=",
	} {
		if page.Code != http.StatusOK || !strings.Contains(pageBody, required) {
			t.Fatalf("pairing page missing %q: status=%d body=%q", required, page.Code, pageBody)
		}
	}

	params.Set("pairing_secret", "wrong-secret")
	wrong := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(params.Encode()))
	wrong.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wrongResult := httptest.NewRecorder()
	mux.ServeHTTP(wrongResult, wrong)
	if wrongResult.Code != http.StatusUnauthorized || len(s.authCodes) != 0 || !strings.Contains(wrongResult.Body.String(), "Copy the newest key from the boxed startup banner") {
		t.Fatalf("wrong pairing secret response: status=%d codes=%d body=%q", wrongResult.Code, len(s.authCodes), wrongResult.Body.String())
	}

	params.Set("pairing_secret", "  "+pairingSecret+"\n")
	correct := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(params.Encode()))
	correct.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	correctResult := httptest.NewRecorder()
	mux.ServeHTTP(correctResult, correct)
	if correctResult.Code != http.StatusFound || !strings.Contains(correctResult.Header().Get("Location"), "code=") {
		t.Fatalf("correct pairing result = %d %q", correctResult.Code, correctResult.Header().Get("Location"))
	}
}

func TestRedirectAllowlist(t *testing.T) {
	if !redirectAllowed("https://chatgpt.com/connector/oauth/app-123", []string{"https://chatgpt.com/connector/oauth/*"}) {
		t.Fatal("expected ChatGPT callback prefix to be allowed")
	}
	for _, candidate := range []string{
		"https://evil.example/callback",
		"https://chatgpt.com.evil.example/connector/oauth/app-123",
		"https://chatgpt.com/other/path",
		"https://chatgpt.com/connector/oauth/../other",
		"https://chatgpt.com/connector/oauth/%2e%2e/other",
		"https://chatgpt.com/connector/oauth/app%2fother",
		"https://chatgpt.com/connector/oauth/app%5cother",
		"https://chatgpt.com@evil.example/connector/oauth/app-123",
	} {
		if redirectAllowed(candidate, []string{"https://chatgpt.com/connector/oauth/*"}) {
			t.Errorf("unexpectedly allowed %q", candidate)
		}
	}
}

// TestRateLimiting verifies that the token endpoint rate limits per client.
func TestRateLimitAddressIgnoresEphemeralPort(t *testing.T) {
	first := rateLimitAddress("192.0.2.10:41000")
	second := rateLimitAddress("192.0.2.10:42000")
	if first != "192.0.2.10" || first != second {
		t.Fatalf("rate limit keys = %q and %q", first, second)
	}
}

func TestRateLimiterCleansExpiredKeys(t *testing.T) {
	limiter := NewRateLimiter(time.Millisecond, 2)
	limiter.attempts["stale"] = []time.Time{time.Now().Add(-time.Hour)}
	limiter.lastCleanup = time.Now().Add(-time.Hour)
	if !limiter.Allow("active") {
		t.Fatal("active request was unexpectedly denied")
	}
	if _, exists := limiter.attempts["stale"]; exists {
		t.Fatal("expired rate-limit key was retained")
	}
}

func TestRateLimiting(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	h := NewHandlers(s, "", []string{"https://example.com/callback"}, "pairing-secret-at-least-32-characters")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Use a low rate limit for testing
	h.rateLimiter = NewRateLimiter(1*time.Minute, 2)

	// First two requests should succeed
	for i := 0; i < 2; i++ {
		body := strings.NewReader("grant_type=client_credentials&client_id=chatgpt-mcp")
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: expected 401 (public client rejected), got %d", i+1, rr.Code)
		}
	}

	// Third request should be rate limited
	body := strings.NewReader("grant_type=client_credentials&client_id=chatgpt-mcp")
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 for rate limited request, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "slow_down") {
		t.Errorf("expected slow_down error, got %s", rr.Body.String())
	}
}

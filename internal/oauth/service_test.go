package oauth

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

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
			// Use plain PKCE to avoid computing correct verifier
			code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", "plainverifier"+strconv.Itoa(i), "plain")
			if err != nil {
				t.Errorf("GenerateAuthCode failed: %v", err)
				return
			}
			_, err = s.ExchangeAuthCode(code, "chatgpt-mcp", "https://example.com/callback", "plainverifier"+strconv.Itoa(i))
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
	h := NewHandlers(s, "", []string{"https://example.com/callback"})

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

	code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", "challenge", "S256")
	if err != nil {
		t.Fatalf("GenerateAuthCode failed: %v", err)
	}
	_ = code

	// Compute correct S256 verifier — use plain method for simplicity in this test
	code2, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", "plainverifier", "plain")
	if err != nil {
		t.Fatalf("GenerateAuthCode failed: %v", err)
	}

	resp, err := s.ExchangeAuthCode(code2, "chatgpt-mcp", "https://example.com/callback", "plainverifier")
	if err != nil {
		t.Fatalf("ExchangeAuthCode failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("expected access token")
	}
}

// TestConfidentialClientCredentials verifies that confidential clients can
// use the client_credentials grant.
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
	h := NewHandlers(s, "", []string{"https://example.com/callback"})

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
}

// TestClientIDMismatch verifies that authorization code exchange fails
// when the client_id does not match the one stored during authorization.
func TestClientIDMismatch(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", "", "")
	if err != nil {
		t.Fatalf("GenerateAuthCode failed: %v", err)
	}

	_, err = s.ExchangeAuthCode(code, "other-client", "https://example.com/callback", "")
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

	code, err := s.GenerateAuthCode("chatgpt-mcp", "user", "https://example.com/callback", "", "")
	if err != nil {
		t.Fatalf("GenerateAuthCode failed: %v", err)
	}

	// Manually expire the code
	s.mu.Lock()
	s.authCodes[code].ExpiresAt = time.Now().Add(-time.Minute)
	s.mu.Unlock()

	_, err = s.ExchangeAuthCode(code, "chatgpt-mcp", "https://example.com/callback", "")
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

// TestUnknownClientRejected verifies that the auto-approved principal flow is
// limited to the explicitly configured client.
func TestUnknownClientRejected(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	h := NewHandlers(s, "", []string{"https://example.com/callback"})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Request auth with an unknown client ID
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id=chatgpt-generated-id&redirect_uri=https://example.com/callback&state=xyz&code_challenge=challenge", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if s.GetClient("chatgpt-generated-id") != nil {
		t.Error("unknown client must not be registered")
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
		"https://chatgpt.com@evil.example/connector/oauth/app-123",
	} {
		if redirectAllowed(candidate, []string{"https://chatgpt.com/connector/oauth/*"}) {
			t.Errorf("unexpectedly allowed %q", candidate)
		}
	}
}

// TestRateLimiting verifies that the token endpoint rate limits per client.
func TestRateLimiting(t *testing.T) {
	s, err := NewService([]byte("test-key-32-bytes-long-for-hs256"), "https://example.com", "chatgpt-mcp")
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	h := NewHandlers(s, "", []string{"https://example.com/callback"})

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

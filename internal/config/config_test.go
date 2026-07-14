package config

import (
	"strings"
	"testing"
)

func TestOAuthRequiresRedirectAllowlist(t *testing.T) {
	t.Setenv("CHARON_AUTH_MODE", "oauth")
	t.Setenv("CHARON_PUBLIC_URL", "https://charon.example.test")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	t.Setenv("CHARON_HMAC_KEY", strings.Repeat("h", 32))
	t.Setenv("CHARON_OAUTH_REDIRECT_URIS", "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "CHARON_OAUTH_REDIRECT_URIS") {
		t.Fatalf("expected redirect allowlist error, got %v", err)
	}
}

func TestOAuthParsesRedirectAllowlist(t *testing.T) {
	t.Setenv("CHARON_AUTH_MODE", "oauth")
	t.Setenv("CHARON_PUBLIC_URL", "https://charon.example.test")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	t.Setenv("CHARON_HMAC_KEY", strings.Repeat("h", 32))
	t.Setenv("CHARON_OAUTH_REDIRECT_URIS", "https://chatgpt.com/connector/oauth/*, https://chatgpt.com/connector_platform_oauth_redirect")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(cfg.OAuthRedirectURIs) != 2 {
		t.Fatalf("redirect allowlist = %v", cfg.OAuthRedirectURIs)
	}
}

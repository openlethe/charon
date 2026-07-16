package config

import (
	"strings"
	"testing"
	"time"
)

func setTestOnlyConfig(t *testing.T) {
	t.Helper()
	t.Setenv("CHARON_AUTH_MODE", "none")
	t.Setenv("CHARON_HTTP", "127.0.0.1:0")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	t.Setenv("CHARON_HMAC_KEY", strings.Repeat("h", 32))
}

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
	t.Setenv("CHARON_OAUTH_PAIRING_SECRET", strings.Repeat("p", 32))
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(cfg.OAuthRedirectURIs) != 2 {
		t.Fatalf("redirect allowlist = %v", cfg.OAuthRedirectURIs)
	}
}

func TestOAuthGeneratesPairingSecretWhenEnabled(t *testing.T) {
	t.Setenv("CHARON_AUTH_MODE", "oauth")
	t.Setenv("CHARON_PUBLIC_URL", "https://charon.example.test")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	t.Setenv("CHARON_HMAC_KEY", strings.Repeat("h", 32))
	t.Setenv("CHARON_OAUTH_REDIRECT_URIS", "https://chatgpt.com/connector/oauth/*")
	t.Setenv("CHARON_OAUTH_GENERATE_PAIRING_SECRET", "true")

	first, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	second, err := FromEnv()
	if err != nil {
		t.Fatalf("second FromEnv: %v", err)
	}
	if !first.OAuthPairingSecretGenerated || len(first.OAuthPairingSecret) < 32 {
		t.Fatalf("generated authorization key state = generated:%v length:%d", first.OAuthPairingSecretGenerated, len(first.OAuthPairingSecret))
	}
	if first.OAuthPairingSecret == second.OAuthPairingSecret {
		t.Fatal("OAuth authorization key was reused across independent generations")
	}
}

func TestOAuthRequiresPairingSecretWhenGenerationIsDisabled(t *testing.T) {
	t.Setenv("CHARON_AUTH_MODE", "oauth")
	t.Setenv("CHARON_PUBLIC_URL", "https://charon.example.test")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	t.Setenv("CHARON_HMAC_KEY", strings.Repeat("h", 32))
	t.Setenv("CHARON_OAUTH_REDIRECT_URIS", "https://chatgpt.com/connector/oauth/*")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "CHARON_OAUTH_PAIRING_SECRET") {
		t.Fatalf("expected missing pairing secret error, got %v", err)
	}
}

func TestOAuthRejectsShortConfiguredPairingSecret(t *testing.T) {
	t.Setenv("CHARON_AUTH_MODE", "oauth")
	t.Setenv("CHARON_PUBLIC_URL", "https://charon.example.test")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	t.Setenv("CHARON_HMAC_KEY", strings.Repeat("h", 32))
	t.Setenv("CHARON_OAUTH_REDIRECT_URIS", "https://chatgpt.com/connector/oauth/*")
	t.Setenv("CHARON_OAUTH_PAIRING_SECRET", "too-short")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "CHARON_OAUTH_PAIRING_SECRET") {
		t.Fatalf("expected short pairing secret error, got %v", err)
	}
}

func TestOAuthForcedGenerationOverridesConfiguredPairingSecret(t *testing.T) {
	t.Setenv("CHARON_AUTH_MODE", "oauth")
	t.Setenv("CHARON_PUBLIC_URL", "https://charon.example.test")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	t.Setenv("CHARON_HMAC_KEY", strings.Repeat("h", 32))
	t.Setenv("CHARON_OAUTH_REDIRECT_URIS", "https://chatgpt.com/connector/oauth/*")
	configured := strings.Repeat("p", 32)
	t.Setenv("CHARON_OAUTH_PAIRING_SECRET", configured)
	t.Setenv("CHARON_OAUTH_GENERATE_PAIRING_SECRET", "true")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if !cfg.OAuthPairingSecretGenerated || cfg.OAuthPairingSecret == configured {
		t.Fatalf("forced generation did not replace configured key")
	}
}

func TestMemoryGitModeFromEnv(t *testing.T) {
	setTestOnlyConfig(t)
	t.Setenv("CHARON_MODE", "memory-git")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.Mode != ModeMemoryGit {
		t.Fatalf("mode = %q, want %q", cfg.Mode, ModeMemoryGit)
	}
}

func TestOAuthAuthorScopesOptInRequiresMemoryGitMode(t *testing.T) {
	setTestOnlyConfig(t)
	t.Setenv("CHARON_MODE", "direct")
	t.Setenv("CHARON_OAUTH_ALLOW_AUTHOR_SCOPES", "true")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "requires CHARON_MODE=memory-git") {
		t.Fatalf("expected memory-git mode requirement, got %v", err)
	}
}

func TestOAuthAuthorScopesOptInParsesInMemoryGitMode(t *testing.T) {
	setTestOnlyConfig(t)
	t.Setenv("CHARON_MODE", "memory-git")
	t.Setenv("CHARON_OAUTH_ALLOW_AUTHOR_SCOPES", "true")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if !cfg.OAuthAllowAuthorScopes {
		t.Fatal("OAuthAllowAuthorScopes = false, want true")
	}
}

func TestOAuthAuthorScopesRejectsInvalidBoolean(t *testing.T) {
	setTestOnlyConfig(t)
	t.Setenv("CHARON_OAUTH_ALLOW_AUTHOR_SCOPES", "sometimes")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "CHARON_OAUTH_ALLOW_AUTHOR_SCOPES") {
		t.Fatalf("expected invalid boolean error, got %v", err)
	}
}

func TestInvalidModeFailsClosed(t *testing.T) {
	setTestOnlyConfig(t)
	t.Setenv("CHARON_MODE", "legacy-bypass")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "unsupported CHARON_MODE") {
		t.Fatalf("expected unsupported mode error, got %v", err)
	}
}

func TestInvalidAuthModeFailsClosed(t *testing.T) {
	t.Setenv("CHARON_AUTH_MODE", "optional")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "unsupported CHARON_AUTH_MODE") {
		t.Fatalf("expected unsupported auth mode error, got %v", err)
	}
}

func TestAuthenticatedPublicURLRequiresHTTPSOutsideLoopback(t *testing.T) {
	t.Setenv("CHARON_MODE", "direct")
	t.Setenv("CHARON_AUTH_MODE", "obol")
	t.Setenv("CHARON_PUBLIC_URL", "http://charon.example.test")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	t.Setenv("CHARON_OBOL_HMAC_KEY", strings.Repeat("o", 32))
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "https is required") {
		t.Fatalf("expected HTTPS requirement, got %v", err)
	}
}

func TestUpstreamRejectsBasePath(t *testing.T) {
	setTestOnlyConfig(t)
	t.Setenv("CHARON_MODE", "direct")
	t.Setenv("CHARON_UPSTREAM", "http://127.0.0.1:18483/prefix")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("expected upstream path rejection, got %v", err)
	}
}

func TestAuthNoneRequiresLoopbackBind(t *testing.T) {
	t.Setenv("CHARON_AUTH_MODE", "none")
	t.Setenv("CHARON_HTTP", ":18484")
	t.Setenv("LETHE_API_KEY", "test-lethe-key")
	t.Setenv("CHARON_HMAC_KEY", strings.Repeat("h", 32))
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "requires a loopback") {
		t.Fatalf("expected loopback bind error, got %v", err)
	}
}

func TestOperationalLimitsParseFromEnv(t *testing.T) {
	setTestOnlyConfig(t)
	t.Setenv("CHARON_MAX_RESULT_BYTES", "2097152")
	t.Setenv("CHARON_MAX_RESULT_COUNT", "250")
	t.Setenv("CHARON_MAX_REQUEST_BYTES", "524288")
	t.Setenv("CHARON_REQUEST_TIMEOUT", "25s")
	t.Setenv("CHARON_HTTP_READ_HEADER_TIMEOUT", "7s")
	t.Setenv("CHARON_HTTP_READ_TIMEOUT", "45s")
	t.Setenv("CHARON_HTTP_IDLE_TIMEOUT", "3m")
	t.Setenv("CHARON_HTTP_SHUTDOWN_TIMEOUT", "20s")
	t.Setenv("CHARON_HTTP_MAX_HEADER_BYTES", "65536")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.MaxResultBytes != 2097152 || cfg.MaxResultCount != 250 || cfg.MaxRequestBytes != 524288 {
		t.Fatalf("limits not parsed: %#v", cfg)
	}
	if cfg.RequestTimeout != 25*time.Second || cfg.HTTPReadHeaderTimeout != 7*time.Second ||
		cfg.HTTPReadTimeout != 45*time.Second || cfg.HTTPIdleTimeout != 3*time.Minute ||
		cfg.HTTPShutdownTimeout != 20*time.Second || cfg.HTTPMaxHeaderBytes != 65536 {
		t.Fatalf("timeouts not parsed: %#v", cfg)
	}
}

func TestPurposeSpecificKeysOverrideLegacyFallback(t *testing.T) {
	setTestOnlyConfig(t)
	t.Setenv("CHARON_MODE", "memory-git")
	t.Setenv("CHARON_OBOL_HMAC_KEY", strings.Repeat("o", 32))
	t.Setenv("CHARON_OAUTH_HMAC_KEY", strings.Repeat("a", 32))
	t.Setenv("CHARON_MERGE_HMAC_KEY", strings.Repeat("m", 32))
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ObolKey() != strings.Repeat("o", 32) || cfg.OAuthKey() != strings.Repeat("a", 32) || cfg.MergeKey() != strings.Repeat("m", 32) {
		t.Fatalf("purpose-specific keys were not selected")
	}
}

func TestOperationalLimitsRejectUnsafeValues(t *testing.T) {
	setTestOnlyConfig(t)
	t.Setenv("CHARON_MAX_REQUEST_BYTES", "0")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "CHARON_MAX_REQUEST_BYTES") {
		t.Fatalf("expected request limit error, got %v", err)
	}
}

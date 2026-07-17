package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Mode determines how Charon operates.
type Mode string

const (
	// ModeDirect preserves the original Charon tool surface for compatibility.
	ModeDirect Mode = "direct"
	// ModeMemoryGit exposes only versioned Memory Git tools. It is the required
	// mode for frontier-model access through Charon.
	ModeMemoryGit Mode = "memory-git"
)

// Config holds all Charon configuration.
type Config struct {
	// Mode determines whether Charon exposes the compatibility surface or
	// the fail-closed Memory Git-only surface.
	Mode Mode `env:"CHARON_MODE" envDefault:"memory-git"`

	// HTTP is the public MCP listener address.
	HTTP string `env:"CHARON_HTTP" envDefault:":18484"`

	// AdminHTTP is the admin listener address. Must be loopback or Unix socket.
	AdminHTTP string `env:"CHARON_ADMIN_HTTP" envDefault:"127.0.0.1:18486"`

	// Upstream is the Lethe API URL.
	Upstream string `env:"CHARON_UPSTREAM" envDefault:"http://localhost:18485"`

	// PublicURL is the external URL clients use to reach Charon.
	PublicURL string `env:"CHARON_PUBLIC_URL"`

	// OAuthClientID is the client_id advertised in OAuth protected resource metadata.
	OAuthClientID string `env:"CHARON_OAUTH_CLIENT_ID" envDefault:"chatgpt-mcp"`

	// OAuthDefaultUserID is the principal authorized after the operator enters
	// the pairing secret. A unique principal ID is preferred over a name.
	OAuthDefaultUserID string `env:"CHARON_OAUTH_DEFAULT_USER" envDefault:"chatgpt"`

	// OAuthRedirectURIs is the explicit allowlist for authorization-code redirects.
	// A trailing * allows a trusted path prefix, such as ChatGPT's per-app callback ID.
	OAuthRedirectURIs []string `env:"CHARON_OAUTH_REDIRECT_URIS"`

	// OAuthAllowAuthorScopes permits the configured OAuth authorization-code flow
	// to retain Memory Git branch/commit scopes granted to its principal. It never
	// permits review, merge, or direct-write scopes and is valid only in memory-git mode.
	OAuthAllowAuthorScopes bool `env:"CHARON_OAUTH_ALLOW_AUTHOR_SCOPES" envDefault:"false"`

	// OAuthPairingSecret authenticates the resource owner at the authorization
	// endpoint before Charon issues a code for the configured default principal.
	OAuthPairingSecret string `env:"CHARON_OAUTH_PAIRING_SECRET"`

	// OAuthGeneratePairingSecret forces a fresh high-entropy authorization key
	// at process startup. Container deployments enable this by default so the
	// active key is always the one emitted by the current process.
	OAuthGeneratePairingSecret bool `env:"CHARON_OAUTH_GENERATE_PAIRING_SECRET" envDefault:"false"`

	// OAuthPairingSecretGenerated reports whether the active authorization key
	// was generated for this process rather than loaded from configuration.
	OAuthPairingSecretGenerated bool

	// DataDir is the directory for SQLite and other persistent data.
	DataDir string `env:"CHARON_DATA_DIR" envDefault:"./data"`

	// LetheAPIKey is the bearer token for Lethe.
	LetheAPIKey string `env:"LETHE_API_KEY"`

	// HMACKey is the legacy shared-key fallback. New deployments should use
	// the purpose-specific keys below to reduce compromise blast radius.
	HMACKey string `env:"CHARON_HMAC_KEY"`

	// ObolHMACKey protects Charon bearer credential digests.
	ObolHMACKey string `env:"CHARON_OBOL_HMAC_KEY"`

	// OAuthHMACKey signs and verifies OAuth access tokens.
	OAuthHMACKey string `env:"CHARON_OAUTH_HMAC_KEY"`

	// MergeHMACKey authorizes protected-ref movement at the trusted Lethe
	// upstream. Lethe receives this value as its CHARON_HMAC_KEY.
	MergeHMACKey string `env:"CHARON_MERGE_HMAC_KEY"`

	// MergeHMACKeyID names which configured Lethe key signs merge envelopes.
	// Rotation keeps both IDs configured at Lethe during the overlap window.
	MergeHMACKeyID string `env:"CHARON_MERGE_HMAC_KEY_ID"`

	// MergeAuthorizationTTL bounds how long a signed merge authorization
	// lives. Short by design; values outside (0, 15m] are rejected so an
	// authorization can never be long-lived.
	MergeAuthorizationTTL time.Duration `env:"CHARON_MERGE_AUTH_TTL" envDefault:"2m"`

	// LedgerHMACKey signs audit ledger entries (purpose-specific HMAC).
	LedgerHMACKey string `env:"CHARON_LEDGER_HMAC_KEY"`

	// LedgerKeyID identifies the active ledger key for rotation.
	LedgerKeyID string `env:"CHARON_LEDGER_KEY_ID"`

	// LedgerAnchorPath is an append-only file outside the database that
	// anchors every committed ledger tail for rollback detection.
	LedgerAnchorPath string `env:"CHARON_LEDGER_ANCHOR_PATH"`

	// AllowInsecureUpstream permits plaintext HTTP to a non-loopback Lethe
	// endpoint. Development-only; production must use HTTPS, mTLS, a Unix
	// socket, or a loopback endpoint. Always logged when set.
	AllowInsecureUpstream bool `env:"CHARON_ALLOW_INSECURE_UPSTREAM" envDefault:"false"`

	// RecoveryReadOnly disables every mutation after a restore until
	// cross-database reconciliation succeeds (charon reconcile).
	RecoveryReadOnly bool `env:"CHARON_RECOVERY_READONLY" envDefault:"false"`

	// MaxResultBytes caps MCP tool response sizes.
	MaxResultBytes int `env:"CHARON_MAX_RESULT_BYTES" envDefault:"1048576"`

	// MaxResultCount caps MCP tool result item counts.
	MaxResultCount int `env:"CHARON_MAX_RESULT_COUNT" envDefault:"100"`

	// MaxRequestBytes caps inbound OAuth and MCP request bodies.
	MaxRequestBytes int64 `env:"CHARON_MAX_REQUEST_BYTES" envDefault:"1048576"`

	// RequestTimeout bounds upstream Lethe calls.
	RequestTimeout time.Duration `env:"CHARON_REQUEST_TIMEOUT" envDefault:"10s"`

	// HTTP server limits. WriteTimeout is intentionally disabled because MCP
	// stream responses may remain open; request contexts and upstream timeouts
	// bound the work performed for each tool call.
	HTTPReadHeaderTimeout time.Duration `env:"CHARON_HTTP_READ_HEADER_TIMEOUT" envDefault:"10s"`
	HTTPReadTimeout       time.Duration `env:"CHARON_HTTP_READ_TIMEOUT" envDefault:"30s"`
	HTTPIdleTimeout       time.Duration `env:"CHARON_HTTP_IDLE_TIMEOUT" envDefault:"2m"`
	HTTPShutdownTimeout   time.Duration `env:"CHARON_HTTP_SHUTDOWN_TIMEOUT" envDefault:"15s"`
	HTTPMaxHeaderBytes    int           `env:"CHARON_HTTP_MAX_HEADER_BYTES" envDefault:"1048576"`

	// AuthMode controls MCP endpoint authentication.
	// "obol" = require Obol token (default), "oauth" = OAuth 2.0, "none" = no auth (testing only).
	AuthMode string `env:"CHARON_AUTH_MODE" envDefault:"obol"`
}

// FromEnv loads configuration from environment variables.
// Fail-closed: missing required secrets prevent startup.
func FromEnv() (*Config, error) {
	oauthAllowAuthorScopes, err := getEnvBool("CHARON_OAUTH_ALLOW_AUTHOR_SCOPES", false)
	if err != nil {
		return nil, err
	}
	oauthGeneratePairingSecret, err := getEnvBool("CHARON_OAUTH_GENERATE_PAIRING_SECRET", false)
	if err != nil {
		return nil, err
	}
	maxResultBytes, err := getEnvInt("CHARON_MAX_RESULT_BYTES", 1048576, 1024, 16<<20)
	if err != nil {
		return nil, err
	}
	maxResultCount, err := getEnvInt("CHARON_MAX_RESULT_COUNT", 100, 1, 1000)
	if err != nil {
		return nil, err
	}
	maxRequestBytes, err := getEnvInt("CHARON_MAX_REQUEST_BYTES", 1048576, 1024, 4<<20)
	if err != nil {
		return nil, err
	}
	requestTimeout, err := getEnvDuration("CHARON_REQUEST_TIMEOUT", 10*time.Second, 100*time.Millisecond, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	mergeAuthTTL, err := getEnvDuration("CHARON_MERGE_AUTH_TTL", 2*time.Minute, time.Second, 15*time.Minute)
	if err != nil {
		return nil, err
	}
	allowInsecureUpstream, err := getEnvBool("CHARON_ALLOW_INSECURE_UPSTREAM", false)
	if err != nil {
		return nil, err
	}
	recoveryReadOnly, err := getEnvBool("CHARON_RECOVERY_READONLY", false)
	if err != nil {
		return nil, err
	}
	readHeaderTimeout, err := getEnvDuration("CHARON_HTTP_READ_HEADER_TIMEOUT", 10*time.Second, time.Second, time.Minute)
	if err != nil {
		return nil, err
	}
	readTimeout, err := getEnvDuration("CHARON_HTTP_READ_TIMEOUT", 30*time.Second, time.Second, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	idleTimeout, err := getEnvDuration("CHARON_HTTP_IDLE_TIMEOUT", 2*time.Minute, time.Second, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	shutdownTimeout, err := getEnvDuration("CHARON_HTTP_SHUTDOWN_TIMEOUT", 15*time.Second, time.Second, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	maxHeaderBytes, err := getEnvInt("CHARON_HTTP_MAX_HEADER_BYTES", 1048576, 8192, 1<<20)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Mode:                       Mode(getEnv("CHARON_MODE", string(ModeMemoryGit))),
		HTTP:                       getEnv("CHARON_HTTP", ":18484"),
		AdminHTTP:                  getEnv("CHARON_ADMIN_HTTP", "127.0.0.1:18486"),
		Upstream:                   getEnv("CHARON_UPSTREAM", "http://localhost:18485"),
		PublicURL:                  os.Getenv("CHARON_PUBLIC_URL"),
		OAuthClientID:              getEnv("CHARON_OAUTH_CLIENT_ID", "chatgpt-mcp"),
		OAuthDefaultUserID:         getEnv("CHARON_OAUTH_DEFAULT_USER", "chatgpt"),
		OAuthRedirectURIs:          splitCSV(os.Getenv("CHARON_OAUTH_REDIRECT_URIS")),
		OAuthAllowAuthorScopes:     oauthAllowAuthorScopes,
		OAuthPairingSecret:         os.Getenv("CHARON_OAUTH_PAIRING_SECRET"),
		OAuthGeneratePairingSecret: oauthGeneratePairingSecret,
		DataDir:                    getEnv("CHARON_DATA_DIR", "./data"),
		LetheAPIKey:                os.Getenv("LETHE_API_KEY"),
		HMACKey:                    os.Getenv("CHARON_HMAC_KEY"),
		ObolHMACKey:                firstNonEmpty(os.Getenv("CHARON_OBOL_HMAC_KEY"), os.Getenv("CHARON_HMAC_KEY")),
		OAuthHMACKey:               firstNonEmpty(os.Getenv("CHARON_OAUTH_HMAC_KEY"), os.Getenv("CHARON_HMAC_KEY")),
		MergeHMACKey:               firstNonEmpty(os.Getenv("CHARON_MERGE_HMAC_KEY"), os.Getenv("CHARON_HMAC_KEY")),
		MergeHMACKeyID:             os.Getenv("CHARON_MERGE_HMAC_KEY_ID"),
		MergeAuthorizationTTL:      mergeAuthTTL,
		LedgerHMACKey:              firstNonEmpty(os.Getenv("CHARON_LEDGER_HMAC_KEY"), os.Getenv("CHARON_HMAC_KEY")),
		LedgerKeyID:                os.Getenv("CHARON_LEDGER_KEY_ID"),
		LedgerAnchorPath:           os.Getenv("CHARON_LEDGER_ANCHOR_PATH"),
		AllowInsecureUpstream:      allowInsecureUpstream,
		RecoveryReadOnly:           recoveryReadOnly,
		MaxResultBytes:             maxResultBytes,
		MaxResultCount:             maxResultCount,
		MaxRequestBytes:            int64(maxRequestBytes),
		RequestTimeout:             requestTimeout,
		HTTPReadHeaderTimeout:      readHeaderTimeout,
		HTTPReadTimeout:            readTimeout,
		HTTPIdleTimeout:            idleTimeout,
		HTTPShutdownTimeout:        shutdownTimeout,
		HTTPMaxHeaderBytes:         maxHeaderBytes,
		AuthMode:                   getEnv("CHARON_AUTH_MODE", "obol"),
	}

	switch cfg.Mode {
	case ModeDirect, ModeMemoryGit:
	default:
		return nil, fmt.Errorf("unsupported CHARON_MODE: %q (must be direct or memory-git)", cfg.Mode)
	}

	if err := validateUpstreamTransport(cfg.Upstream, cfg.AllowInsecureUpstream); err != nil {
		return nil, fmt.Errorf("invalid CHARON_UPSTREAM: %w", err)
	}

	if cfg.LetheAPIKey == "" {
		return nil, errors.New("LETHE_API_KEY is required")
	}

	switch cfg.AuthMode {
	case "obol", "oauth", "none":
	default:
		return nil, fmt.Errorf("unsupported CHARON_AUTH_MODE: %q (must be obol, oauth, or none)", cfg.AuthMode)
	}
	if cfg.AuthMode == "obol" && len(cfg.ObolHMACKey) < 32 {
		return nil, errors.New("CHARON_OBOL_HMAC_KEY must be at least 32 characters (CHARON_HMAC_KEY remains a legacy fallback)")
	}
	if cfg.AuthMode == "oauth" && len(cfg.OAuthHMACKey) < 32 {
		return nil, errors.New("CHARON_OAUTH_HMAC_KEY must be at least 32 characters (CHARON_HMAC_KEY remains a legacy fallback)")
	}
	if cfg.Mode == ModeMemoryGit && len(cfg.MergeHMACKey) < 32 {
		return nil, errors.New("CHARON_MERGE_HMAC_KEY must be at least 32 characters in memory-git mode (CHARON_HMAC_KEY remains a legacy fallback)")
	}
	if cfg.AuthMode != "none" {
		if err := validateHTTPURL(cfg.PublicURL, true); err != nil {
			return nil, fmt.Errorf("invalid CHARON_PUBLIC_URL: %w", err)
		}
	}
	if cfg.AuthMode == "oauth" && len(cfg.OAuthRedirectURIs) == 0 {
		return nil, errors.New("CHARON_OAUTH_REDIRECT_URIS is required for OAuth mode")
	}
	if cfg.AuthMode == "oauth" && cfg.OAuthGeneratePairingSecret {
		cfg.OAuthPairingSecret, err = generateOAuthPairingSecret()
		if err != nil {
			return nil, err
		}
		cfg.OAuthPairingSecretGenerated = true
	}
	if cfg.AuthMode == "oauth" && len(cfg.OAuthPairingSecret) < 32 {
		return nil, errors.New("CHARON_OAUTH_PAIRING_SECRET must be at least 32 characters for OAuth mode")
	}
	if cfg.OAuthAllowAuthorScopes && cfg.Mode != ModeMemoryGit {
		return nil, errors.New("CHARON_OAUTH_ALLOW_AUTHOR_SCOPES requires CHARON_MODE=memory-git")
	}

	if err := validateAdminBind(cfg.AdminHTTP); err != nil {
		return nil, fmt.Errorf("invalid admin bind: %w", err)
	}
	if cfg.AuthMode == "none" {
		if err := validateLoopbackTCPBind(cfg.HTTP); err != nil {
			return nil, fmt.Errorf("CHARON_AUTH_MODE=none requires a loopback CHARON_HTTP bind: %w", err)
		}
	}

	return cfg, nil
}

func splitCSV(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func validateAdminBind(addr string) error {
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") {
		return nil
	}
	return validateLoopbackTCPBind(addr)
}

func validateLoopbackTCPBind(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("address must be host:port: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listener must use an explicit loopback host, got %q", host)
	}
	return nil
}

func validateHTTPURL(raw string, requireHTTPS bool) error {
	if raw == "" {
		return errors.New("URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return errors.New("must be an absolute URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.EscapedPath() != "" {
		return errors.New("userinfo, path, query, and fragment are not allowed")
	}
	if requireHTTPS && parsed.Scheme != "https" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return errors.New("https is required outside loopback development")
		}
	}
	return nil
}

// validateUpstreamTransport enforces the production transport policy for the
// Charon-to-Lethe channel: HTTPS (certificates and hostnames validated by the
// Go stack), a Unix socket for local IPC, or loopback HTTP for development.
// Plaintext HTTP to a non-loopback endpoint carries bearer credentials and
// merge authorizations in the clear and is rejected unless the explicit
// development-only override is set.
func validateUpstreamTransport(raw string, allowInsecure bool) error {
	if raw == "" {
		return errors.New("URL is required")
	}
	if socketPath, isUnix := strings.CutPrefix(raw, "unix://"); isUnix {
		if !strings.HasPrefix(socketPath, "/") {
			return errors.New("unix:// upstream requires an absolute socket path")
		}
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return errors.New("must be an absolute URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.EscapedPath() != "" {
		return errors.New("userinfo, path, query, and fragment are not allowed")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		if allowInsecure {
			return nil
		}
		return errors.New("plaintext HTTP to a non-loopback Lethe endpoint is not allowed in production; use HTTPS, a Unix socket, or loopback (CHARON_ALLOW_INSECURE_UPSTREAM=1 is a development-only override)")
	default:
		return errors.New("scheme must be http, https, or unix")
	}
}

func generateOAuthPairingSecret() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate OAuth authorization key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(key), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ObolKey returns the purpose-specific key or the legacy fallback for manually
// constructed Config values used by embedders and tests.
func (c *Config) ObolKey() string  { return firstNonEmpty(c.ObolHMACKey, c.HMACKey) }
func (c *Config) OAuthKey() string { return firstNonEmpty(c.OAuthHMACKey, c.HMACKey) }
func (c *Config) MergeKey() string { return firstNonEmpty(c.MergeHMACKey, c.HMACKey) }

// LedgerKey returns the purpose-specific ledger key or the legacy fallback.
func (c *Config) LedgerKey() string { return firstNonEmpty(c.LedgerHMACKey, c.HMACKey) }

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %q (must be a boolean)", key, value)
	}
	return parsed, nil
}

func getEnvInt(key string, fallback, minimum, maximum int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("invalid %s: %q (must be between %d and %d)", key, value, minimum, maximum)
	}
	return parsed, nil
}

func getEnvDuration(key string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("invalid %s: %q (must be between %s and %s)", key, value, minimum, maximum)
	}
	return parsed, nil
}

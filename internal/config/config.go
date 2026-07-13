package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// Mode determines how Charon operates.
type Mode string

const (
	ModeDirect Mode = "direct"
)

// Config holds all Charon configuration.
type Config struct {
	// Mode determines deployment mode. Only "direct" is supported initially.
	Mode Mode `env:"CHARON_MODE" envDefault:"direct"`

	// HTTP is the public MCP listener address.
	HTTP string `env:"CHARON_HTTP" envDefault:":18484"`

	// AdminHTTP is the admin listener address. Must be loopback or Unix socket.
	AdminHTTP string `env:"CHARON_ADMIN_HTTP" envDefault:"127.0.0.1:18486"`

	// Upstream is the Lethe API URL.
	Upstream string `env:"CHARON_UPSTREAM" envDefault:"http://localhost:18483"`

	// PublicURL is the external URL clients use to reach Charon.
	PublicURL string `env:"CHARON_PUBLIC_URL"`

	// OAuthClientID is the client_id advertised in OAuth protected resource metadata.
	OAuthClientID string `env:"CHARON_OAUTH_CLIENT_ID" envDefault:"chatgpt-mcp"`

	// DataDir is the directory for SQLite and other persistent data.
	DataDir string `env:"CHARON_DATA_DIR" envDefault:"./data"`

	// LetheAPIKey is the bearer token for Lethe.
	LetheAPIKey string `env:"LETHE_API_KEY"`

	// HMACKey is the server-held key for obol digests.
	HMACKey string `env:"CHARON_HMAC_KEY"`

	// MaxResultBytes caps MCP tool response sizes.
	MaxResultBytes int `env:"CHARON_MAX_RESULT_BYTES" envDefault:"1048576"`

	// MaxResultCount caps MCP tool result item counts.
	MaxResultCount int `env:"CHARON_MAX_RESULT_COUNT" envDefault:"100"`

	// RequestTimeout for upstream Lethe calls.
	RequestTimeout time.Duration `env:"CHARON_REQUEST_TIMEOUT" envDefault:"10s"`

	// AuthMode controls MCP endpoint authentication.
	// "obol" = require Obol token (default), "oauth" = OAuth 2.0, "none" = no auth (testing only).
	AuthMode string `env:"CHARON_AUTH_MODE" envDefault:"obol"`
}

// FromEnv loads configuration from environment variables.
// Fail-closed: missing required secrets prevent startup.
func FromEnv() (*Config, error) {
	cfg := &Config{
		Mode:           ModeDirect,
		HTTP:           getEnv("CHARON_HTTP", ":18484"),
		AdminHTTP:      getEnv("CHARON_ADMIN_HTTP", "127.0.0.1:18486"),
		Upstream:       getEnv("CHARON_UPSTREAM", "http://localhost:18483"),
		PublicURL:      os.Getenv("CHARON_PUBLIC_URL"),
		DataDir:        getEnv("CHARON_DATA_DIR", "./data"),
		LetheAPIKey:    os.Getenv("LETHE_API_KEY"),
		HMACKey:        os.Getenv("CHARON_HMAC_KEY"),
		MaxResultBytes: 1048576,
		MaxResultCount: 100,
		RequestTimeout: 10 * time.Second,
		AuthMode:       getEnv("CHARON_AUTH_MODE", "obol"),
	}

	if cfg.Mode != ModeDirect {
		return nil, fmt.Errorf("unsupported CHARON_MODE: %q", cfg.Mode)
	}

	if cfg.Upstream == "" {
		return nil, errors.New("CHARON_UPSTREAM is required")
	}

	if cfg.LetheAPIKey == "" {
		return nil, errors.New("LETHE_API_KEY is required")
	}

	if cfg.HMACKey == "" && cfg.AuthMode != "none" {
		return nil, errors.New("CHARON_HMAC_KEY is required (set CHARON_AUTH_MODE=none to skip for testing)")
	}

	if cfg.PublicURL == "" && cfg.AuthMode == "oauth" {
		return nil, errors.New("CHARON_PUBLIC_URL is required for OAuth mode")
	}

	if err := validateAdminBind(cfg.AdminHTTP); err != nil {
		return nil, fmt.Errorf("invalid admin bind: %w", err)
	}

	return cfg, nil
}

func validateAdminBind(addr string) error {
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") {
		// Unix socket
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("address must be host:port or unix socket path: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("admin host must be an IP, got %q", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("admin listener must be loopback, got %q", host)
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

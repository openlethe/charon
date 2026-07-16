package main

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/openlethe/charon/internal/config"
)

func TestVersionInfoIncludesBuildIdentity(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := version, commit, buildDate
	t.Cleanup(func() { version, commit, buildDate = oldVersion, oldCommit, oldBuildDate })
	version, commit, buildDate = "v1.2.3", "abc123", "2026-07-15T12:00:00Z"
	got := versionInfo()
	for _, value := range []string{"v1.2.3", "abc123", "2026-07-15T12:00:00Z", "go1."} {
		if !strings.Contains(got, value) {
			t.Fatalf("version output %q missing %q", got, value)
		}
	}
}

func TestOAuthAuthorizationKeyBannerShowsOnlyGeneratedKey(t *testing.T) {
	generated := &config.Config{
		AuthMode:                    "oauth",
		OAuthClientID:               "chatgpt-mcp",
		PublicURL:                   "https://charon.example.test",
		OAuthPairingSecret:          "generated-authorization-key-value",
		OAuthPairingSecretGenerated: true,
	}

	var banner bytes.Buffer
	writeOAuthAuthorizationKeyBanner(&banner, generated)
	shown := banner.String()
	for _, required := range []string{
		"CHARON OAUTH AUTHORIZATION KEY",
		"generated-authorization-key-value",
		"Paste this key into the browser authorization page",
	} {
		if !strings.Contains(shown, required) {
			t.Fatalf("authorization key banner missing %q: %s", required, shown)
		}
	}
	if strings.Count(shown, generated.OAuthPairingSecret) != 1 {
		t.Fatalf("generated key should appear exactly once in banner: %s", shown)
	}

	configured := *generated
	configured.OAuthPairingSecret = "configured-secret-must-not-be-logged"
	configured.OAuthPairingSecretGenerated = false
	banner.Reset()
	writeOAuthAuthorizationKeyBanner(&banner, &configured)
	if banner.Len() != 0 {
		t.Fatalf("configured authorization key must not be printed: %s", banner.String())
	}
}

func TestLogOAuthConfigurationDoesNotRepeatAuthorizationKey(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	generated := &config.Config{
		AuthMode:                    "oauth",
		OAuthClientID:               "chatgpt-mcp",
		PublicURL:                   "https://charon.example.test",
		OAuthPairingSecret:          "generated-authorization-key-value",
		OAuthPairingSecretGenerated: true,
	}
	logOAuthConfiguration(logger, generated)
	logged := output.String()
	if strings.Contains(logged, generated.OAuthPairingSecret) || !strings.Contains(logged, "authorization key generated") {
		t.Fatalf("generated authorization key logging was unsafe or unclear: %s", logged)
	}

	output.Reset()
	configured := *generated
	configured.OAuthPairingSecret = "configured-secret-must-not-be-logged"
	configured.OAuthPairingSecretGenerated = false
	logOAuthConfiguration(logger, &configured)
	logged = output.String()
	if strings.Contains(logged, configured.OAuthPairingSecret) || !strings.Contains(logged, "value not logged") {
		t.Fatalf("configured authorization key logging was unsafe or unclear: %s", logged)
	}
}

func TestOptionalLimit(t *testing.T) {
	if got, err := optionalLimit([]string{"charon", "ledger", "tail"}, 3, 20); err != nil || got != 20 {
		t.Fatalf("default limit = %d, %v", got, err)
	}
	if got, err := optionalLimit([]string{"charon", "ledger", "tail", "50"}, 3, 20); err != nil || got != 50 {
		t.Fatalf("parsed limit = %d, %v", got, err)
	}
	for _, value := range []string{"0", "101", "invalid"} {
		if _, err := optionalLimit([]string{"charon", "ledger", "tail", value}, 3, 20); err == nil {
			t.Fatalf("expected invalid limit error for %q", value)
		}
	}
}

func TestScopesForPermission(t *testing.T) {
	got, err := scopesForPermission("readonly")
	if err != nil {
		t.Fatalf("readonly: %v", err)
	}
	want := []string{"memory.search", "memory.read", "thread.read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readonly scopes = %v, want %v", got, want)
	}
	propose, err := scopesForPermission("propose")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	for _, scope := range []string{"memory.branch", "memory.commit", "memory.propose"} {
		if !contains(propose, scope) {
			t.Errorf("propose scopes missing %q: %v", scope, propose)
		}
	}
	review, err := scopesForPermission("review")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	for _, scope := range []string{"memory.review", "memory.merge", "proposal.review"} {
		if !contains(review, scope) {
			t.Errorf("review scopes missing %q: %v", scope, review)
		}
	}
	for _, forbidden := range []string{"memory.branch", "memory.commit", "memory.propose", "memory.write"} {
		if contains(review, forbidden) {
			t.Errorf("review scopes unexpectedly include %q: %v", forbidden, review)
		}
	}

	write, err := scopesForPermission("write")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, scope := range []string{"memory.review", "memory.merge", "memory.write", "proposal.review"} {
		if !contains(write, scope) {
			t.Errorf("write scopes missing %q: %v", scope, write)
		}
	}
	if _, err := scopesForPermission("admin"); err == nil {
		t.Fatal("expected invalid permission error")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSplitNonEmpty(t *testing.T) {
	got := splitNonEmpty(" default,lethe,default, * ")
	want := []string{"default", "lethe", "*"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projects = %v, want %v", got, want)
	}
}

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitScriptAllowsUnsetOptionalVariables(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join(repoRoot, "init.sh"))
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"CHARON_BIN=" + truePath,
		"CHARON_AUTH_MODE=none",
		"CHARON_MODE=direct",
		"CHARON_DATA_DIR=" + t.TempDir(),
		"LETHE_API_KEY=test-value",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init script failed with optional variables unset: %v\n%s", err, output)
	}
}

func TestInitScriptKeepsOAuthStartupOutputForServer(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fakeCharon := filepath.Join(t.TempDir(), "charon")
	fakeProgram := `#!/bin/sh
set -eu
case "$1:${2:-}" in
  principal:reconcile)
    printf 'Reconciled principal: principal-id (updated)\n'
    ;;
  serve:)
    printf 'SERVER-START\n'
    ;;
  *)
    exit 1
    ;;
esac
`
	if err := os.WriteFile(fakeCharon, []byte(fakeProgram), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", filepath.Join(repoRoot, "init.sh"))
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"CHARON_BIN=" + fakeCharon,
		"CHARON_AUTH_MODE=oauth",
		"CHARON_MODE=direct",
		"CHARON_DATA_DIR=" + t.TempDir(),
		"CHARON_AUTO_USER=ChatGPT",
		"CHARON_OAUTH_DEFAULT_USER=principal-id",
		"CHARON_OAUTH_HMAC_KEY=" + strings.Repeat("h", 32),
		"CHARON_OAUTH_GENERATE_PAIRING_SECRET=true",
		"LETHE_API_KEY=test-value",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("OAuth init script failed: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "SERVER-START" {
		t.Fatalf("entrypoint wrote output before the OAuth server banner: %q", got)
	}
}

func TestInitScriptRemintsBootstrapTokenWhenPrincipalChanges(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	fakeCharon := filepath.Join(t.TempDir(), "charon")
	fakeProgram := `#!/bin/sh
set -eu
case "$1:${2:-}" in
  principal:reconcile)
    printf 'Reconciled principal: %s (updated)\n' "$FAKE_PRINCIPAL_ID"
    ;;
  obol:mint)
    printf 'Obol minted:\n  ID: test-id\n  Token: obol_%s_test\n  Exp: 2030-01-01T00:00:00Z\n' "$FAKE_PRINCIPAL_ID"
    ;;
  serve:)
    ;;
  *)
    printf 'unexpected fake charon invocation: %s %s\n' "$1" "${2:-}" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(fakeCharon, []byte(fakeProgram), 0o700); err != nil {
		t.Fatal(err)
	}

	run := func(principalID string) {
		t.Helper()
		cmd := exec.Command("sh", filepath.Join(repoRoot, "init.sh"))
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"CHARON_BIN=" + fakeCharon,
			"CHARON_AUTH_MODE=obol",
			"CHARON_MODE=direct",
			"CHARON_DATA_DIR=" + dataDir,
			"CHARON_AUTO_USER=Bootstrap User",
			"CHARON_OBOL_HMAC_KEY=" + strings.Repeat("o", 32),
			"LETHE_API_KEY=test-value",
			"FAKE_PRINCIPAL_ID=" + principalID,
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("init script failed for %s: %v\n%s", principalID, err, output)
		}
	}

	run("principal_one")
	run("principal_two")

	marker, err := os.ReadFile(filepath.Join(dataDir, ".bootstrap-token-minted"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(marker)) != "principal_two" {
		t.Fatalf("bootstrap marker = %q, want principal_two", marker)
	}
	token, err := os.ReadFile(filepath.Join(dataDir, "bootstrap-obol.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(token), "principal_two") || strings.Contains(string(token), "principal_one") {
		t.Fatalf("bootstrap token was not replaced for the reconciled principal: %s", token)
	}
}

func TestRepositoryOperationalScriptContracts(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(repoRoot, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	rotation := read("scripts/rotate-tokens.sh")
	for _, required := range []string{
		"docker compose run --rm --no-deps --entrypoint charon charon-reviewer",
		`awk '$3 == "active" {print $1}'`,
		`"${reviewer_cli[@]}" obol mint`,
	} {
		if !strings.Contains(rotation, required) {
			t.Fatalf("rotation script missing %q", required)
		}
	}
	if strings.Contains(rotation, "/bin/charon") {
		t.Fatal("rotation script must not target the host database through bin/charon")
	}
	if strings.Contains(rotation, `"${charon_cli[@]}" obol mint`) {
		t.Fatal("rotation script must mint every role Obol with the reviewer (Obol) gateway audience")
	}

	start := read("start.sh")
	for _, required := range []string{
		`chmod 600 "$lethe_header_file"`,
		`--header "@${lethe_header_file}"`,
		"trap cleanup EXIT",
	} {
		if !strings.Contains(start, required) {
			t.Fatalf("start script missing protected health-header behavior %q", required)
		}
	}
	if strings.Contains(start, `-H "Authorization: Bearer ${LETHE_API_KEY}"`) {
		t.Fatal("start script exposes the Lethe bearer credential in curl arguments")
	}

	compose := read("docker-compose.yml")
	if !strings.Contains(compose, `CHARON_HMAC_KEY: ${CHARON_HMAC_KEY:-}`) {
		t.Fatal("primary Compose profile dropped the legacy HMAC migration fallback")
	}
	setup := read("setup.sh")
	if !strings.Contains(setup, "docker compose run --rm --no-deps --entrypoint charon charon-reviewer") ||
		!strings.Contains(setup, `"${reviewer_cli[@]}" obol mint`) ||
		!strings.Contains(setup, `auth_mode=${CHARON_AUTH_MODE:-oauth}`) ||
		!strings.Contains(setup, `bootstrap_default_user=${CHARON_OAUTH_DEFAULT_USER:-principal_bootstrap_placeholder}`) {
		t.Fatal("setup script must align OAuth defaults and mint all role Obols with the reviewer (Obol) gateway audience")
	}

	launcherPath := filepath.Join(repoRoot, "bin", "charon")
	launcher, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(launcher), "#!/bin/sh\n") || !strings.Contains(string(launcher), `go run "$repo_root/cmd/charon"`) {
		t.Fatalf("bin/charon is not the expected source launcher: %q", launcher)
	}
	info, err := os.Stat(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("bin/charon source launcher is not executable")
	}
}

#!/usr/bin/env bash
set -euo pipefail

# Local CLI and persistence smoke test. This script never prints the minted
# credential and does not require a running Lethe service.

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

export CHARON_MODE=direct
export CHARON_AUTH_MODE=obol
export CHARON_DATA_DIR="$tmp_dir/data"
export CHARON_UPSTREAM=http://127.0.0.1:18483
export CHARON_PUBLIC_URL=http://127.0.0.1:18484
export LETHE_API_KEY=test-only-upstream-key
export CHARON_OBOL_HMAC_KEY
CHARON_OBOL_HMAC_KEY=$(openssl rand -hex 32)

binary="$tmp_dir/charon"
token_file="$tmp_dir/e2e-obol.txt"

echo "Building Charon smoke-test binary..."
go build -trimpath -o "$binary" ./cmd/charon

echo "Reconciling read-only principal..."
principal=$($binary principal reconcile "E2E Test" readonly charon | awk '/^Reconciled principal:/ {print $3}')
test -n "$principal"

"$binary" obol mint --expires 5m "$principal" > "$token_file"
chmod 600 "$token_file"
grep -q '^  Token: obol_' "$token_file"

"$binary" ledger verify >/dev/null

echo "Charon local CLI smoke test passed. Temporary credentials were removed."

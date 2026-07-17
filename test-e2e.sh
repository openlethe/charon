#!/usr/bin/env bash
set -euo pipefail

# Local CLI and persistence smoke test covering all three deployment roles.
# For each role (author/propose, reviewer/review, reader/readonly) it
# reconciles the principal, verifies the exact scope set (required scopes
# present, privileged scopes absent), mints an Obol, and confirms it is
# active. This script never prints credential values and does not require a
# running Lethe service.

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

command -v sqlite3 >/dev/null || { echo "ERROR: sqlite3 is required."; exit 1; }

binary="$tmp_dir/charon"
db_path="$CHARON_DATA_DIR/charon.db"

echo "Building Charon smoke-test binary..."
go build -trimpath -o "$binary" ./cmd/charon

has_scope() {
    local principal_id=$1 scope=$2
    [[ $(sqlite3 "$db_path" "SELECT COUNT(*) FROM principal_scopes WHERE principal_id = '$principal_id' AND scope = '$scope';") == 1 ]]
}

check_role() {
    local name=$1 profile=$2 required_scopes=$3 forbidden_scopes=$4
    local principal_id scope active_count

    principal_id=$("$binary" principal reconcile "$name" "$profile" e2e | awk '/^Reconciled principal:/ {print $3}')
    test -n "$principal_id"

    for scope in $required_scopes; do
        has_scope "$principal_id" "$scope" || {
            echo "ERROR: $profile principal $principal_id is missing required scope $scope"
            exit 1
        }
    done
    for scope in $forbidden_scopes; do
        if has_scope "$principal_id" "$scope"; then
            echo "ERROR: $profile principal $principal_id unexpectedly holds privileged scope $scope"
            exit 1
        fi
    done

    "$binary" obol mint --expires 5m "$principal_id" > "$tmp_dir/$profile-obol.txt"
    chmod 600 "$tmp_dir/$profile-obol.txt"
    grep -q '^  Token: obol_' "$tmp_dir/$profile-obol.txt"

    active_count=$("$binary" obol ls "$principal_id" | awk '$3 == "active"' | wc -l | tr -d ' ')
    [[ $active_count -ge 1 ]] || {
        echo "ERROR: $profile principal $principal_id has no active Obol after minting"
        exit 1
    }

    echo "$profile principal $principal_id: scope set verified, active Obol minted."
}

check_role "E2E Author" propose \
    "memory.search memory.read thread.read memory.branch memory.commit memory.propose" \
    "memory.review memory.merge memory.write proposal.review"

check_role "E2E Reviewer" review \
    "memory.search memory.read thread.read memory.review memory.merge proposal.review" \
    "memory.branch memory.commit memory.propose memory.write"

check_role "E2E Reader" readonly \
    "memory.search memory.read thread.read" \
    "memory.branch memory.commit memory.propose memory.review memory.merge memory.write proposal.review"

"$binary" ledger verify >/dev/null

echo "Charon three-role CLI smoke test passed. Temporary credentials were removed."

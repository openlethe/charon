#!/usr/bin/env bash
set -euo pipefail
umask 077

# Bootstrap the hardened single-host Charon container and create separate
# author, reviewer, and reader principals. Credential values are written only
# to mode-0600 files and are never printed.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
env_file="${repo_root}/.env"
secrets_dir="${repo_root}/secrets"
project=${CHARON_PROJECT:-default}
merge_key_input=${CHARON_MERGE_HMAC_KEY:-}

command -v docker >/dev/null || { echo "ERROR: Docker is required."; exit 1; }
docker compose version >/dev/null || { echo "ERROR: Docker Compose v2 is required."; exit 1; }
command -v openssl >/dev/null || { echo "ERROR: openssl is required."; exit 1; }

mkdir -p "$secrets_dir"
chmod 700 "$secrets_dir"

if [[ ! -f "$env_file" ]]; then
    [[ -n ${LETHE_API_KEY:-} ]] || {
        echo "ERROR: export the existing upstream LETHE_API_KEY before running setup.sh."
        exit 1
    }
    [[ ${#merge_key_input} -ge 32 ]] || {
        echo "ERROR: export CHARON_MERGE_HMAC_KEY after configuring the same value on Lethe (CHARON_MERGE_HMAC_KEY there; legacy CHARON_HMAC_KEY is still accepted)."
        exit 1
    }

    obol_key=${CHARON_OBOL_HMAC_KEY:-$(openssl rand -hex 32)}
    oauth_key=${CHARON_OAUTH_HMAC_KEY:-$(openssl rand -hex 32)}
    pairing_secret=${CHARON_OAUTH_PAIRING_SECRET:-$(openssl rand -hex 32)}
    public_url=${CHARON_PUBLIC_URL:-http://127.0.0.1:18484}
    auth_mode=${CHARON_AUTH_MODE:-oauth}
    upstream=${CHARON_UPSTREAM:-http://host.docker.internal:18485}
    if [[ "$auth_mode" == oauth && -z ${CHARON_OAUTH_REDIRECT_URIS:-} ]]; then
        echo "ERROR: export CHARON_OAUTH_REDIRECT_URIS before bootstrapping OAuth mode."
        exit 1
    fi

    cat > "$env_file" <<EOF
LETHE_API_KEY=${LETHE_API_KEY}
CHARON_UPSTREAM=${upstream}
CHARON_MODE=memory-git
CHARON_PUBLIC_URL=${public_url}
CHARON_AUTH_MODE=${auth_mode}
CHARON_OBOL_HMAC_KEY=${obol_key}
CHARON_OAUTH_HMAC_KEY=${oauth_key}
CHARON_MERGE_HMAC_KEY=${merge_key_input}
CHARON_OAUTH_CLIENT_ID=${CHARON_OAUTH_CLIENT_ID:-chatgpt-mcp}
CHARON_OAUTH_DEFAULT_USER=${CHARON_OAUTH_DEFAULT_USER:-}
CHARON_OAUTH_REDIRECT_URIS=${CHARON_OAUTH_REDIRECT_URIS:-}
CHARON_OAUTH_PAIRING_SECRET=${pairing_secret}
CHARON_OAUTH_ALLOW_AUTHOR_SCOPES=${CHARON_OAUTH_ALLOW_AUTHOR_SCOPES:-false}
EOF
    chmod 600 "$env_file"
    echo "Created protected .env configuration. No secret values were printed."
else
    chmod 600 "$env_file"
    echo "Using existing protected .env configuration."
fi

set -a
# shellcheck disable=SC1090
source "$env_file"
set +a

cd "$repo_root"
# Compose requires the served OAuth principal during interpolation. Bootstrap
# commands run before that principal exists, so give only these one-off CLI
# containers a non-persisted placeholder and write the reconciled ID below
# before start.sh launches either gateway.
bootstrap_default_user=${CHARON_OAUTH_DEFAULT_USER:-principal_bootstrap_placeholder}
CHARON_OAUTH_DEFAULT_USER="$bootstrap_default_user" docker compose build charon
charon_cli=(env CHARON_OAUTH_DEFAULT_USER="$bootstrap_default_user" docker compose run --rm --no-deps --entrypoint charon charon)
reviewer_cli=(env CHARON_OAUTH_DEFAULT_USER="$bootstrap_default_user" docker compose run --rm --no-deps --entrypoint charon charon-reviewer)

author_id=$("${charon_cli[@]}" principal reconcile "Local Memory Author" propose "$project" | awk '/^Reconciled principal:/ {print $3}')
reviewer_id=$("${charon_cli[@]}" principal reconcile "Local Memory Reviewer" review "$project" | awk '/^Reconciled principal:/ {print $3}')
reader_id=$("${charon_cli[@]}" principal reconcile "Local Memory Reader" readonly "$project" | awk '/^Reconciled principal:/ {print $3}')

[[ -n "$author_id" && -n "$reviewer_id" && -n "$reader_id" ]] || {
    echo "ERROR: principal reconciliation did not return all IDs."
    exit 1
}

set_env_value() {
    local key=$1
    local value=$2
    local temporary
    temporary=$(mktemp)
    awk -v key="$key" -v value="$value" '
        BEGIN { replaced = 0 }
        index($0, key "=") == 1 {
            if (!replaced) {
                print key "=" value
                replaced = 1
            }
            next
        }
        { print }
        END {
            if (!replaced) print key "=" value
        }
    ' "$env_file" > "$temporary"
    chmod 600 "$temporary"
    mv "$temporary" "$env_file"
}

if [[ -z ${CHARON_OAUTH_DEFAULT_USER:-} ]]; then
    set_env_value CHARON_OAUTH_DEFAULT_USER "$author_id"
fi

mint_to_file() {
    local principal_id=$1
    local destination=$2
    local gateway=${3:-primary}
    if [[ "$gateway" == reviewer ]]; then
        "${reviewer_cli[@]}" obol mint --expires 7d "$principal_id" > "$destination"
    else
        "${charon_cli[@]}" obol mint --expires 7d "$principal_id" > "$destination"
    fi
    chmod 600 "$destination"
}

mint_to_file "$author_id" "${secrets_dir}/author-obol.txt"
mint_to_file "$reviewer_id" "${secrets_dir}/reviewer-obol.txt" reviewer
mint_to_file "$reader_id" "${secrets_dir}/reader-obol.txt"

cat > "${secrets_dir}/principal-ids.txt" <<EOF
AUTHOR_ID=${author_id}
REVIEWER_ID=${reviewer_id}
READER_ID=${reader_id}
EOF
chmod 600 "${secrets_dir}/principal-ids.txt"

"${repo_root}/start.sh"

cat <<EOF
Charon bootstrap completed for project ${project}.
Credentials were stored under secrets/ with mode 0600:
  - author-obol.txt
  - reviewer-obol.txt
  - reader-obol.txt
No credential values were printed.
EOF

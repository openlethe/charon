#!/usr/bin/env bash
set -euo pipefail

# Start the hardened single-host Compose profile using externally provisioned
# credentials. This script never creates or prints secret values.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
env_file="${repo_root}/.env"

if [[ ! -f "$env_file" ]]; then
    echo "ERROR: ${env_file} is missing. Copy .env.example and provision the required values."
    exit 1
fi
chmod 600 "$env_file"

set -a
# shellcheck disable=SC1090
source "$env_file"
set +a

charon_mode=${CHARON_MODE:-memory-git}
obol_key=${CHARON_OBOL_HMAC_KEY:-${CHARON_HMAC_KEY:-}}
oauth_key=${CHARON_OAUTH_HMAC_KEY:-${CHARON_HMAC_KEY:-}}
merge_key=${CHARON_MERGE_HMAC_KEY:-${CHARON_HMAC_KEY:-}}
pairing_secret=${CHARON_OAUTH_PAIRING_SECRET:-}
generate_pairing_secret=${CHARON_OAUTH_GENERATE_PAIRING_SECRET:-true}

[[ -n ${LETHE_API_KEY:-} ]] || { echo "ERROR: LETHE_API_KEY is required."; exit 1; }
[[ "$LETHE_API_KEY" != *$'\n'* && "$LETHE_API_KEY" != *$'\r'* ]] || {
    echo "ERROR: LETHE_API_KEY must not contain control characters."
    exit 1
}
[[ -n ${CHARON_PUBLIC_URL:-} ]] || { echo "ERROR: CHARON_PUBLIC_URL is required."; exit 1; }

[[ ${#obol_key} -ge 32 ]] || { echo "ERROR: provision CHARON_OBOL_HMAC_KEY for the reviewer gateway."; exit 1; }
[[ ${#oauth_key} -ge 32 ]] || { echo "ERROR: provision CHARON_OAUTH_HMAC_KEY for the main gateway."; exit 1; }
[[ -n ${CHARON_OAUTH_DEFAULT_USER:-} ]] || { echo "ERROR: CHARON_OAUTH_DEFAULT_USER is required."; exit 1; }
[[ -n ${CHARON_OAUTH_REDIRECT_URIS:-} ]] || { echo "ERROR: CHARON_OAUTH_REDIRECT_URIS is required."; exit 1; }
case "$generate_pairing_secret" in
    1|[Tt]|[Tt][Rr][Uu][Ee]) ;;
    0|[Ff]|[Ff][Aa][Ll][Ss][Ee])
        [[ ${#pairing_secret} -ge 32 ]] || { echo "ERROR: provision CHARON_OAUTH_PAIRING_SECRET when generated pairing keys are disabled."; exit 1; }
        ;;
    *)
        echo "ERROR: CHARON_OAUTH_GENERATE_PAIRING_SECRET must be true or false."
        exit 1
        ;;
esac

if [[ "$charon_mode" == memory-git && ${#merge_key} -lt 32 ]]; then
    echo "ERROR: provision CHARON_MERGE_HMAC_KEY and configure the same value on Lethe as CHARON_HMAC_KEY."
    exit 1
fi

check_http_200() {
    local status
    status=$(curl --max-time 5 --silent --show-error --output /dev/null --write-out '%{http_code}' "$@") || return 1
    [[ "$status" == 200 ]]
}

upstream=${CHARON_UPSTREAM:-http://host.docker.internal:18485}
host_upstream=${upstream/host.docker.internal/127.0.0.1}
lethe_health=${LETHE_HEALTH_URL:-${host_upstream%/}/api/health}
lethe_header_file=$(mktemp)
cleanup() {
    rm -f "$lethe_header_file"
}
trap cleanup EXIT
chmod 600 "$lethe_header_file"
printf 'Authorization: Bearer %s\n' "$LETHE_API_KEY" > "$lethe_header_file"
if ! check_http_200 --header "@${lethe_header_file}" "$lethe_health"; then
    echo "ERROR: the configured Lethe upstream is not healthy."
    exit 1
fi

cd "$repo_root"
docker compose up -d --build

for _ in {1..30}; do
    if check_http_200 http://127.0.0.1:18484/readyz; then
        echo "Charon is healthy at http://127.0.0.1:18484."
        echo "OAuth metadata: ${CHARON_PUBLIC_URL%/}/.well-known/oauth-protected-resource"
        exit 0
    fi
    sleep 1
done

echo "ERROR: Charon did not become healthy."
docker compose ps
exit 1

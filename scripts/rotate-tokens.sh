#!/usr/bin/env bash
set -euo pipefail
umask 077

# Rotate locally stored Obols without printing credential values.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
secrets_dir="${repo_root}/secrets"
principal_file="${secrets_dir}/principal-ids.txt"
env_file="${repo_root}/.env"

if [[ ! -f "$principal_file" || ! -f "$env_file" ]]; then
    echo "ERROR: run setup.sh before rotating credentials."
    exit 1
fi

set -a
# shellcheck disable=SC1090
source "$principal_file"
# shellcheck disable=SC1090
source "$env_file"
set +a

command -v docker >/dev/null || { echo "ERROR: Docker is required."; exit 1; }
docker compose version >/dev/null || { echo "ERROR: Docker Compose v2 is required."; exit 1; }
cd "$repo_root"
charon_cli=(docker compose run --rm --no-deps --entrypoint charon charon)
reviewer_cli=(docker compose run --rm --no-deps --entrypoint charon charon-reviewer)

rotate_token() {
    local principal_id=$1
    local name=$2
    local destination=$3
    local gateway=${4:-primary}
    local -a cli

    if [[ "$gateway" == reviewer ]]; then
        cli=("${reviewer_cli[@]}")
    else
        cli=("${charon_cli[@]}")
    fi

    while read -r obol_id; do
        [[ -n "$obol_id" ]] || continue
        "${cli[@]}" obol revoke "$obol_id" >/dev/null 2>&1 || true
    done < <("${cli[@]}" obol ls "$principal_id" | awk '$3 == "active" {print $1}')

    "${cli[@]}" obol mint --expires 7d "$principal_id" > "$destination"
    chmod 600 "$destination"
    echo "Rotated $name credential and stored it at ${destination#"$repo_root/"}."
}

rotate_token "$AUTHOR_ID" "author" "${secrets_dir}/author-obol.txt"
rotate_token "$REVIEWER_ID" "reviewer" "${secrets_dir}/reviewer-obol.txt" reviewer
rotate_token "$READER_ID" "reader" "${secrets_dir}/reader-obol.txt"

echo "Credential rotation complete. Update clients from the protected files; no token values were printed."

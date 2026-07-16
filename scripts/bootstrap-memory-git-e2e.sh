#!/bin/sh
set -eu

container=${CHARON_MEMORY_GIT_CONTAINER:-charon-memory-git-test}
project=${1:-lethe-git-e2e}
out_dir=${2:-./charon-memory-git-data/e2e-secrets}

if ! docker inspect "$container" >/dev/null 2>&1; then
  printf 'container %s is not available\n' "$container" >&2
  exit 1
fi

umask 077
mkdir -p "$out_dir"
chmod 700 "$out_dir"

author_output=$(docker exec "$container" charon principal reconcile "Memory Git E2E Author" propose "$project")
reviewer_output=$(docker exec "$container" charon principal reconcile "Memory Git E2E Reviewer" review "$project")

author_id=$(printf '%s\n' "$author_output" | awk '/Reconciled principal:/ {print $3; exit}')
reviewer_id=$(printf '%s\n' "$reviewer_output" | awk '/Reconciled principal:/ {print $3; exit}')

if [ -z "$author_id" ] || [ -z "$reviewer_id" ] || [ "$author_id" = "$reviewer_id" ]; then
  printf 'failed to create two distinct principals\n' >&2
  exit 1
fi

author_obol=$(docker exec "$container" charon obol mint "$author_id" | awk '/Token:/ {print $2; exit}')
reviewer_obol=$(docker exec "$container" charon obol mint "$reviewer_id" | awk '/Token:/ {print $2; exit}')

if [ -z "$author_obol" ] || [ -z "$reviewer_obol" ]; then
  printf 'failed to mint trusted Obols\n' >&2
  exit 1
fi

printf '%s\n' "$author_id" > "$out_dir/author-principal.txt"
printf '%s\n' "$reviewer_id" > "$out_dir/reviewer-principal.txt"
printf '%s\n' "$author_obol" > "$out_dir/author.obol"
printf '%s\n' "$reviewer_obol" > "$out_dir/reviewer.obol"
chmod 600 "$out_dir"/*

printf 'created distinct exact-project E2E principals for %s\n' "$project"
printf 'principal IDs and Obols stored under %s with restrictive permissions; token values were not printed\n' "$out_dir"
printf 'author scopes are read/search/branch/commit/propose; reviewer scopes are read/search/review/merge with no authoring authority\n'

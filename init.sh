#!/bin/sh
set -eu
umask 077

# Charon container entrypoint
# Validates externally provisioned secrets, reconciles an optional principal,
# and delegates to the long-running server without printing credential values.

CHARON_BIN="${CHARON_BIN:-/usr/local/bin/charon}"
DATA_DIR="${CHARON_DATA_DIR:-/data}"
AUTH_MODE="${CHARON_AUTH_MODE:-obol}"
CHARON_MODE_VALUE="${CHARON_MODE:-memory-git}"

# Secrets must be provisioned explicitly. The application supports
# CHARON_HMAC_KEY only as a legacy fallback for the purpose-specific keys.
if [ "$AUTH_MODE" = "obol" ] && [ -z "${CHARON_OBOL_HMAC_KEY:-${CHARON_HMAC_KEY:-}}" ]; then
    echo "ERROR: CHARON_OBOL_HMAC_KEY is required for obol authentication."
    exit 1
fi
if [ "$AUTH_MODE" = "oauth" ]; then
    if [ -z "${CHARON_OAUTH_HMAC_KEY:-${CHARON_HMAC_KEY:-}}" ]; then
        echo "ERROR: CHARON_OAUTH_HMAC_KEY is required for OAuth authentication."
        exit 1
    fi
    if [ "${CHARON_OAUTH_GENERATE_PAIRING_SECRET:-false}" != "true" ] && [ -z "${CHARON_OAUTH_PAIRING_SECRET:-}" ]; then
        echo "ERROR: CHARON_OAUTH_PAIRING_SECRET is required when generated OAuth pairing is disabled."
        exit 1
    fi
fi
if [ "$CHARON_MODE_VALUE" = "memory-git" ] && [ -z "${CHARON_MERGE_HMAC_KEY:-${CHARON_HMAC_KEY:-}}" ]; then
    echo "ERROR: CHARON_MERGE_HMAC_KEY is required for protected Memory Git merges."
    exit 1
fi

# Validate Lethe key is set
if [ -z "${LETHE_API_KEY:-}" ]; then
    echo "ERROR: LETHE_API_KEY is required."
    echo ""
    echo "Generate one with: openssl rand -hex 32"
    echo "Then add it to your .env file:"
    echo "  LETHE_API_KEY=your-generated-key"
    echo ""
    exit 1
fi

# Reconcile the configured principal on every start. Bootstrap credentials are
# written only to the protected data volume and are never emitted to logs.
TOKEN_MARKER="${DATA_DIR}/.bootstrap-token-minted"
BOOTSTRAP_TOKEN_FILE="${DATA_DIR}/bootstrap-obol.txt"
BOOTSTRAP_PRINCIPAL_FILE="${DATA_DIR}/.bootstrap-principal"

if [ -n "${CHARON_AUTO_USER:-}" ]; then
    if [ "$AUTH_MODE" != "oauth" ]; then
        echo ""
        echo "╔══════════════════════════════════════════════════════════════╗"
        echo "║  Charon Principal Reconciliation                            ║"
        echo "╚══════════════════════════════════════════════════════════════╝"
        echo ""
    fi

    PROJECTS="${CHARON_AUTO_PROJECTS:-${CHARON_AUTO_PROJECT:-default}}"
    PERMISSION="${CHARON_AUTO_PERMISSION:-readonly}"
    PRINCIPAL_OUTPUT=$($CHARON_BIN principal reconcile "$CHARON_AUTO_USER" "$PERMISSION" "$PROJECTS" 2>&1)
    PRINCIPAL_ID=$(echo "$PRINCIPAL_OUTPUT" | awk '/^Reconciled principal:/ {print $3}')
    if [ -z "$PRINCIPAL_ID" ]; then
        echo "ERROR: principal reconciliation did not return an ID"
        echo "$PRINCIPAL_OUTPUT"
        exit 1
    fi
    if [ "$AUTH_MODE" != "oauth" ]; then
        echo "  Principal ID: $PRINCIPAL_ID"
        echo "  Projects:     $PROJECTS"
        echo "  Permission:   $PERMISSION"
    fi
    echo "$PRINCIPAL_ID" > "$BOOTSTRAP_PRINCIPAL_FILE"

    MINTED_PRINCIPAL=""
    if [ -f "$TOKEN_MARKER" ]; then
        MINTED_PRINCIPAL=$(tr -d '[:space:]' < "$TOKEN_MARKER")
    fi
    if [ "${CHARON_AUTH_MODE:-obol}" != "oauth" ] &&
       { [ ! -f "$BOOTSTRAP_TOKEN_FILE" ] || [ "$MINTED_PRINCIPAL" != "$PRINCIPAL_ID" ]; }; then
        EXPIRES="${CHARON_AUTO_EXPIRES:-7d}"
        TOKEN_TEMP="${BOOTSTRAP_TOKEN_FILE}.tmp"
        rm -f "$TOKEN_TEMP"
        if ! $CHARON_BIN obol mint --expires "$EXPIRES" "$PRINCIPAL_ID" > "$TOKEN_TEMP"; then
            rm -f "$TOKEN_TEMP"
            exit 1
        fi
        chmod 600 "$TOKEN_TEMP"
        mv "$TOKEN_TEMP" "$BOOTSTRAP_TOKEN_FILE"
        printf '%s\n' "$PRINCIPAL_ID" > "$TOKEN_MARKER"
        chmod 600 "$TOKEN_MARKER"
        echo "Bootstrap Obol written to $BOOTSTRAP_TOKEN_FILE with mode 0600."
    fi

    if [ -z "${CHARON_OAUTH_DEFAULT_USER:-}" ]; then
        export CHARON_OAUTH_DEFAULT_USER="$PRINCIPAL_ID"
        echo "Set CHARON_OAUTH_DEFAULT_USER=$PRINCIPAL_ID"
    fi
fi

# If OAuth default user is still not set, try to load from bootstrap file
if [ -z "${CHARON_OAUTH_DEFAULT_USER:-}" ] && [ -f "$BOOTSTRAP_PRINCIPAL_FILE" ]; then
    BOOTSTRAP_PRINCIPAL=$(cat "$BOOTSTRAP_PRINCIPAL_FILE" | tr -d '[:space:]')
    if [ -n "$BOOTSTRAP_PRINCIPAL" ]; then
        export CHARON_OAUTH_DEFAULT_USER="$BOOTSTRAP_PRINCIPAL"
        echo "Restored CHARON_OAUTH_DEFAULT_USER=$BOOTSTRAP_PRINCIPAL from bootstrap"
    fi
fi

# OAuth client metadata and the generated browser key are printed by the
# long-running server. Keeping the entrypoint quiet makes that key banner the
# first visible OAuth startup output.

# Start charon
exec "$CHARON_BIN" serve

#!/bin/bash
# Charon Docker startup script for an existing canonical Lethe service.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env"

# Create .env from example if it doesn't exist
if [ ! -f "$ENV_FILE" ]; then
    if [ -f "${ENV_FILE}.example" ]; then
        cp "${ENV_FILE}.example" "$ENV_FILE"
        echo "Created .env from .env.example"
    fi
fi

# The environment file contains the Lethe API key and Charon signing key.
if [ -f "$ENV_FILE" ]; then
    chmod 600 "$ENV_FILE"
fi

# Load .env if present
if [ -f "$ENV_FILE" ]; then
    while IFS= read -r LINE || [ -n "$LINE" ]; do
        LINE="${LINE%$'\r'}"
        case "$LINE" in
            ""|\#*) continue ;;
        esac
        KEY="${LINE%%=*}"
        VALUE="${LINE#*=}"
        if [[ "$KEY" =~ ^[A-Z_][A-Z0-9_]*$ ]]; then
            if [[ "$VALUE" == \"*\" && "$VALUE" == *\" ]]; then
                VALUE="${VALUE:1:${#VALUE}-2}"
            elif [[ "$VALUE" == \'*\' && "$VALUE" == *\' ]]; then
                VALUE="${VALUE:1:${#VALUE}-2}"
            fi
            printf -v "$KEY" '%s' "$VALUE"
            export "$KEY"
        fi
    done < "$ENV_FILE"
fi

# Track if we generated anything
GENERATED=0

check_http_200() {
    STATUS=$(curl --max-time 5 -sS -o /dev/null -w '%{http_code}' "$@") || return 1
    [ "$STATUS" = "200" ]
}

# The upstream key must already match the canonical Lethe service. Generating
# a replacement here would only break connectivity.
if [ -z "$LETHE_API_KEY" ] || [ "$LETHE_API_KEY" == "change-me-in-production" ] || [ "$LETHE_API_KEY" == "replace-with-canonical-lethe-api-key" ]; then
    echo "ERROR: set LETHE_API_KEY to the canonical Lethe API key in .env"
    exit 1
fi

# Auto-generate CHARON_HMAC_KEY if not set
if [ -z "$CHARON_HMAC_KEY" ] || [ "$CHARON_HMAC_KEY" == "change-me-in-production" ] || [ "$CHARON_HMAC_KEY" == "replace-with-random-32-byte-key" ] || [ "$CHARON_HMAC_KEY" == "*** " ]; then
    NEW_KEY=$(openssl rand -hex 32)

    if [ -f "$ENV_FILE" ]; then
        if grep -q "^CHARON_HMAC_KEY=" "$ENV_FILE"; then
            if [[ "$OSTYPE" == "darwin"* ]]; then
                sed -i '' "s/^CHARON_HMAC_KEY=.*/CHARON_HMAC_KEY=${NEW_KEY}/" "$ENV_FILE"
            else
                sed -i "s/^CHARON_HMAC_KEY=.*/CHARON_HMAC_KEY=${NEW_KEY}/" "$ENV_FILE"
            fi
        else
            echo "CHARON_HMAC_KEY=${NEW_KEY}" >> "$ENV_FILE"
        fi
    else
        echo "CHARON_HMAC_KEY=${NEW_KEY}" >> "$ENV_FILE"
    fi

    export CHARON_HMAC_KEY="$NEW_KEY"
    GENERATED=1
    echo "Generated CHARON_HMAC_KEY"
fi

if [ "$GENERATED" -eq 1 ]; then
    echo ""
    echo "╔══════════════════════════════════════════════════════════════╗"
    echo "║  Auto-Generated Secrets                                      ║"
    echo "╚══════════════════════════════════════════════════════════════╝"
    echo ""
    echo "Keys saved to .env — they will be reused on subsequent starts."
    echo ""
fi

# Ensure Charon's policy database directory exists. Lethe owns its own storage.
mkdir -p "${SCRIPT_DIR}/charon-data"

# Verify the same canonical upstream Charon will use. The Docker-only host alias
# maps back to localhost for this host-side preflight.
UPSTREAM_FOR_HEALTH="${CHARON_UPSTREAM:-http://host.docker.internal:18483}"
case "$UPSTREAM_FOR_HEALTH" in
    http://host.docker.internal:*|https://host.docker.internal:*)
        UPSTREAM_FOR_HEALTH=$(echo "$UPSTREAM_FOR_HEALTH" | sed 's|://host\.docker\.internal|://localhost|')
        ;;
esac
LETHE_API="${LETHE_HEALTH_URL:-${UPSTREAM_FOR_HEALTH%/}/api/health}"
if ! check_http_200 -H "Authorization: Bearer ${LETHE_API_KEY}" "$LETHE_API"; then
    echo "ERROR: canonical Lethe is not healthy at $LETHE_API"
    exit 1
fi

echo "Starting Charon..."
docker compose up -d --build

# Verify health
echo "Waiting for services to start..."
for i in {1..30}; do
    LETHE_OK=0
    CHARON_OK=0

    if check_http_200 -H "Authorization: Bearer ${LETHE_API_KEY}" "$LETHE_API" >/dev/null 2>&1; then
        LETHE_OK=1
    fi

    if check_http_200 http://localhost:18484/health >/dev/null 2>&1; then
        CHARON_OK=1
    fi

    if [ "$LETHE_OK" -eq 1 ] && [ "$CHARON_OK" -eq 1 ]; then
        echo ""
        echo "╔══════════════════════════════════════════════════════════════╗"
        echo "║  Charon is connected to canonical Lethe                      ║"
        echo "╚══════════════════════════════════════════════════════════════╝"
        echo ""
        echo "  Lethe API: $LETHE_API"
        echo "  Charon:    http://localhost:18484/health"
        echo "  MCP:       http://localhost:18484/mcp"
        echo ""
        if [ "${CHARON_AUTH_MODE:-obol}" == "oauth" ]; then
            echo "  OAuth:     ${CHARON_PUBLIC_URL:-http://localhost:18484}/.well-known/oauth-protected-resource"
        fi
        echo ""
        exit 0
    fi

    sleep 1
done

echo "WARNING: Health checks failed after 30s"
exit 1

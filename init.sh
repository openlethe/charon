#!/bin/sh
set -e

# Charon Init Script
# Auto-bootstraps principal and generates secrets on first start
# Runs once on first container start, then delegates to charon serve

CHARON_BIN="/usr/local/bin/charon"
DATA_DIR="${CHARON_DATA_DIR:-/data}"
SECRETS_FILE="${DATA_DIR}/.secrets"

# Auto-generate HMAC key if not provided
if [ -z "$CHARON_HMAC_KEY" ]; then
    if [ -f "$SECRETS_FILE" ]; then
        # Load previously generated key
        SAVED_KEY=$(grep "^CHARON_HMAC_KEY=" "$SECRETS_FILE" | tail -1 | cut -d= -f2)
        if [ -n "$SAVED_KEY" ] && [ ${#SAVED_KEY} -ge 32 ]; then
            export CHARON_HMAC_KEY="$SAVED_KEY"
            echo "Loaded existing HMAC key from ${SECRETS_FILE}"
        else
            echo "WARNING: Invalid HMAC key in ${SECRETS_FILE}, regenerating..."
            rm -f "$SECRETS_FILE"
            HMAC_KEY=$(openssl rand -hex 32)
            export CHARON_HMAC_KEY="$HMAC_KEY"
            echo "CHARON_HMAC_KEY=$HMAC_KEY" > "$SECRETS_FILE"
            chmod 600 "$SECRETS_FILE"
            echo "Generated new HMAC key (saved to ${SECRETS_FILE})"
        fi
    else
        # Generate new key using openssl (now available in image)
        HMAC_KEY=$(openssl rand -hex 32)
        export CHARON_HMAC_KEY="$HMAC_KEY"
        echo "CHARON_HMAC_KEY=$HMAC_KEY" > "$SECRETS_FILE"
        chmod 600 "$SECRETS_FILE"
        echo ""
        echo "╔══════════════════════════════════════════════════════════════╗"
        echo "║  Charon Auto-Generated Secrets                               ║"
        echo "╚══════════════════════════════════════════════════════════════╝"
        echo ""
        echo "Generated CHARON_HMAC_KEY (saved to ${SECRETS_FILE})"
        echo ""
        echo "IMPORTANT: Add this to your .env file for future restarts:"
        echo "  CHARON_HMAC_KEY=$HMAC_KEY"
        echo ""
    fi
fi

# Validate Lethe key is set
if [ -z "$LETHE_API_KEY" ]; then
    echo "ERROR: LETHE_API_KEY is required."
    echo ""
    echo "Generate one with: openssl rand -hex 32"
    echo "Then add it to your .env file:"
    echo "  LETHE_API_KEY=your-generated-key"
    echo ""
    exit 1
fi

# Reconcile the configured principal on every start. The marker is used only
# to avoid printing/minting a fresh legacy Obol after every restart.
TOKEN_MARKER="${DATA_DIR}/.bootstrap-token-minted"
BOOTSTRAP_PRINCIPAL_FILE="${DATA_DIR}/.bootstrap-principal"

if [ -n "$CHARON_AUTO_USER" ]; then
    echo ""
    echo "╔══════════════════════════════════════════════════════════════╗"
    echo "║  Charon Principal Reconciliation                            ║"
    echo "╚══════════════════════════════════════════════════════════════╝"
    echo ""

    PROJECTS="${CHARON_AUTO_PROJECTS:-${CHARON_AUTO_PROJECT:-default}}"
    PERMISSION="${CHARON_AUTO_PERMISSION:-readonly}"
    PRINCIPAL_OUTPUT=$($CHARON_BIN principal reconcile "$CHARON_AUTO_USER" "$PERMISSION" "$PROJECTS" 2>&1)
    PRINCIPAL_ID=$(echo "$PRINCIPAL_OUTPUT" | awk '/^Reconciled principal:/ {print $3}')
    if [ -z "$PRINCIPAL_ID" ]; then
        echo "ERROR: principal reconciliation did not return an ID"
        echo "$PRINCIPAL_OUTPUT"
        exit 1
    fi
    echo "  Principal ID: $PRINCIPAL_ID"
    echo "  Projects:     $PROJECTS"
    echo "  Permission:   $PERMISSION"
    echo "$PRINCIPAL_ID" > "$BOOTSTRAP_PRINCIPAL_FILE"

    if [ ! -f "$TOKEN_MARKER" ] && [ "${CHARON_AUTH_MODE:-obol}" != "oauth" ]; then
        EXPIRES="${CHARON_AUTO_EXPIRES:-7d}"
        echo ""
        echo "═══════════════════════════════════════════════════════════════"
        echo "  TOKEN FOR $CHARON_AUTO_USER"
        echo "═══════════════════════════════════════════════════════════════"
        $CHARON_BIN obol mint --expires "$EXPIRES" "$PRINCIPAL_ID"
        echo "═══════════════════════════════════════════════════════════════"
        echo ""
        echo "Copy the token above into your MCP client settings."
        echo "This message will not appear again."
        echo ""
        
        touch "$TOKEN_MARKER"
    fi

    if [ -z "$CHARON_OAUTH_DEFAULT_USER" ]; then
        export CHARON_OAUTH_DEFAULT_USER="$PRINCIPAL_ID"
        echo "Set CHARON_OAUTH_DEFAULT_USER=$PRINCIPAL_ID"
    fi
fi

# If OAuth default user is still not set, try to load from bootstrap file
if [ -z "$CHARON_OAUTH_DEFAULT_USER" ] && [ -f "$BOOTSTRAP_PRINCIPAL_FILE" ]; then
    BOOTSTRAP_PRINCIPAL=$(cat "$BOOTSTRAP_PRINCIPAL_FILE" | tr -d '[:space:]')
    if [ -n "$BOOTSTRAP_PRINCIPAL" ]; then
        export CHARON_OAUTH_DEFAULT_USER="$BOOTSTRAP_PRINCIPAL"
        echo "Restored CHARON_OAUTH_DEFAULT_USER=$BOOTSTRAP_PRINCIPAL from bootstrap"
    fi
fi

# Print OAuth configuration for easy client setup
if [ "${CHARON_AUTH_MODE:-obol}" = "oauth" ] && [ -n "$CHARON_PUBLIC_URL" ]; then
    echo ""
    echo "╔══════════════════════════════════════════════════════════════╗"
    echo "║  OAuth / MCP Client Configuration                            ║"
    echo "╚══════════════════════════════════════════════════════════════╝"
    echo ""
    echo "  Client ID: ${CHARON_OAUTH_CLIENT_ID:-chatgpt-mcp}"
    echo "  Metadata:   ${CHARON_PUBLIC_URL}/.well-known/oauth-protected-resource"
    echo "  Authorize:  ${CHARON_PUBLIC_URL}/oauth/authorize"
    echo "  Token:      ${CHARON_PUBLIC_URL}/oauth/token"
    echo ""
    echo "  Redirects: ${CHARON_OAUTH_REDIRECT_URIS:-not configured}"
    echo ""
fi

# Start charon
exec $CHARON_BIN serve

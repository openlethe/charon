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
    echo "Generate one with: docker run --rm lethe:local-v0.4 keygen"
    echo "Then add it to your .env file:"
    echo "  LETHE_API_KEY=lethe_xxx..."
    echo ""
    exit 1
fi

# Check if we should auto-bootstrap
if [ -n "$CHARON_AUTO_USER" ]; then
    # Use a marker file to prevent re-bootstrapping on restart
    BOOTSTRAP_MARKER="${DATA_DIR}/.bootstrapped"
    
    if [ ! -f "$BOOTSTRAP_MARKER" ]; then
        echo ""
        echo "╔══════════════════════════════════════════════════════════════╗"
        echo "║  Charon Auto-Bootstrap                                       ║"
        echo "╚══════════════════════════════════════════════════════════════╝"
        echo ""
        echo "Creating principal: $CHARON_AUTO_USER"
        
        # Create principal
        PRINCIPAL_OUTPUT=$($CHARON_BIN principal create "$CHARON_AUTO_USER" 2>&1)
        PRINCIPAL_ID=$(echo "$PRINCIPAL_OUTPUT" | awk '{print $3}')
        echo "  Principal ID: $PRINCIPAL_ID"
        
        # Grant project
        PROJECT="${CHARON_AUTO_PROJECT:-default}"
        $CHARON_BIN principal grant-project "$PRINCIPAL_ID" "$PROJECT" > /dev/null 2>&1
        echo "  Project: $PROJECT"
        
        # Grant scopes based on permission level
        PERMISSION="${CHARON_AUTO_PERMISSION:-propose}"
        echo "  Permission: $PERMISSION"
        
        case "$PERMISSION" in
            readonly)
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.search > /dev/null 2>&1
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.read > /dev/null 2>&1
                ;;
            propose)
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.search > /dev/null 2>&1
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.read > /dev/null 2>&1
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.propose > /dev/null 2>&1
                ;;
            write)
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.search > /dev/null 2>&1
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.read > /dev/null 2>&1
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.propose > /dev/null 2>&1
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.write > /dev/null 2>&1
                ;;
            *)
                echo "WARNING: Unknown permission '$PERMISSION', defaulting to propose"
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.search > /dev/null 2>&1
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.read > /dev/null 2>&1
                $CHARON_BIN principal grant-scope "$PRINCIPAL_ID" memory.propose > /dev/null 2>&1
                ;;
        esac
        
        # Mint token
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
        
        # Create marker file to prevent re-bootstrap
        touch "$BOOTSTRAP_MARKER"
    fi
fi

# Print OAuth configuration for easy client setup
if [ -n "$CHARON_PUBLIC_URL" ]; then
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
    echo "  (Any client ID works — Charon auto-registers unknown clients)"
    echo ""
fi

# Start charon
exec $CHARON_BIN serve

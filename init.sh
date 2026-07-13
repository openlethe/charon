#!/bin/sh
set -e

# Charon Init Script
# Auto-bootstraps principal if CHARON_AUTO_USER is set and DB is empty
# Runs once on first container start, then delegates to charon serve

CHARON_BIN="/usr/local/bin/charon"
DATA_DIR="${CHARON_DATA_DIR:-/data}"

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

# Start charon
exec $CHARON_BIN serve

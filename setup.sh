#!/bin/bash
set -euo pipefail

# Charon Secure Setup Script
# Generates .env, creates principals, mints initial tokens
# Run once: ./setup.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA_DIR="${SCRIPT_DIR}/charon-data"
SECRETS_DIR="${SCRIPT_DIR}/secrets"

# Create directories
mkdir -p "$DATA_DIR" "$SECRETS_DIR"
chmod 700 "$SECRETS_DIR"

# Generate secrets if .env doesn't exist
ENV_FILE="${SCRIPT_DIR}/.env"
if [[ ! -f "$ENV_FILE" ]]; then
	 echo "Generating secure secrets..."
	 
	 LETHE_API_KEY="lethe_$(openssl rand -hex 32)"
	 CHARON_HMAC_KEY="$(openssl rand -hex 32)"
	 
	 cat > "$ENV_FILE" << EOF
# Charon Secure Configuration
# Generated on $(date -u +%Y-%m-%dT%H:%M:%SZ)
# DO NOT COMMIT TO GIT

# Lethe API key (shared between Lethe and Charon)
LETHE_API_KEY=${LETHE_API_KEY}

# Charon HMAC key for Obol signing
CHARON_HMAC_KEY=${CHARON_HMAC_KEY}

# Public URL (update after creating Cloudflare Tunnel)
CHARON_PUBLIC_URL=http://localhost:18484

# Admin bind (Unix socket for security)
CHARON_ADMIN_HTTP=/tmp/charon-admin.sock
EOF
	 
	 chmod 600 "$ENV_FILE"
	 echo "✓ Created .env with generated secrets"
else
	 echo "✓ .env already exists, loading..."
fi

# Load .env
set -a
source "$ENV_FILE"
set +a

# Build Charon if needed
if [[ ! -f "${SCRIPT_DIR}/bin/charon" ]]; then
	 echo "Building Charon..."
	 make -C "$SCRIPT_DIR" build
fi

# Initialize database and create principals
 echo ""
 echo "=== Creating Principals ==="

# OpenClaw principal (full access)
ARCHIMEDES_ID=$("${SCRIPT_DIR}/bin/charon" principal create "OpenClaw (archimedes)" 2>> /dev/null | awk '{print $3}')
echo "✓ OpenClaw principal: ${ARCHIMEDES_ID}"

"${SCRIPT_DIR}/bin/charon" principal grant-project "$ARCHIMEDES_ID" default > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$ARCHIMEDES_ID" memory.search > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$ARCHIMEDES_ID" memory.read > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$ARCHIMEDES_ID" memory.propose > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$ARCHIMEDES_ID" memory.write > /dev/null
echo "  → Full read+write access to 'default' project"

# ChatGPT principal (read + propose only)
CHATGPT_ID=$("${SCRIPT_DIR}/bin/charon" principal create "ChatGPT" 2>> /dev/null | awk '{print $3}')
echo "✓ ChatGPT principal: ${CHATGPT_ID}"

"${SCRIPT_DIR}/bin/charon" principal grant-project "$CHATGPT_ID" default > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$CHATGPT_ID" memory.search > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$CHATGPT_ID" memory.read > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$CHATGPT_ID" memory.propose > /dev/null
echo "  → Read + propose access to 'default' project"

# Claude principal (read + propose only)
CLAUDE_ID=$("${SCRIPT_DIR}/bin/charon" principal create "Claude" 2>> /dev/null | awk '{print $3}')
echo "✓ Claude principal: ${CLAUDE_ID}"

"${SCRIPT_DIR}/bin/charon" principal grant-project "$CLAUDE_ID" default > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$CLAUDE_ID" memory.search > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$CLAUDE_ID" memory.read > /dev/null
"${SCRIPT_DIR}/bin/charon" principal grant-scope "$CLAUDE_ID" memory.propose > /dev/null
echo "  → Read + propose access to 'default' project"

# Mint initial tokens (7-day expiry)
echo ""
echo "=== Minting Initial Tokens (7-day expiry) ==="

"${SCRIPT_DIR}/bin/charon" obol mint "$ARCHIMEDES_ID" --expires 7d > "${SECRETS_DIR}/archimedes-obol.txt"
chmod 600 "${SECRETS_DIR}/archimedes-obol.txt"
echo "✓ OpenClaw token → secrets/archimedes-obol.txt"

"${SCRIPT_DIR}/bin/charon" obol mint "$CHATGPT_ID" --expires 7d > "${SECRETS_DIR}/chatgpt-obol.txt"
chmod 600 "${SECRETS_DIR}/chatgpt-obol.txt"
echo "✓ ChatGPT token → secrets/chatgpt-obol.txt"

"${SCRIPT_DIR}/bin/charon" obol mint "$CLAUDE_ID" --expires 7d > "${SECRETS_DIR}/claude-obol.txt"
chmod 600 "${SECRETS_DIR}/claude-obol.txt"
echo "✓ Claude token → secrets/claude-obol.txt"

# Save principal IDs for rotation script
cat > "${SECRETS_DIR}/principal-ids.txt" << EOF
ARCHIMEDES_ID=${ARCHIMEDES_ID}
CHATGPT_ID=${CHATGPT_ID}
CLAUDE_ID=${CLAUDE_ID}
EOF
chmod 600 "${SECRETS_DIR}/principal-ids.txt"

echo ""
echo "=== Setup Complete ==="
echo ""
echo "Next steps:"
echo "  1. Update CHARON_PUBLIC_URL in .env after creating Cloudflare Tunnel"
echo "  2. Start services: docker-compose up -d"
echo "  3. Rotate tokens weekly: ./scripts/rotate-tokens.sh"
echo ""
echo "Token locations:"
echo "  - secrets/archimedes-obol.txt (OpenClaw)"
echo "  - secrets/chatgpt-obol.txt (ChatGPT)"
echo "  - secrets/claude-obol.txt (Claude)"

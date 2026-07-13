#!/bin/bash
set -euo pipefail

# Token Rotation Script
# Run weekly via cron: 0 0 * * 0 /path/to/charon/scripts/rotate-tokens.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_DIR="${SCRIPT_DIR}/secrets"

if [[ ! -f "${SECRETS_DIR}/principal-ids.txt" ]]; then
	echo "Error: Principal IDs not found. Run setup.sh first."
	exit 1
fi

# Load principal IDs
set -a
source "${SECRETS_DIR}/principal-ids.txt"
set +a

# Load environment
set -a
source "${SCRIPT_DIR}/.env"
set +a

echo "Rotating tokens on $(date -u +%Y-%m-%dT%H:%M:%SZ)..."

# Function to rotate a token
rotate_token() {
	local principal_id=$1
	local name=$2
	local secret_file=$3
	
	# Revoke old tokens for this principal
	"${SCRIPT_DIR}/bin/charon" obol ls "$principal_id" | while read -r line; do
		if [[ "$line" =~ obol_ ]]; then
			local obol_id=$(echo "$line" | grep -oP 'obol_[a-f0-9-]+')
			if [[ -n "$obol_id" ]]; then
				echo "  Revoking old token for $name..."
				"${SCRIPT_DIR}/bin/charon" obol revoke "$obol_id" > /dev/null 2>&1 || true
			fi
		fi
	done
	
	# Mint new token (7-day expiry)
	echo "  Minting new token for $name..."
	"${SCRIPT_DIR}/bin/charon" obol mint "$principal_id" --expires 7d > "$secret_file"
	chmod 600 "$secret_file"
	echo "  ✓ New token saved to $secret_file"
}

# Rotate all tokens
rotate_token "$ARCHIMEDES_ID" "OpenClaw" "${SECRETS_DIR}/archimedes-obol.txt"
rotate_token "$CHATGPT_ID" "ChatGPT" "${SECRETS_DIR}/chatgpt-obol.txt"
rotate_token "$CLAUDE_ID" "Claude" "${SECRETS_DIR}/claude-obol.txt"

echo ""
echo "Token rotation complete."
echo "Next rotation: $(date -u -d '+7 days' +%Y-%m-%d)"
echo ""
echo "IMPORTANT: Update ChatGPT/Claude MCP settings with new tokens:"
echo "  - ChatGPT: $(cat ${SECRETS_DIR}/chatgpt-obol.txt | grep 'Token:' | awk '{print $2}')"
echo "  - Claude: $(cat ${SECRETS_DIR}/claude-obol.txt | grep 'Token:' | awk '{print $2}')"

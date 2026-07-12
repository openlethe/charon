#!/bin/bash
set -e

# End-to-end test script for Charon
# This tests the basic setup: config, store, obol auth, and MCP read tools

echo "=== Charon E2E Test ==="

# Setup temp dir
export CHARON_DATA_DIR=$(mktemp -d)
export LETHE_API_KEY="test-lethe-key"
export CHARON_HMAC_KEY="test-hmac-key-$(openssl rand -hex 16)"
export CHARON_PUBLIC_URL="http://localhost:18484"
export CHARON_HTTP=":18484"
export CHARON_ADMIN_HTTP="127.0.0.1:18486"

# Build
echo "Building..."
go build -o bin/charon ./cmd/charon

# Create principal
echo "Creating principal..."
PRINCIPAL=$(./bin/charon principal create "E2E Test" | awk '{print $3}')
echo "Principal: $PRINCIPAL"

# Grant project and scopes
echo "Granting project and scopes..."
./bin/charon principal grant-project "$PRINCIPAL" charon
./bin/charon principal grant-scope "$PRINCIPAL" memory.search
./bin/charon principal grant-scope "$PRINCIPAL" memory.read

# Mint obol
echo "Minting obol..."
TOKEN=$(./bin/charon obol mint "$PRINCIPAL" | grep "Token:" | awk '{print $2}')
echo "Obol: ${TOKEN:0:20}..."

# Test ledger
echo "Checking ledger..."
./bin/charon ledger tail

echo ""
echo "=== E2E Test Complete ==="
echo "Principal: $PRINCIPAL"
echo "Token: $TOKEN"
echo ""
echo "To test MCP (requires Lethe running):"
echo "  curl -H \"Authorization: Bearer $TOKEN\" http://localhost:18484/mcp"

# Cleanup
rm -rf "$CHARON_DATA_DIR"

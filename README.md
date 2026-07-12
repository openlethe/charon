# Charon

> Lethe remembers. Charon carries.

Charon is a self-hosted MCP policy gateway for [Lethe](https://github.com/openlethe/lethe). It provides scoped, auditable access to Lethe memory for AI clients while keeping Lethe private.

## Architecture

```
MCP Client (ChatGPT, Claude, etc.)
  |
  | HTTPS
  v
Cloudflare Tunnel / cloudflared
  |
  | HTTP
  v
Charon (MCP Gateway + Auth)
  |
  | Private Network
  v
Lethe (Memory Store)
```

## MCP Tools

### Read-Only (Phase 0-4)
- `memory_search` — Search accepted memory across authorized projects
- `memory_get` — Retrieve a single memory event by ID
- `memory_recent` — Retrieve recent accepted memory

### Proposals (Phase 5)
- `memory_propose` — Propose a durable memory entry for approval
- `memory_checkpoint` — Propose a structured conversation checkpoint
- `proposal_list` — List pending memory proposals

### Direct Writes (Phase 6)
- `memory_write` — Direct trusted write to Lethe (requires `memory.write` scope)

## CLI Commands

```bash
export CHARON_HTTP=":18484"
export CHARON_ADMIN_HTTP="127.0.0.1:18486"
export CHARON_UPSTREAM="http://localhost:18483"
export CHARON_PUBLIC_URL="https://charon.example.com"
export LETHE_API_KEY="your-lethe-api-key"
export CHARON_HMAC_KEY="$(openssl rand -hex 32)"
export CHARON_DATA_DIR="./data"
```

### 2. Build and Run

```bash
make build
./bin/charon serve
```

### 3. Create a Principal

```bash
./bin/charon principal create "My Client"
# Created principal: principal_...

./bin/charon principal grant-project principal_... charon
./bin/charon principal grant-scope principal_... memory.search
./bin/charon principal grant-scope principal_... memory.read
```

### 4. Mint an Obol

```bash
./bin/charon obol mint principal_...
# Obol minted:
#   ID:    ...
#   Token: obol_..._...
#   Exp:   2026-08-11...
```

### 5. Test MCP Connection

```bash
curl -H "Authorization: Bearer obol_..._..." \
     http://localhost:18484/mcp
```

## CLI Commands

| Command | Description |
|---------|-------------|
| `charon serve` | Start the MCP gateway server |
| `charon principal create <name>` | Create a new principal |
| `charon principal grant-project <id> <project>` | Grant project access |
| `charon principal grant-scope <id> <scope>` | Grant scope |
| `charon obol mint <principal-id>` | Mint a new obol |
| `charon obol ls <principal-id>` | List obols |
| `charon obol revoke <obol-id>` | Revoke an obol |
| `charon proposal ls [limit]` | List pending proposals |
| `charon proposal approve <proposal-id>` | Approve a proposal |
| `charon proposal reject <proposal-id>` | Reject a proposal |
| `charon ledger tail [limit]` | View recent ledger entries |

## Docker

```bash
docker-compose up -d
```

## Development

```bash
make test    # Run tests
make lint    # Format and vet
make build   # Build binary
make docker  # Build container
```

## Security

- **Fail-closed**: Missing secrets prevent startup
- **Least-privilege**: Scoped projects and scopes per principal
- **No generic proxy**: Strict Lethe adapter with typed methods only
- **Quarantine**: All model writes go through proposal approval (Phase 5)
- **Audit**: Hash-chained ledger entries for every operation
- **Obols**: High-entropy bearer credentials with HMAC verification
- **Local admin**: Admin interface binds to loopback only

## License

MIT

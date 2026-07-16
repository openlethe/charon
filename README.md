# Charon

> Lethe remembers. Charon carries.

Charon is a self-hosted MCP policy gateway for [Lethe](https://github.com/openlethe/lethe). It provides scoped, auditable access to Lethe memory for AI clients while keeping Lethe private.

Charon defaults to `CHARON_MODE=memory-git`, which registers only versioned
branch, changeset, context, review, and merge tools. The original mutable event
surface remains available only through the explicit `CHARON_MODE=direct`
compatibility setting.

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
Charon (MCP policy + Memory Git workflow)
  |
  | Private local network
  v
Lethe Git (git mode, port 18485, isolated SQLite)

OpenClaw ──> OpenLethe (legacy mode, port 18483, existing SQLite)
```

## MCP Tools

In `memory-git` mode only the thirteen Memory Git V1 tools below are
registered. The read/proposal/write/thread tools are the `direct` compatibility
surface and cannot be called through the versioned deployment.

### Direct compatibility: Read-Only (Phase 0-4)
- `memory_search` — Search accepted memory across authorized projects
- `memory_get` — Retrieve a single memory event by ID
- `memory_recent` — Retrieve recent accepted memory
- `thread_list` — List threads for an authorized session
- `thread_get` — Retrieve a thread from an authorized project
- `thread_events` — Retrieve authorized events in a thread

### Direct compatibility: Proposals (Phase 5)
- `memory_propose` — Quarantine a proposed legacy memory entry
- `memory_checkpoint` — Quarantine a structured conversation checkpoint
- `proposal_list` — List pending memory proposals

Legacy proposal approval currently fails closed because Lethe's event-create API
does not offer an idempotency key or caller-supplied event ID. Use Memory Git
for reviewed durable writes; direct compatibility proposals may be inspected or
rejected but are not falsely reported as materialized.

### Direct compatibility: Writes (Phase 6)
- `memory_write` — Direct trusted write to Lethe (requires `memory.write` scope)

### Memory Git V1
- `memory_repo_init` — Idempotently create a project's synthetic root and protected `refs/shared/main` (exact-project `memory.commit` authority required)
- `memory_status`, `memory_log`, `memory_show`, `memory_diff`, `memory_ref_list` — Inspect authorized project history
- `memory_context_at` — Reconstruct an exact authorized ref/head; pin selected IDs only with exact-project commit/write authority
- `memory_branch_create` — Create an owned agent, session, or topic ref
- `memory_changeset_create` — CAS commit to an owned non-protected ref
- `memory_merge_propose` — Reuse Charon proposals as merge requests into protected refs
- `memory_merge_review` — Record an explicit approve, request-changes, or reject verdict bound to the current proposal snapshot
- `memory_merge` — Apply only a currently approved fast-forward, multi-parent, or selected-op merge
- `memory_merge_cancel` — Cancel a pending merge request without deleting its branch history

## Quick start

```bash
export CHARON_HTTP=":18484"
export CHARON_ADMIN_HTTP="127.0.0.1:18486"
export CHARON_MODE="memory-git"
export CHARON_UPSTREAM="http://localhost:18485"
export CHARON_PUBLIC_URL="https://charon.example.com"
export LETHE_API_KEY="your-lethe-api-key"
export CHARON_OBOL_HMAC_KEY="$(openssl rand -hex 32)"
export CHARON_OAUTH_HMAC_KEY="$(openssl rand -hex 32)"
export CHARON_MERGE_HMAC_KEY="$(openssl rand -hex 32)"
# Configure CHARON_MERGE_HMAC_KEY on trusted Lethe as CHARON_HMAC_KEY so
# protected merge signatures can be verified. Keep all three values distinct.
export CHARON_DATA_DIR="./data"
```

### 2. Build and Run

```bash
make build
./bin/charon serve
```

`bin/charon` is a tracked development launcher, not a compiled artifact. It
executes the root `./charon` binary produced by `make build` when present and
otherwise falls back to `go run ./cmd/charon`. Tagged release binaries are
created under `dist/` and published by the release workflow.

### 3. Create a Principal

```bash
# Trusted local author: exact project grant plus branch/commit/propose scopes.
./bin/charon principal reconcile "Local Memory Author" propose lethe-git
# Reconciled principal: principal_...

# Independent reviewer: exact project grant, review/merge authority, no direct write.
./bin/charon principal reconcile "Local Memory Reviewer" review lethe-git

# Read-only clients may use a wildcard project grant.
./bin/charon principal reconcile "Read Only Client" readonly '*'
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
| `charon version` | Show version, commit, build time, and Go runtime |
| `charon serve` | Start the MCP gateway server |
| `charon principal create <name>` | Create a new principal |
| `charon principal grant-project <id> <project>` | Grant project access |
| `charon principal grant-scope <id> <scope>` | Grant scope |
| `charon principal reconcile <name> <permission> <projects>` | Create/update exact grants |
| `charon principal disable <id>` | Immediately disable authentication for a principal |
| `charon principal enable <id>` | Re-enable a disabled principal |
| `charon obol mint <principal-id>` | Mint a new obol |
| `charon obol ls <principal-id>` | List obols |
| `charon obol revoke <obol-id>` | Revoke an obol |
| `charon proposal ls [limit]` | List pending proposals |
| `charon proposal approve <proposal-id>` | Fails closed for legacy event proposals until Lethe exposes idempotent materialization; Memory Git merges use `memory_merge` |
| `charon proposal reject <proposal-id>` | Reject a proposal |
| `charon ledger tail [limit]` | View recent ledger entries |
| `charon ledger verify` | Verify every audit hash and predecessor link |

## Docker

The primary `docker-compose.yml` is a hardened local production reference. It
runs as an unprivileged user, binds the MCP port to loopback, drops Linux
capabilities, enables `no-new-privileges`, uses a read-only root filesystem,
limits processes and memory, and mounts only a named `/data` volume. Charon
never mounts or writes Lethe's SQLite database directly.

```bash
cp .env.example .env
# Edit .env so the upstream key, three purpose-specific HMAC keys,
# authentication mode, project grants, and public URL match the deployment.
docker compose up -d --build
```

For the repository's isolated sibling-checkout E2E environment, use the
separate test stack instead:

```bash
docker compose -f docker-compose.memory-git-test.yml up -d
```

That test stack intentionally expects `../lethe/.env.git`, publishes Charon on
`127.0.0.1:18487`, stores its database in a dedicated named volume, and writes
only local E2E credential/expectation files under the ignored
`./charon-memory-git-data` directory.

OAuth authorization is restricted to `CHARON_OAUTH_CLIENT_ID` and the explicit
redirect allowlist. A trailing `*` permits ChatGPT's per-app callback path, but
the scheme, host, and path prefix are parsed and validated independently.

The browser authorization step displays Charon's no-store pairing page, including
the exact client ID, callback destination, and protected resource. The primary
Compose profile generates a fresh 32-byte URL-safe authorization key at every
Charon process start and prints it once in a dedicated first-output container
banner. The page accepts that exact key, tolerates accidental leading or trailing
whitespace, shows an immediate authorizing state after submission, and places
key-mismatch feedback at the top of the authorization card.
Set `CHARON_OAUTH_GENERATE_PAIRING_SECRET=false` only when a static 32+ character
`CHARON_OAUTH_PAIRING_SECRET` is intentionally managed outside the container;
static values are never logged. Treat container logs as sensitive while the
runtime key is active. Public clients must use S256 PKCE and state. Tokens are
capped to read/proposal scopes by default. In `memory-git`
mode, a trusted deployment may explicitly set
`CHARON_OAUTH_ALLOW_AUTHOR_SCOPES=true` to retain only `memory.branch` and
`memory.commit` when the principal already has those grants. Review, merge, and
direct-write scopes remain stripped from this OAuth flow.

The explicit review-verdict field changes the MCP tool schema. Existing ChatGPT
connectors must refresh or reconnect after upgrading so they retrieve the new
`memory_merge_review` contract, and OAuth clients must complete a new paired
authorization if their existing token is no longer accepted.

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
- **Mode isolation**: `memory-git` mode does not register legacy read/write/proposal/thread tools
- **Quarantine**: Canonical/protected memory changes go through proposal review; branch work remains isolated
- **Audit**: Serialized transactional metadata ledger with verifiable hashes; write failures are surfaced in logs
- **Protected refs**: Charon-signed merge CAS; generic Lethe writes cannot move protected refs
- **Obols**: High-entropy bearer credentials with purpose-specific HMAC verification
- **Key separation**: Distinct Obol, OAuth, and protected-merge keys with a legacy migration fallback
- **Local admin**: Admin interface binds to loopback or a Unix socket only
- **Bounded HTTP**: Request bodies, headers, reads, idle connections, upstream calls, and shutdown are capped
- **Container isolation**: Pinned base images, non-root UID, dropped capabilities, read-only root filesystem

Report security issues through [SECURITY.md](SECURITY.md), not a public issue.
See [CONTRIBUTING.md](CONTRIBUTING.md) for development and review requirements
and [docs/operations.md](docs/operations.md) for health, backup, restoration,
rotation, and incident-response procedures. The local hardening history and
verification evidence are tracked in
[PRODUCTION_HARDENING.md](PRODUCTION_HARDENING.md).

## License

MIT

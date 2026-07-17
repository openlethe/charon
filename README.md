# Charon

> Lethe remembers. Charon carries.

Charon is the MCP authorization and governance gateway for [Lethe](https://github.com/openlethe/lethe). It controls project scopes, proposals, review, protected merges, credentials, and audit behavior while keeping Lethe private. Charon requires a reachable Lethe instance; ChatGPT, Claude, and other MCP clients connect to Charon, never to Lethe directly.

Charon defaults to `CHARON_MODE=memory-git`, which registers only versioned
branch, changeset, context, review, and merge tools. The original mutable event
surface remains available only through the explicit `CHARON_MODE=direct`
compatibility setting.

## Architecture

```text
ChatGPT / Claude / Generic MCP Client
                |
                | OAuth or Obol-authenticated MCP
                v
             Charon
   authorization / policy / review
                |
                | private typed API
                v
              Lethe
     canonical persistent memory

OpenClaw
   |
   | optional automatic context integration
   v
Lethe OpenClaw Plugin
```

The OpenClaw plugin is optional and does not replace Charon: generic MCP
clients use the full Memory Git workflow by calling Charon's tools explicitly.
A typical deployment fronts Charon with Cloudflare Tunnel over HTTPS while
Lethe Git (port 18485, isolated SQLite) stays on the private network; OpenClaw
keeps using OpenLethe (legacy mode, port 18483) separately.

## Compatibility

| Component | Release |
|---|---|
| Charon | `v0.1.0-beta.1` |
| Lethe | `v0.4.0-beta.1` |
| Memory Git schema | `memory_git/v1` |
| Merge authorization | `memory-git-merge/v2` |
| Lethe OpenClaw plugin | matching Lethe release |

Canonical default ports:

| Service | Default |
|---|---:|
| Legacy/OpenClaw Lethe | `18483` |
| Charon MCP | `18484` |
| Lethe Git | `18485` |
| Charon reviewer | `18486` |

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

Prerequisite: a reachable Lethe instance. For Memory Git mode, run Lethe in
`git` mode on port 18485 (see the Lethe repository's `docker-compose.git.yml`)
and point `CHARON_UPSTREAM` at it.

### 1. Configure `.env` manually (no scripts required)

Copy `.env.example` to `.env` and replace the placeholders, or write the file
from scratch — every value is operator-generated. A complete manual `.env`
looks like this:

```bash
# ── Required ──────────────────────────────────────────────
# Must match the key the upstream Lethe was started with.
LETHE_API_KEY=replace-with-upstream-lethe-api-key

# Three distinct signing keys. Generate each independently:
#   openssl rand -hex 32
# Never reuse one value across purposes; leave legacy CHARON_HMAC_KEY unset.
CHARON_OBOL_HMAC_KEY=replace-with-random-64-hex
CHARON_OAUTH_HMAC_KEY=replace-with-random-64-hex

# Must ALSO be configured on Lethe with the identical value — it signs and
# verifies the memory-git-merge/v2 merge envelopes.
CHARON_MERGE_HMAC_KEY=replace-with-random-64-hex

# Public URL of this gateway — credential audience + OAuth metadata.
CHARON_PUBLIC_URL=https://charon.example.com

# OAuth client configuration (browser-flow clients such as ChatGPT).
CHARON_OAUTH_CLIENT_ID=chatgpt-mcp
CHARON_OAUTH_REDIRECT_URIS=https://chatgpt.com/connector/oauth/*
# Principal (ID or unique name) the OAuth flow issues tokens for. Leave
# blank until after `charon principal reconcile`, then fill it in.
CHARON_OAUTH_DEFAULT_USER=

# ── Defaults you can keep ─────────────────────────────────
CHARON_MODE=memory-git
CHARON_AUTH_MODE=oauth
# Plaintext HTTP to a non-loopback upstream is refused; the documented
# local Docker Desktop gateway is the one allowed exception.
# CHARON_UPSTREAM=http://host.docker.internal:18485
# CHARON_ALLOW_INSECURE_UPSTREAM=1

# ── Optional ──────────────────────────────────────────────
# CHARON_OAUTH_GENERATE_PAIRING_SECRET=true   # fresh browser pairing key on every start (default in Compose)
# CHARON_OAUTH_ALLOW_AUTHOR_SCOPES=false      # keep OAuth tokens read/propose-only
# CHARON_AUTO_USER=chatgpt                    # auto-bootstrap a principal + token on first start
# CHARON_AUTO_PERMISSION=readonly             # readonly | propose | review | write
# CHARON_AUTO_PROJECTS=*
# CHARON_LEDGER_HMAC_KEY=                     # purpose-specific ledger signing key (recommended)
# CHARON_LEDGER_ANCHOR_PATH=./data/ledger-anchor.jsonl
# CHARON_MERGE_AUTH_TTL=2m                    # envelope lifetime (hard cap 15m)
# CHARON_RECOVERY_READONLY=false              # set true after a restore until reconcile passes
```

The full annotated reference is `.env.example`. After editing:
`chmod 600 .env` and never commit it — `.gitignore` already covers it.

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
# Configure the same value on trusted Lethe as CHARON_MERGE_HMAC_KEY
# (CHARON_HMAC_KEY accepted as a legacy fallback) so protected merge
# signatures can be verified. Keep all three values distinct.
export CHARON_DATA_DIR="./data"
```

### 2. Run with Docker (pulls the published image)

The Compose stack pulls `ghcr.io/openlethe/charon:latest` — no local build:

```bash
docker compose up -d          # primary OAuth gateway on 127.0.0.1:18484
                              # reviewer Obol gateway on 127.0.0.1:18486
```

The `charon-data` named volume is created automatically on first start and
holds the policy database. To build from source instead:

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

## Independent review path

Memory Git changes reach protected refs only through independent review:

1. An author principal (exact project grant plus branch/commit scopes)
   commits a changeset to an owned branch ref with `memory_changeset_create`
   and opens a proposal with `memory_merge_propose`.
2. A separate reviewer principal (review/merge authority, no direct write)
   records an explicit approve, request-changes, or reject verdict with
   `memory_merge_review`, bound to the exact proposal snapshot reviewed.
3. Only a currently approved proposal can be applied with `memory_merge`;
   review findings against a stale or mutated proposal fail closed.

Author and reviewer principals are distinct credentials (see quick start step
3), so no single principal can both propose and approve the same protected
merge.

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

## Optional OpenClaw integration

The Lethe OpenClaw plugin is optional. It automatically retrieves and injects
accepted memory into OpenClaw conversations and records context metadata.
Charon and Lethe work without it: ChatGPT, Claude, and other MCP clients use
Charon's Memory Git tools directly. Install the plugin from ClawHub when an
OpenClaw agent should receive accepted memory automatically instead of calling
MCP tools explicitly.

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
rotation, and incident-response procedures.

## License

MIT

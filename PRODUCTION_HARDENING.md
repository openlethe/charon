# Charon Production Hardening Tracker

Status: local source hardening, runtime-generated OAuth authorization-key flow,
Lethe-styled pairing UI, local/public OAuth routing validation, and AUTOREVIEW
closeout complete. Credential migration, tunnel-token rotation, ChatGPT connector
reauthorization, live Lethe reconciliation, and the final credentialed E2E remain
incomplete. No commit, push, remote release, or production deployment has been
performed.

This tracker records the work completed on the local `memory-git-local` branch
against base commit `717e4b5`. It distinguishes source verification from
credentialed integration, remote CI, and production validation.

## Objective

Harden Charon into an open-source production candidate while preserving the
existing Memory Git architecture and unrelated local work. The review covered:

- OAuth and Obol authentication;
- project and scope authorization;
- actor-owned refs and protected merges;
- idempotency and crash recovery;
- typed Lethe adapter behavior;
- request and response limits;
- SQLite durability and audit integrity;
- container and Compose isolation;
- CI, release, dependency, and project-governance controls;
- operator documentation and recovery procedures.

## Sprint record

| Sprint | Area | Result |
|---|---|---|
| 0 | Baseline production-readiness review | Completed; release blockers identified and scope frozen |
| 1 | OAuth and protected-merge authorization | Completed; generated or static pairing keys and explicit review verdicts enforced |
| 2 | HTTP reliability, principals, and audit ledger | Completed; bounded servers, disablement, graceful shutdown, and ledger verification added |
| 3 | Keys, toolchain, container, and vulnerability posture | Completed; purpose-specific keys, Go 1.25.12, pinned non-root image, and zero reachable vulnerabilities |
| 4 | Open-source governance and release gates | Completed locally; CI, release, Dependabot, policies, changelog, and operations runbook added |
| 5 | AUTOREVIEW and behavior closeout | Completed for source and local proof; credentialed final E2E requires connector reauthorization |

## Completed security controls

### OAuth

- Authorization codes require the active operator-held authorization key.
- The primary Compose profile generates a fresh 32-byte URL-safe authorization
  key at every Charon process start and prints it once in the startup log;
  explicit static-key deployments remain fail-closed and do not print the value.
- The non-cacheable pairing page uses Lethe's visual system and displays the
  exact client, callback, and protected resource receiving authorization.
- The authorization endpoint requires `response_type=code`, state, and S256
  PKCE for the public connector client.
- Redirects are parsed and validated by exact scheme and host plus the
  configured path rule.
- Authorization-code scopes are capped and intersected with the principal's
  current database grants on every MCP request.
- Optional author tokens retain only actor-scoped branch and commit authority;
  review, merge, and direct-write scopes are stripped.
- Confidential clients without a configured secret fail closed.
- Client-credential requests cannot choose elevated scopes.
- Consumed and expired authorization codes are removed, and the unused
  in-memory access-token registry was removed.

### Protected Memory Git merges

- Reviews have explicit `approve`, `request_changes`, and `reject` verdicts.
- A review is bound to the proposal source, selected operations, protected
  target ref, and target head.
- Only a current independent approval satisfies the merge gate.
- A later independent objection blocks the merge.
- Proposal authors cannot review or merge their own proposal.
- The approving reviewer's identity is carried into Lethe's separately signed
  protected-ref compare-and-swap request.
- Fractional, negative, duplicate, and non-numeric selected operation ordinals
  are rejected rather than truncated or silently reinterpreted.
- Concurrent review findings append atomically in SQLite.

### Lethe adapter

- Charon retains a typed upstream adapter and never exposes a generic Lethe
  proxy.
- Project, changeset, session, and manifest path values are escaped before URL
  construction.
- Redirects are refused.
- Every typed JSON response is read through an explicit byte limit that detects
  over-limit bodies instead of accepting a valid prefix.
- The E2E manifest verifier applies the same exact size-bound behavior.

### Credentials and principals

- Obol digesting, OAuth signing, protected-merge authorization, and OAuth
  pairing support separate secrets.
- `CHARON_HMAC_KEY` remains a documented migration fallback and is forwarded by
  the primary Compose profile.
- The container entrypoint does not generate or print HMAC signing keys. The
  Charon process prints only the intentionally short-lived browser authorization
  key when runtime generation is explicitly enabled.
- Host readiness checks keep the Lethe bearer credential out of process
  arguments by using a protected temporary header file.
- Disabled principals are rejected during authentication and can be explicitly
  enabled or disabled through the local CLI.
- Reconciliation does not silently re-enable disabled identities.
- Malformed credential timestamps and cryptographic-random failures fail closed.

## Completed reliability controls

- Public and admin servers use bounded headers, reads, writes, idle time, and
  graceful-shutdown windows.
- Public request bodies are capped before OAuth or MCP parsing.
- Process liveness and upstream readiness are separate endpoints.
- Unauthenticated mode is limited to an explicit loopback listener.
- Configuration values for result limits, request limits, timeouts, URLs,
  authentication modes, and key lengths are parsed and range-validated.
- SQLite schema changes and their migration markers commit atomically.
- The Charon data directory and database use owner-only permissions.
- Stale Unix admin sockets are removed only when the existing path is a socket.
- Audit ledger appends are serialized and transactional for Charon's documented
  single-process SQLite ownership model.
- Ledger sequence, predecessor, and entry hashes can be verified with
  `charon ledger verify`; concurrency and tamper tests are included.
- Ledger write failures are logged rather than silently discarded.
- Bootstrap Obols are written atomically and reminted when the reconciled
  principal changes.
- Obol rotation executes Charon through Compose so it operates on the named
  production data volume and revokes active UUID-based Obols correctly.

## Completed deployment and supply-chain controls

- Go requirement is 1.25.12.
- Builder and runtime images are pinned by digest.
- The image runs as UID/GID 10001.
- The primary Compose reference binds the MCP port to loopback, drops all Linux
  capabilities, enables `no-new-privileges`, uses a read-only root filesystem,
  limits PIDs and resources, and provides only the required writable data and
  temporary storage.
- Development, production-reference, and isolated Memory Git E2E behavior are
  separated.
- The previous tracked platform binary was replaced by a portable source
  launcher. Release binaries are generated under ignored `dist/` output.
- CI includes formatting, module verification, vet, Staticcheck, race tests,
  vulnerability scanning, shell validation, Actionlint, Compose validation,
  image build, and non-root assertions.
- Release verification repeats source and container gates and prepares
  checksums, build metadata, an SPDX SBOM, and provenance attestation.
- GitHub Actions are pinned to resolved commits and Dependabot covers Go,
  Actions, and Docker dependencies.

## Open-source project controls

- `SECURITY.md`
- `CONTRIBUTING.md`
- `CODE_OF_CONDUCT.md`
- `SUPPORT.md`
- `CHANGELOG.md`
- `docs/operations.md`

The operations runbook covers deployment boundaries, health, offline backup,
isolated restoration, ledger verification, key and Obol rotation, principal
lifecycle, connector upgrades, and incident response.

## AUTOREVIEW findings

### Cycle 1 accepted and fixed

1. Fractional merge operation ordinals were truncated.
2. Changeset and manifest identifier paths were not consistently escaped.
3. Reachable adapter methods bypassed the bounded JSON decoder.
4. The primary Compose profile omitted the legacy HMAC migration fallback.
5. The Lethe health probe placed its bearer credential in `curl` arguments.
6. Obol rotation targeted the host database instead of the Compose volume.
7. Confidential OAuth clients with empty secrets did not fail closed.
8. Bootstrap token state was not tied to the reconciled principal.
9. The Memory Git E2E client had not been updated for explicit review verdicts.

### Cycle 2 accepted and fixed

1. The OAuth authorization endpoint did not enforce `response_type=code`.

### Candidates rejected or deferred

- Named-volume initialization preserves the image-owned non-root `/data`
  permissions; no bind-mount ownership blocker was reproduced.
- Ledger serialization is correct for the documented single Charon writer.
  Multi-process shared-database operation and an external integrity anchor are
  future architecture work, not claims made by this deployment profile.
- Concurrent duplicate proposals cannot both persist because of the unique
  principal and idempotency index. Improving the losing caller's response is a
  follow-up, not a protected-state safety defect.
- The source launcher under `bin/charon` is intentionally tracked and tested;
  it is not a compiled release artifact.
- The pairing page already displayed the exact callback and protected resource,
  and direct inspection confirmed the missing-state path emits one redirect.

## Final local proof

The following passed after the final fix cycle:

- `git diff --check`
- clean `gofmt -l .`
- `go mod verify`
- `go vet ./...`
- `go test -race ./...`
- `go test -cover ./...`
- Staticcheck across all packages
- `govulncheck ./...` with zero reachable vulnerabilities
- interpreter-aware Bash and POSIX shell syntax checks
- Actionlint for all workflows
- primary Compose validation
- explicit legacy `CHARON_HMAC_KEY` Compose forwarding assertion
- isolated Memory Git Compose validation
- `go build ./...`
- local CLI and persistence smoke test
- cross-platform Darwin, Linux, and Windows release builds
- embedded Go build-information inspection for every release binary
- fresh container build from pinned images
- non-root image identity assertion (`10001:10001`)
- read-only isolated runtime smoke where `/livez` returned 200 and `/readyz`
  correctly returned 503 for an intentionally unavailable upstream
- source-level MCP schema test proving `memory_merge_review` advertises and
  requires `verdict`
- focused OAuth pairing, review-verdict, adapter-bound, ledger-concurrency, and
  tamper-detection tests under the race detector

Final package coverage observed during closeout included:

- policy: 96.3%
- config: 83.7%
- idempotency: 83.3%
- proposal: 74.0%
- store: 68.2%
- server: 69.0%
- OAuth: 64.6%
- Lethe adapter: 62.1%
- ledger: 58.7%
- MCP aggregate: 24.2%

Coverage is evidence, not a release claim. The critical authorization and
failure boundaries above have targeted negative tests even where aggregate MCP
coverage remains lower.

## Integration and external limitations

- The isolated Memory Git Charon container was rebuilt from the final local
  source and reached `/livez` successfully.
- The combined credential-file E2E command was blocked before execution by the
  platform secret-safety layer. That control was not bypassed.
- After the rebuild, the existing ChatGPT connector retained its pre-upgrade
  tool schema and its prior OAuth token returned 401. The connector must be
  refreshed or reconnected and complete a new paired authorization before the
  credentialed author-reviewer-merge-manifest scenario can be rerun against the
  final image.
- Earlier in this work, a Compose rendering command exposed local Lethe and
  Charon secret values in tool output. Those credentials must be treated as
  exposed and rotated before the environment is considered secure.
- GitHub-hosted CI, release, SBOM, and provenance jobs require a future remote
  branch or tag. Their workflow syntax and local command equivalents pass.
- Reverse-proxy TLS, DNS, load, penetration, and multi-host recovery testing are
  deployment-specific and were not performed.

These limitations prevent claiming that a particular production deployment is
validated. They do not represent remaining actionable source findings in the
reviewed local change bundle.

## Final disposition

AUTOREVIEW found no remaining actionable source finding after the final fix and
proof cycle.

Charon is a **local open-source production candidate**. It should not be labeled
as a validated production deployment until credentials are rotated, the
connector is reauthorized with the new schema, the final credentialed Memory Git
E2E scenario passes, and the intended remote CI/release jobs complete.

The checkout remains local, dirty, uncommitted, and unpushed by design.

---

## Local upgrade/recovery — 2026-07-15 through 2026-07-16

### Verified state after local-agent timeout

**Repositories**

- Charon remains on `memory-git-local` at `717e4b5`; the hardening bundle is local, dirty, uncommitted, and unpushed.
- Lethe remains on `lethe-git` at `0937a63`; its Memory Git work is also local and dirty.
- No new Git commit, tag, push, release, or protected Lethe merge was observed.

### Phase 1 — Baseline and credential-file handling: complete

The local agent captured the pre-upgrade baseline, then rewrote the two ignored
configuration files before recreating the services:

- Charon `.env`: mode `0600`, modified 2026-07-15 22:15 EDT.
- Lethe `.env.git`: mode `0600`, modified 2026-07-15 22:13 EDT.

The secret-safety layer blocked direct value or digest comparison, so credential
values were not inspected. Rotation is indirectly supported by the file rewrite
and container recreation times, and by Charon readiness succeeding against the
authenticated Lethe upstream. This record does not claim direct observation of
the old and new values.

### Phase 2 — Lethe rebuild: complete and verified

- `lethe-git-local` was rebuilt as image `openlethe/lethe-git:local`
  (`sha256:4a067ac29611...`) and recreated at 2026-07-15 22:13 EDT.
- The container is running as `appuser`, has zero restarts, is Docker-healthy,
  and publishes only `127.0.0.1:18485`.
- `docker compose -f docker-compose.git.yml --env-file .env.git config --quiet`
  passed.
- `git diff --check`, `gofmt` cleanliness, and `go test ./...` passed in Lethe.

### Phase 3 — Charon rebuild: complete and verified

- Primary Charon was rebuilt from the final local source as `charon:local`
  (`sha256:546dce559868...`) and recreated on 2026-07-16 at 00:15 EDT.
- Isolated test Charon was rebuilt separately as
  `charon-memory-git-test:local` (`sha256:fcc48f867b94...`) and recreated on
  2026-07-16 at 00:15 EDT; its existing named volume was preserved.
- Both containers run as `10001:10001`, use read-only root filesystems, drop all
  capabilities, enable `no-new-privileges`, have zero restarts, and are healthy.
- Primary and test `/livez` and `/readyz` endpoints returned HTTP 200.
- `charon ledger verify` passed with zero entries in the rebuilt primary
  database.
- The primary log contains exactly one generated 32-byte URL-safe authorization
  key entry by design. The checked long-lived credential identifiers were absent;
  the authorization key itself was not copied into this tracker or support output.

### Phase 4 — Source and Compose verification: complete with named tool gaps

The following were rerun against the current source and passed:

- Charon: both Compose files with `config --quiet`, `git diff --check`, clean
  `gofmt -l .`, `go mod verify`, `go vet ./...`, `go test -race ./...`,
  `go test -cover ./...`, `go build ./...`, and syntax checks for six shell
  scripts.
- Lethe: Git Compose with `config --quiet`, `git diff --check`, clean
  `gofmt -l .`, and `go test ./...`.
- Focused OAuth configuration, handler, server E2E, and command tests passed
  after each AUTOREVIEW-triggered correction.

`staticcheck`, `govulncheck`, and `actionlint` were not installed in the current
workspace environment, so no current pass is claimed for those three tools.

### Phase 5 — ChatGPT connector refresh: blocked in this session

The current ChatGPT capability inventory does not expose a callable Lethe MCP
action, and Plugin Management cannot install, reconnect, or execute the app.
The connector must be refreshed or recreated in the ChatGPT UI and complete a
new paired OAuth authorization. Until a live Lethe action is available, the
current tool schema, project grants, and connector token cannot be verified.

### Phase 6 — Lethe log verification and closeout changeset: not completed

Because no callable Lethe connector is available in this session, the
`Archimedes` refs and session log could not be read, and no missing hardening
closeout changeset was written through the live connector. The Charon repository
tracker remains the complete local record.

### Phase 7 — Credentialed final Memory Git E2E: not rerun after rebuild

Host E2E artifacts exist, but their timestamps are 2026-07-15 21:31 EDT, before
the final Lethe and Charon recreations. They prove an earlier E2E run, not the
post-rotation/post-rebuild environment. The final author-reviewer-protected-
merge-manifest-restart scenario remains pending after connector reauthorization.

### Phase 8 — Compose and tunnel verification after connector failure

The primary deployment file remains `docker-compose.yml`. Its `charon` service
is the source of `charon-charon-1`, binds `127.0.0.1:18484`, runs in OAuth-capable
Memory Git mode, and serves the OAuth discovery and authorization routes. The
separate `docker-compose.memory-git-test.yml` file remains the isolated Obol-auth
test stack named `charon-memory-git-test` on `127.0.0.1:18487`; neither file was
copied over the other.

The primary `.env` remains operational but still uses the documented legacy
`CHARON_HMAC_KEY` fallback for OAuth and Obol signing. It defines a separate
merge key but does not yet define `CHARON_OAUTH_HMAC_KEY` or
`CHARON_OBOL_HMAC_KEY`. Final key separation still requires two independently
generated keys and removal or emptying of the Charon-side legacy fallback after
safe local-only secret editing and service recreation.

Current direct probing shows the public route has been corrected:

- local primary port 18484 returns HTTP 200 for both OAuth discovery documents,
  HTTP 400 for a parameterless authorization request, and HTTP 401 with an OAuth
  `WWW-Authenticate` challenge for `/mcp`;
- local test port 18487 still returns HTTP 404 for all OAuth routes and HTTP 401
  for `/mcp`;
- `https://lethe.mwyatt.me` now matches the primary port-18484 signature, serves
  the styled no-store authorization page, and returns the public protected-
  resource metadata URL in the `/mcp` challenge.

The public secret-bearing authorization POST was not replayed through the tunnel
because the tool safety layer blocked that credential transmission. The same
POST path, using the current generated key without displaying it, passed locally
with HTTP 302, an authorization code, and the original state.

The previously exposed Cloudflare Tunnel token must still be treated as
compromised. No Cloudflare administrative capability was available to verify or
perform token rotation, so rotation remains unverified even though routing is
currently correct.

### Phase 9 — Runtime authorization-key and UI recovery

- Primary Compose defaults `CHARON_OAUTH_GENERATE_PAIRING_SECRET=true`.
- Charon uses `crypto/rand` to generate 32 bytes and emits one URL-safe key in
  the startup log; a forced generation overrides any stale configured pairing
  value for that process.
- Non-Compose and explicit static-key launches remain fail-closed unless a valid
  static key is configured or generation is explicitly enabled.
- The authorization page mirrors Lethe's palette, textured background, cards,
  badges, typography, responsive layout, focus states, and error presentation.
- Invalid keys return an actionable styled HTTP 401; copy/paste whitespace is
  normalized before constant-time comparison.
- Local behavior validation confirmed the generated key, styled GET, invalid-
  key rejection, and valid code redirect without exposing the key.

### Phase 10 — AUTOREVIEW and operational closeout status

GPT-5.6 Thinking at high reasoning reviewed the frozen local authorization-key
and UI change bundle. Two accepted in-scope findings were corrected:

1. Generation originally occurred implicitly when a non-Compose static key was
   missing, weakening the existing fail-closed contract. Generation now occurs
   only when explicitly enabled; primary Compose enables it by default.
2. The page copy originally implied every deployment used a changing log key.
   It now accurately distinguishes the primary generated-key profile from
   supported static-key deployments.

The final source-aware pass found no remaining accepted actionable finding in
the frozen scope. Operational closeout remains blocked by purpose-specific key
migration, Cloudflare tunnel-token rotation verification, ChatGPT connector
reauthorization, live Lethe log reconciliation, the post-rebuild credentialed
Memory Git E2E, and unavailable `staticcheck`, `govulncheck`, and `actionlint`
executables. No remote action was performed.

## Production-remediation sprint — 2026-07-16

Independent production-readiness audit findings (CHARON_LETHE_PRODUCTION_REMEDIATION_HANDOFF.md)
remediated on the `remediation/memory-git-local` branch. Every work package
ships with regression tests; all local gates pass (gofmt, vet, staticcheck,
race tests, gosec, govulncheck — 0 affecting vulnerabilities).

### Correctness and integrity (WP1–WP5)

- Conflict analysis is pure; conflicts persist only bound to proposals with
  deterministic identity and a complete lifecycle
  (`open|resolved|rejected|superseded|canceled|deferred`). Cancel/reject
  retires; landed merges supersede; accepted context exposes only open,
  head-relevant conflicts.
- Merge-proposal creation is atomically idempotent: proposal row + canonical
  request digest + idempotency record commit in one transaction. Exact replay
  returns the original; same-key different-body fails closed.
- Persisted proposal state fails closed on any corruption (JSON, timestamps,
  invalid values); a canonical proposal-state digest is recomputed on every
  mutation and verified on every load; legacy rows migrate via startup
  backfill or are quarantined.
- Memory Git writes use strict typed parsing: unknown fields and malformed
  elements reject the whole request before any side effect; operation count
  and order are preserved exactly.
- Lethe enforces versioned (memory_git/v1) semantic validation for every
  operation before immutable history: content/kind/visibility/confidence/tags
  invariants, target existence and identity collisions against sequentially
  applied parent state, trust transitions, self-relation and self-supersede
  bans, attestation-marker provenance, cross-project rejection.

### Authorization, audit, and durability (WP6–WP9)

- Protected-merge authorization is a versioned, expiring, single-use envelope
  (memory-git-merge/v2) bound to the exact merge fields and proposal-state
  digest, with key IDs and rotation overlap; Lethe consumes the nonce
  atomically with the CAS, enforces merge shape, and records a durable
  advancement record. Generic-key fallback formally deprecated.
- Every security-relevant mutation records a durable audit intent before its
  side effect and completes/fails/marks-ambiguous after; mutations fail
  closed when audit persistence is unavailable; restart reconciliation marks
  stranded intents ambiguous. Ledger entries carry purpose-specific HMAC MACs
  with key rotation; an external append-only anchor detects wholesale chain
  replacement.
- Both stores run WAL + synchronous=FULL + foreign_keys with startup
  verification (fail closed on unsupported storage) and checkpoint-on-close;
  abrupt-kill survival is tested.
- Loopback-only trust is the default; non-loopback binds require API keys;
  Charon-to-Lethe requires HTTPS, a Unix socket, or loopback HTTP (dev
  override is explicit and logged); bearer secrets never appear in errors.

### Scale, recovery, and observability (WP10–WP12)

- Historical reconstruction uses a bulk loader (recursive-CTE skeleton with
  O(n) primary-key lookups, batched loads, iterative exact DFS post-order
  proven identical to the legacy recursive traversal on random DAGs),
  one-traversal reachability+reconstruction, batched source-event resolution,
  and a configurable fail-closed history limit. Benchmarks: 1k ≈ 13ms,
  10k ≈ 140ms, 100k ≈ 1.6s (was 65s at 10k before the O(n) fix).
- Coordinated backup/restore: `charon backup` manifests both databases;
  `charon reconcile` + `lethe verify-chain` validate restores; recovery
  read-only modes block mutations until reconciliation passes.
- Prometheus-text metrics on both services (Charon `/admin/metrics`, Lethe
  `/api/metrics`), liveness/readiness split, and documented SLOs, alert
  thresholds, and runbooks in `docs/observability.md`.

### Remaining risks and follow-ups

- Remote CI evidence, signed/attested container images, SBOM, and provenance
  require pushing to a remote with CI (Charon currently has no git remote).
- The destructive end-to-end matrix (container kills, host restarts, disk
  full) is partially covered by unit-level fault tests; full drills need an
  isolated environment.
- `mTLS` for Charon-to-Lethe is not implemented; HTTPS or Unix socket are the
  supported production transports.
- OAuth pairing/connector reauthorization and live tunnel verification remain
  operator tasks from earlier phases.

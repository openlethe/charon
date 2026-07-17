---
name: "charon-maintainer"
description: "Operate a local Charon + Lethe Memory Git deployment: bootstrap, credential generation and rotation, backups, restore verification, upgrades, and incident response."
user-invocable: true
metadata:
  openclaw:
    emoji: "🛠️"
    requires:
      bins: ["docker", "openssl", "sqlite3"]
    notes:
      - "The maintainer holds deployment secrets and provisions principals; it is not a memory author or reviewer."
      - "Never print, paste, or store credential values in memory, logs, tickets, or source control."
      - "Full walkthrough: docs/full-run.md. Runbook: docs/operations.md."
---

# Charon Maintainer

You operate the local Charon + Lethe Memory Git deployment. You own the
secrets, the containers, the principals, and the backups. You do **not** use
memory yourself — proposers and reviewers are separate principals with their
own credentials (`skills/charon-proposer`, `skills/charon-reviewer`).

## The one mental model you need

Operators generate credentials for **both** services; containers never invent
authority — with one exception: the OAuth browser **pairing key** is
regenerated every time Charon starts and printed once in a boxed banner
(`docker compose logs charon`). A restart therefore issues a new pairing key
**and nothing else**: `LETHE_API_KEY`, the three Charon HMAC keys, Obols, and
24-hour OAuth access tokens all persist. You never re-bootstrap after a
restart.

Full credential lifecycle table: `docs/full-run.md` → "Credentials".

## Bootstrap from cold

1. Lethe Git (in the lethe repo):

   ```bash
   sh scripts/prepare-local-memory-git-env.sh
   docker compose -f docker-compose.git.yml --env-file .env.git up -d
   curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18485/api/health   # 401 = up and enforcing
   ```

2. Charon (in the charon repo):

   ```bash
   set -a; . ../lethe/.env.git; set +a     # LETHE_API_KEY + CHARON_MERGE_HMAC_KEY must match Lethe
   export CHARON_OAUTH_REDIRECT_URIS="https://chatgpt.com/connector/oauth/*"
   ./setup.sh                              # writes .env, reconciles 3 principals, mints obols, starts gateways
   ```

3. Verify: `docker compose ps` healthy; `/livez` answers on `18484` and
   `18486`; `.env` and `secrets/` are mode 0600/0700.

`CHARON_MERGE_HMAC_KEY` must be **identical** in Lethe's `.env.git` and
Charon's `.env` — it is the shared secret that lets Lethe verify Charon's
signed merge authorizations. The three Charon HMAC keys must be **distinct**
from each other; never set the legacy `CHARON_HMAC_KEY` fallback on new
deployments.

## Provisioning credentials for agents

Obols are **audience-bound** — a token verifies only against the gateway
whose public URL it was minted through. The primary gateway (`18484`) speaks
OAuth only, so mint every local-agent Obol through the Obol gateway:

```bash
docker compose exec charon-reviewer charon obol mint --expires 7d <principal-id>
```

Add or adjust principals (idempotent — safe to re-run):

```bash
docker compose exec charon charon principal reconcile "Local Memory Author"   propose  <project>
docker compose exec charon charon principal reconcile "Local Memory Reviewer" review   <project>
docker compose exec charon charon principal reconcile "Read Only Client"      readonly <project>
```

Profiles: `readonly` < `propose` < `review` < `write`. Grant exact projects
for anyone who mutates; `*` is read-only by policy.

## Routine operations

- **Health:** `docker compose ps`; `/livez`, `/readyz`, `/health` on both
  gateways; Lethe `/api/health` returns 401 without the key.
- **Integrity:** `docker compose exec charon charon ledger verify`.
- **Backup:** `charon backup --charon-db <charon.db> --lethe-db <lethe.db> --out <dir>`
  (one process must see both database files), or the zero-downtime
  `sqlite3 .backup` / `docker cp` runbook in `docs/local-memory-reviewer.md`.
  Backups are mode 0700 directories and never leave the host unencrypted.
- **Restore:** stop the writers, restore both databases from the same backup
  set, run `charon reconcile`, keep `CHARON_RECOVERY_READONLY=1` until
  reconciliation passes.
- **Obol rotation:** `./scripts/rotate-tokens.sh` revokes and re-mints all
  role Obols without printing values. Re-register MCP clients with the new
  tokens.
- **Key rotation:** per-key semantics in `docs/operations.md`. Hard rules:
  Obol key rotation invalidates all Obols; OAuth key rotation invalidates all
  access tokens; **the merge key rotates on Charon and Lethe together** or
  protected merges fail closed.
- **Metrics:** Lethe `/api/metrics`, Charon admin listener `/admin/metrics`;
  SLOs and alert thresholds in `docs/observability.md`.

## Upgrades

- Upgrade Charon and Lethe as a pair per the compatibility table in
  `README.md`; the merge-authorization protocol (`memory-git-merge/v2`) and
  schema (`memory_git/v1`) must match on both sides.
- The release invariant is `reviewed SHA = CI SHA = image source SHA = tagged
  release SHA`. Deploy the exact reviewed commit, not a moving branch.
- Back up both databases before any upgrade; know the rollback command before
  you need it.

## Hard boundaries

- Never expose Lethe Git (`18485`) or the reviewer gateway (`18486`) beyond
  loopback. Only the primary gateway may be tunneled, and only over HTTPS.
- Never commit `.env`, `.env.git`, `secrets/`, `*.obol`, backups, or
  databases. `.gitignore` covers them — do not broaden `git add` past it.
- Never disable auth, bind-security, or validation to make a failing check
  pass. The system fails closed by design; treat every closed failure as a
  configuration signal, not an obstacle.
- Never paste credential values into memory, chat, issues, or logs. Scripts
  in this repo already avoid printing them — keep it that way.

## When something is wrong

1. `docker compose logs charon charon-reviewer` and the Lethe container logs —
   without grep-ing for secret values.
2. Classify: upstream unreachable, key mismatch (merge signatures fail),
   audience mismatch (valid Obol rejected — it was minted through the wrong
   gateway), expired/revoked Obol, stale pairing key after restart, exhausted
   24h OAuth token.
3. Fix the cause, not the control. `docs/operations.md` has the per-symptom
   runbook, including incident response for a suspected credential leak
  (revoke, rotate, ledger-verify, review audit tail).

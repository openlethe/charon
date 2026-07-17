# Local Memory Reviewer Runbook

> **Scope:** Configure and operate a local agent as a dedicated, independent
> Charon Memory Git reviewer for one exact project.
> Substitute your own case-sensitive project ID wherever `<project>` appears,
> and record the values your deployment produces in the table at the end.

## Architecture

```text
Public MCP clients                  Local reviewer agent
        | OAuth                                  | Obol
        v                                        v
Charon main gateway                    Charon reviewer gateway
charon-charon-1                         charon-charon-reviewer-1
127.0.0.1:18484                         127.0.0.1:18486
        \                                      /
         \ shared policy DB + private upstream /
          v                                   v
             Lethe Git (lethe-git-local)
                    127.0.0.1:18485
```

**Data ownership:**

- The main and reviewer Charon gateways share principals, grants, scopes, proposals, review findings, Obols, idempotency records, and the audit ledger in `/data/charon.db` inside the persistent `charon_charon-data` Docker volume. Ledger appends acquire a cross-process SQLite write lock before reading the predecessor hash.
- Lethe Git (`lethe-git-local`) owns semantic changesets, ref heads, accepted shared memory, protected-ref history, context manifests, and conflict records. It stores these in `/data/lethe.db` inside the host bind-mounted data directory (`./lethe-git-data` in the Lethe repository by default).
- OpenClaw (or another MCP client) stores the MCP server definition in its own client configuration.
- The reviewer's Obol credential is stored outside version control in a mode-`0600` file, conventionally `secrets/reviewer-obol.txt` in the Charon repository.

**Separation of duties:**

- Authoring agents (e.g. ChatGPT, Claude) use their own principals and credentials. They must not use the reviewer principal.
- The reviewer principal has only read, review, and merge scopes. It cannot create branches, commit changesets, or write directly to protected refs.
- The reviewer must never review or merge a proposal it authored. If the local agent needs to propose memory, a separate independent reviewer must review it.

## Database locations

| Service | Container | Database path inside container | Host path |
|---------|-----------|-------------------------------|-----------|
| Charon policy DB | `charon-charon-1`, `charon-charon-reviewer-1` | `/data/charon.db` | Docker volume `charon_charon-data` |
| Lethe Git | `lethe-git-local` | `/data/lethe.db` | `<lethe-repo>/lethe-git-data/lethe.db` |

Both databases use SQLite WAL mode. Neither is ephemeral.

## Backup and restore procedure

Store backups in a dedicated directory (for example `backups/charon-lethe-git/`) with mode `0700`, outside version control.

### Lethe Git backup (no downtime)

```bash
sqlite3 <lethe-repo>/lethe-git-data/lethe.db \
  ".backup backups/charon-lethe-git/lethe-git-before-<reason>.db"

sqlite3 backups/charon-lethe-git/lethe-git-before-<reason>.db \
  "PRAGMA integrity_check;"
```

### Charon backup (requires brief restart)

Charon runs in read-only containers with one SQLite database in a Docker volume. The host cannot access the volume file directly, so stop both database users for a consistent snapshot:

```bash
cd <charon-repo>
docker stop charon-charon-reviewer-1 charon-charon-1
docker cp charon-charon-1:/data/charon.db \
  backups/charon-lethe-git/charon-raw.db
sqlite3 backups/charon-lethe-git/charon-raw.db \
  ".backup backups/charon-lethe-git/charon-before-<reason>.db"
rm backups/charon-lethe-git/charon-raw.db
sqlite3 backups/charon-lethe-git/charon-before-<reason>.db \
  "PRAGMA integrity_check;"
docker compose up -d charon charon-reviewer
```

### Restore

To restore Charon from backup:

1. Stop `charon-charon-reviewer-1` and `charon-charon-1`.
2. Copy the backup `.db` file into the container at `/data/charon.db`.
3. Start `charon-charon-1`.

To restore Lethe Git from backup:

1. Stop `lethe-git-local`.
2. Copy the backup `.db` file over `<lethe-repo>/lethe-git-data/lethe.db`.
3. Start `lethe-git-local`.

## Reviewer-principal setup

Create the reviewer principal with review-only scopes for the exact project:

```bash
cd <charon-repo>
docker compose exec charon-reviewer charon principal reconcile \
  "Local Memory Reviewer" \
  review \
  <project>
```

This grants:

- `memory.read`
- `memory.review`
- `memory.merge`
- `memory.search`
- `proposal.review`
- `thread.read`

It does **not** grant `memory.branch`, `memory.commit`, `memory.propose`, or `memory.write`.

Capture the returned `principal_...` ID and record it as the reviewer principal for all later steps.

## Local agent MCP configuration

1. Mint an Obol for the reviewer principal. Obols are audience-bound to the gateway that mints them, so mint through the Obol listener (`charon-reviewer`), not the OAuth-only primary gateway:

```bash
cd <charon-repo>
docker compose exec charon-reviewer charon obol mint --expires 7d <reviewer-principal-id>
```

2. Store the credential securely, for example `secrets/reviewer-obol.txt` in the Charon repository (covered by `.gitignore`):

```bash
chmod 600 secrets/reviewer-obol.txt
```

3. Add the MCP server to the client. Use a script or non-interactive command to avoid leaving the token in shell history (OpenClaw example):

```bash
TOKEN=$(grep -oE 'Token: obol_[A-Za-z0-9_-]+' \
  secrets/reviewer-obol.txt \
  | sed 's/Token: //')

openclaw mcp add charon-local-reviewer \
  --url http://127.0.0.1:18486/mcp \
  --transport streamable-http \
  --header "Authorization=Bearer ${TOKEN}"

openclaw mcp reload
openclaw mcp probe charon-local-reviewer
```

Do not connect directly to Lethe Git. All agent access must pass through Charon so project grants, scopes, provenance, review rules, and audit logging are enforced.

## Proposal review criteria

Approve a merge proposal only when **all** of the following are true:

- Project is exactly the intended, case-sensitive project ID.
- Target ref is the intended protected shared ref (`refs/shared/main`).
- Proposal originator is a different principal from the reviewer.
- Source changeset belongs to the same project.
- Proposal base and current target state are understood.
- No blocking conflicts exist (`conflict_ids_json` is empty).
- Operation ordinals are valid.
- Content is durable and useful across sessions.
- Content contains no credentials, secret values, private chain-of-thought, or unnecessary personal data.
- Outcomes, decisions, verification, and open threads are represented accurately.
- Claims of testing or completion have supporting evidence.
- The proposal is not stale relative to the content the author intended to share.

### Stale-proposal handling

A Memory Git proposal is pinned to the exact source changeset used at creation time. If the topic branch has advanced since the proposal was created, the proposal does **not** automatically include the newer changesets.

When a proposal is stale:

1. Read the proposal's `head_changeset_id`.
2. Read the current topic ref head with `memory_ref_list` or `memory_log`.
3. If the topic ref head differs from the proposal source, do not merge.
4. Record `request_changes` or `reject` with a clear explanation.
5. Notify the originating principal that a new proposal must be created from the intended current branch head.
6. Only the originating principal may cancel the stale proposal via `memory_merge_cancel`.

## Merge and verification procedure

For every pending proposal:

1. **Resolve exact state:** read the proposal ID, project, originator, base/source changesets, target ref, target head, selected ops, conflicts, and review findings.
2. **Inspect the source:** use `memory_show`, `memory_log`, and `memory_diff` to review the exact source changeset, not the branch name.
3. **Apply review policy:** approve, request changes, or reject based on the criteria above.
4. **Merge approved proposals:**

```text
memory_merge({
  "proposal_id": "<proposal-id>",
  "idempotency_key": "<unique-key-for-this-exact-merge>"
})
```

Do **not** use `charon proposal approve <proposal-id>`. That CLI command belongs to the legacy proposal system and fails closed for Memory Git merges.

5. **Verify after merge:**
   - Proposal status changed from `pending` to `approved`/`completed`.
   - A resulting changeset ID was returned.
   - `refs/shared/main` advanced to the expected resulting changeset.
   - The resulting changeset is readable with `memory_show`.
   - `memory_context_at` can retrieve accepted memory from `refs/shared/main`.
   - The Charon audit ledger (`charon ledger tail`) contains the review and merge operations.
   - The source branch remains intact.
   - No unrelated ref moved.

## Credential rotation

To rotate the reviewer Obol:

1. Mint a new Obol for the reviewer principal (through the `charon-reviewer` gateway, as above).
2. Update the MCP server header in the client with the new token.
3. Reload the MCP runtime.
4. Revoke the old Obol:

```bash
cd <charon-repo>
docker compose exec charon-reviewer charon obol revoke <old-obol-id>
```

`scripts/rotate-tokens.sh` performs this for all deployment roles at once without printing token values.

## Incident recovery

| Symptom | Likely cause | Check first |
|---------|-------------|-------------|
| MCP connection fails | Reviewer gateway not running | Check `charon-charon-reviewer-1` |
| `unauthorized` on MCP | Obol expired/revoked, wrong principal, or token minted with the wrong gateway audience | `charon obol ls <principal-id>` |
| Missing write tools | Reviewer principal lacks author scopes (by design) | `memory_status` scopes |
| Merge denied | No current approval or stale proposal | Proposal `review_findings_json` and target head |
| `memory_status` unavailable | Client safety layer | Use `memory_ref_list`, `memory_log`, `memory_context_at` |

## Operational notes

- Both Charon gateways must run in `memory-git` mode and point to Lethe Git (`http://host.docker.internal:18485` in the reference Compose deployment), not OpenLethe on `18483`.
- The main `18484` gateway is OAuth-only; the loopback-only `18486` reviewer gateway is Obol-only. The isolated `18487` E2E gateway remains a separate test system.
- The protected shared memory ref is `refs/shared/main`.
- Never write directly to `refs/shared/*`.
- Review and merge must remain fail-closed.
- Produce notifications only when there is actionable work; silent success is acceptable.

## Deployment record

Record the values your deployment produces:

| Item | Value |
|------|-------|
| Reviewer principal | `<principal_...>` |
| MCP endpoint | `http://127.0.0.1:18486/mcp` |
| Project | `<project>` |
| Protected ref | `refs/shared/main` |
| Charon container | `charon-charon-1` |
| Reviewer container | `charon-charon-reviewer-1` |
| Lethe Git container | `lethe-git-local` |
| Charon database backup | `<backup-dir>/charon-before-<reason>.db` |
| Lethe Git database backup | `<backup-dir>/lethe-git-before-<reason>.db` |
| Reviewer credential | `secrets/reviewer-obol.txt` (mode `0600`) |
| MCP client server entry | `<client-specific>` |

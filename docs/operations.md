# Charon Operations Runbook

This runbook covers the hardened single-host deployment in `docker-compose.yml`.

## Deployment boundary

- Keep Lethe on a private network reachable only by Charon.
- Terminate public TLS at a trusted reverse proxy or tunnel.
- Keep the published Charon port bound to loopback.
- Use `memory-git` mode unless direct compatibility is deliberately required.
- Run Charon writers against one SQLite database only on the same Docker host
  and only with builds that serialize ledger appends using `BEGIN IMMEDIATE`.
  Never share the database over NFS or between hosts.
- Use exact project grants for mutation-capable principals.

## Health endpoints

- `GET /livez` reports process liveness.
- `GET /readyz` checks Charon and the authenticated Lethe upstream.
- `GET /health` remains a compatibility alias for readiness.
- The local admin listener exposes matching `/admin/livez`, `/admin/readyz`, and `/admin/health` routes.

A Lethe outage should make Charon unready without causing a container restart loop.

## Startup verification

```bash
curl --fail --silent http://127.0.0.1:18484/livez
curl --fail --silent http://127.0.0.1:18484/readyz

docker compose exec -T charon charon version
docker compose exec -T charon charon ledger verify
```

For OAuth deployments, verify metadata through the public HTTPS URL and complete one operator-paired authorization using a least-privilege test principal.

## Connector upgrade procedure

The protected-merge review contract requires an explicit verdict. After an
upgrade that changes the MCP schema:

1. Rebuild and restart Charon.
2. Refresh or reconnect the ChatGPT connector so it reloads the tool schema.
3. Complete a new operator-paired OAuth authorization when the previous token is rejected.
4. Confirm `memory_merge_review` advertises the required `verdict` field.
5. Verify author principals still lack review/merge authority and reviewer principals still lack branch/commit authority.

Do not make `verdict` optional merely to preserve a stale connector schema; the
explicit decision is part of the protected-ref authorization boundary.

## Backup

Charon's database contains principal grants, credential digests, proposals, idempotency records, and audit metadata. Treat backups as sensitive.

Use a consistent offline snapshot:

1. Stop Charon cleanly with `docker compose stop charon`.
2. Identify the named volume with `docker compose config --volumes` and `docker volume ls`.
3. Archive the volume using a temporary container and a protected destination. Replace `<volume>` with the resolved volume name:

   ```bash
   mkdir -p backups
   chmod 700 backups
   docker run --rm \
     -v <volume>:/data:ro \
     -v "$PWD/backups:/backup" \
     alpine:3.23 \
     sh -c 'umask 077; tar -C /data -czf /backup/charon-data.tgz .'
   ```

4. Restart with `docker compose start charon` and verify `/readyz`.

Encrypt backups at rest and test restoration periodically. Do not copy only `charon.db` from a live service; SQLite WAL state may be omitted.

## Restore test

Restore into an isolated named volume before touching production:

1. Extract the archive while Charon is stopped.
2. Start an isolated Charon instance with matching deployment configuration.
3. Run `charon version` and `charon ledger verify`.
4. Confirm expected principals and read-only Memory Git behavior.
5. Permit mutations only after the isolated restore passes.

## Audit integrity

Run regularly and after every restore:

```bash
docker compose exec -T charon charon ledger verify
```

A failure means the metadata chain is inconsistent or altered. Preserve the database and logs, stop mutation traffic, capture a protected backup, and investigate before repair. Alert on the log message `audit ledger write failed`.

## Key rotation behavior

Charon separates credential digesting, OAuth signing, protected-merge authorization, and OAuth pairing. Rotate each purpose independently.

- Rotating the Obol key invalidates existing Obols.
- Rotating the OAuth signing key invalidates existing OAuth access tokens.
- Rotate the merge key on Charon and Lethe as one maintenance operation; protected merges fail while the values differ.
- The primary Compose profile generates a fresh 32-byte URL-safe OAuth authorization key at every process start and logs it once. Restarting Charon invalidates the previous browser-pairing key but does not revoke existing access tokens.
- To use an externally managed static pairing value, set `CHARON_OAUTH_GENERATE_PAIRING_SECRET=false` and configure a 32+ character `CHARON_OAUTH_PAIRING_SECRET`; static values are not logged.

Rotate locally managed Obols against the Compose database with:

```bash
./scripts/rotate-tokens.sh
```

After rotation, verify liveness, readiness, least-privilege access, and the intended merge workflow. Treat Charon startup logs as sensitive whenever generated OAuth pairing is enabled, and never copy the authorization key into support output or durable records.

## Principal lifecycle

Disable a compromised or retired identity immediately:

```bash
charon principal disable <principal-id>
```

Disabled principals cannot authenticate and are not silently re-enabled by reconciliation. Re-enable explicitly only after investigation:

```bash
charon principal enable <principal-id>
```

Then reconcile the exact intended projects and permission profile.

## Incident response

1. Disable affected principals.
2. Stop public mutation traffic.
3. Preserve logs and take an offline protected backup.
4. Run `charon ledger verify`.
5. Rotate affected credentials and purpose-specific keys.
6. Verify protected-ref heads and open merge proposals.
7. Re-enable only the minimum required principals and scopes.
8. Report product vulnerabilities through `SECURITY.md` without publishing credentials or private memory content.
